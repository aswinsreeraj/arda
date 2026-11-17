package gateway

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"gorm.io/gorm"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/mailer"
	"github.com/aswinsreeraj/arda/pkg/mcp"
	"github.com/aswinsreeraj/arda/pkg/mcp/simulator"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/pkg/toolgateway"
	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/aswinsreeraj/arda/services/gateway/credentials"
	gatewayhttp "github.com/aswinsreeraj/arda/services/gateway/http"
	"github.com/aswinsreeraj/arda/services/gateway/identity"
	"github.com/aswinsreeraj/arda/services/gateway/proxy"
	"github.com/aswinsreeraj/arda/services/gateway/ratelimit"
	registrygrpc "github.com/aswinsreeraj/arda/services/registry/grpc"
	"github.com/aswinsreeraj/arda/services/registry/service"
	"github.com/aswinsreeraj/arda/services/registry/store"
	reviewevents "github.com/aswinsreeraj/arda/services/review/events"
	reviewgrpc "github.com/aswinsreeraj/arda/services/review/grpc"
	reviewservice "github.com/aswinsreeraj/arda/services/review/service"
	reviewstore "github.com/aswinsreeraj/arda/services/review/store"
	"github.com/google/uuid"
	"strings"
)

// Config holds runtime configuration for API Gateway v1.
type Config struct {
	Port                   string
	JWTSecret              []byte
	JWTIssuer              string
	JWTAudience            string
	RateLimitRPS           float64
	RateLimitBurst         int
	RateLimitEnabled       bool
	ReliabilityServiceAddr string
	ExecutionServiceAddr   string
	TemporalAddr           string
	TemporalNamespace      string
	TemporalTaskQueue      string
	RegistryGRPCAddr       string
	ReviewGRPCAddr         string
	RedpandaBrokers        string
	RedpandaProducerSecret string
	VaultAddress           string
	VaultToken             string
	VaultMount             string
}

// Server encapsulates the API Gateway HTTP server and dependencies.
type Server struct {
	Handler             http.Handler
	JWTValidator        *auth.TokenValidator
	RateLimiter         *ratelimit.Limiter
	RegistryClient      *proxy.RegistryClient
	ReviewClient        *proxy.ReviewClient
	ReliabilityService  proxy.ReliabilityService
	ExecutionClient     proxy.ExecutionClient
	TemporalClient      *temporal.Client
	CredentialService   *credentials.Service
	IdentityService     *identity.Service
	inProcessGRPC       *registrygrpc.Server
	inProcessReviewGRPC *reviewgrpc.Server
	OutboxRelay         *reviewevents.OutboxRelay
	outboxCancel        context.CancelFunc
}

// NewServer builds and validates the API Gateway. It fails closed if auth configuration is missing.
func NewServer(cfg Config, db *gorm.DB) (*Server, error) {
	// Fails closed if JWT configuration is missing or invalid
	jwtValidator, err := auth.NewTokenValidator(auth.JWTConfig{
		Secret:   cfg.JWTSecret,
		Issuer:   cfg.JWTIssuer,
		Audience: cfg.JWTAudience,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize jwt authentication (failing closed): %w", err)
	}

	rateCfg := ratelimit.DefaultConfig()
	if cfg.RateLimitRPS > 0 {
		rateCfg.RPS = cfg.RateLimitRPS
	}
	if cfg.RateLimitBurst > 0 {
		rateCfg.Burst = cfg.RateLimitBurst
	}
	rateCfg.Enabled = cfg.RateLimitEnabled
	limiter := ratelimit.NewLimiter(rateCfg)

	// Optionally initialize gRPC reliability client if upstream address is set
	var relSvc proxy.ReliabilityService
	if cfg.ReliabilityServiceAddr != "" {
		relClient, err := proxy.NewReliabilityClient(cfg.ReliabilityServiceAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to reliability upstream: %w", err)
		}
		relSvc = relClient
	}

	// Optionally initialize Temporal client if upstream address is set
	var tempClient *temporal.Client
	if cfg.TemporalAddr != "" {
		tc, err := temporal.NewClient(temporal.Config{
			Address:   cfg.TemporalAddr,
			Namespace: cfg.TemporalNamespace,
			TaskQueue: cfg.TemporalTaskQueue,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to initialize temporal client: %w", err)
		}
		tempClient = tc
	}

	// Initialize Agent Registry gRPC connection
	var regClient *proxy.RegistryClient
	var regServiceInstance *service.RegistryService
	var inProcessGRPC *registrygrpc.Server
	if db != nil {
		regStore := store.NewPostgresStore(db)
		regServiceInstance = service.NewRegistryService(regStore)
	}
	if cfg.RegistryGRPCAddr != "" {
		rc, err := proxy.NewRegistryClient(cfg.RegistryGRPCAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to registry grpc service: %w", err)
		}
		regClient = rc
	} else if regServiceInstance != nil {
		// Spawn in-process gRPC server over bufconn so Gateway talks to Registry exclusively via gRPC
		inProcessGRPC = registrygrpc.NewServer(regServiceInstance)
		lis := bufconn.Listen(1024 * 1024)
		go func() {
			_ = inProcessGRPC.Serve(lis)
		}()
		conn, err := grpc.NewClient("passthrough://bufnet",
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return lis.Dial()
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to dial in-process registry grpc: %w", err)
		}
		regClient = proxy.NewRegistryClientFromConn(conn)
	}

	// Initialize Human Review gRPC connection
	var revClient *proxy.ReviewClient
	var inProcessReviewGRPC *reviewgrpc.Server
	var outboxRelay *reviewevents.OutboxRelay
	if cfg.ReviewGRPCAddr != "" {
		rc, err := proxy.NewReviewClient(cfg.ReviewGRPCAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to review grpc service: %w", err)
		}
		revClient = rc
	} else if db != nil {
		revStore := reviewstore.NewPostgresStore(db)
		var producer reviewevents.EventProducer
		brokerAddr := cfg.RedpandaBrokers
		if brokerAddr == "" {
			brokerAddr = "127.0.0.1:19092"
		}
		if kp, err := reviewevents.NewKafkaProducer(brokerAddr); err == nil {
			producer = kp
		} else {
			producer = reviewevents.NewMockProducer()
		}
		outboxRelay = reviewevents.NewOutboxRelay(revStore, producer)
		var signaler reviewservice.TemporalSignaler
		if tempClient != nil {
			signaler = tempClient.RawClient()
		}
		revSvc := reviewservice.NewService(
			revStore,
			reviewservice.WithTemporalSignaler(signaler),
			reviewservice.WithProducerSecret(cfg.RedpandaProducerSecret),
			reviewservice.WithOutboxRelay(outboxRelay),
		)
		inProcessReviewGRPC = reviewgrpc.NewServer(revSvc)
		lis := bufconn.Listen(1024 * 1024)
		go func() {
			_ = inProcessReviewGRPC.Serve(lis)
		}()
		conn, err := grpc.NewClient("passthrough://bufnet-review",
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return lis.Dial()
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to dial in-process review grpc: %w", err)
		}
		revClient = proxy.NewReviewClientFromConn(conn)
	}

	// Initialize Vault and Credential Service
	var credSvc *credentials.Service
	var credHandler *credentials.Handler
	vaultCfg := vault.ConfigFromEnv()
	if cfg.VaultAddress != "" {
		vaultCfg.Address = cfg.VaultAddress
	}
	if cfg.VaultToken != "" {
		vaultCfg.Token = cfg.VaultToken
	}
	if cfg.VaultMount != "" {
		vaultCfg.MountPath = cfg.VaultMount
	}
	if vaultCfg.Token == "" {
		vaultCfg.Token = "root"
	}
	if vaultCfg.Address == "" {
		vaultCfg.Address = "http://127.0.0.1:8200"
	}

	var secretStore vault.SecretStore
	if vClient, err := vault.NewClient(vaultCfg); err == nil {
		secretStore = vClient
	} else {
		secretStore = vault.NewMemoryStore()
	}

	var credStore credentials.Store
	if db != nil {
		credStore = credentials.NewPostgresStore(db)
	} else {
		credStore = credentials.NewMemoryStore()
	}
	credSvc = credentials.NewService(secretStore, credStore)
	credHandler = credentials.NewHandler(credSvc, gatewayhttp.WriteErrorResponse)

	// Initialize Identity, User Accounts, and Tenant Membership Service
	var identStore identity.Store
	if db != nil {
		identStore = identity.NewPostgresStore(db)
	} else {
		identStore = identity.NewMemoryStore()
	}
	identSvc := identity.NewService(identStore, jwtValidator)
	mailClient := mailer.NewClient(mailer.LoadConfigFromEnv())
	identSvc.SetMailer(mailClient)
	identSvc.SetRequireVerification(true)
	identHandler := identity.NewHandler(identSvc, gatewayhttp.WriteErrorResponse)

	var grpcClient ardav1.AgentRegistryServiceClient
	if regClient != nil {
		grpcClient = regClient.Client()
	}

	var grpcReviewClient ardav1.ReviewServiceClient
	if revClient != nil {
		grpcReviewClient = revClient.Client()
	}

	// Initialize Execution Service client if upstream address is set
	var execClient proxy.ExecutionClient
	if cfg.ExecutionServiceAddr != "" {
		ec, err := proxy.NewExecutionClient(cfg.ExecutionServiceAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize execution client: %w", err)
		}
		execClient = ec
	}

	// Initialize Tool Registry and Governance Service
	var toolStore toolgateway.Store
	if db != nil {
		if gs, err := toolgateway.NewGormStore(db); err == nil {
			toolStore = gs
		} else {
			toolStore = toolgateway.NewMemoryStore()
		}
	} else {
		toolStore = toolgateway.NewMemoryStore()
	}
	toolRegistry := toolgateway.NewRegistry(toolStore)
	toolsHandler := gatewayhttp.NewToolsHandler(toolRegistry, toolStore, gatewayhttp.WriteErrorResponse)

	// Initialize AgentRuntime for Realtime Synchronous Execution
	var agentRuntimeInstance agentruntime.AgentRuntime
	if regClient != nil && secretStore != nil {
		providerRegistry := agentruntime.NewProviderRegistry()
		_ = providerRegistry.RegisterWithAliases(agentruntime.NewOpenAIAdapter(), "gpt")
		_ = providerRegistry.RegisterWithAliases(agentruntime.NewAnthropicAdapter(), "claude")
		_ = providerRegistry.RegisterWithAliases(agentruntime.NewGeminiAdapter(), "gemini")
		_ = providerRegistry.RegisterWithAliases(agentruntime.NewGroqAdapter(), "groqcloud")
		_ = providerRegistry.RegisterWithAliases(agentruntime.NewGrokAdapter(), "xai")
		_ = providerRegistry.Register(agentruntime.NewOpenRouterAdapter())
		_ = providerRegistry.Register(agentruntime.NewMockLLMClient())

		credResolver, err := agentruntime.NewVaultCredentialResolver(secretStore)
		if err == nil {
			resolvingClient, err := agentruntime.NewResolvingLLMClient(
				providerRegistry,
				credResolver,
				agentruntime.WithDefaultProvider("openai"),
				agentruntime.WithResolvingDefaultModel("gpt-4o"),
			)
			if err == nil {
				configProvider, _ := agentruntime.NewRegistryConfigProvider(regClient.Client())

				var mcpCredResolver mcp.CredentialResolver
				if secretStore != nil {
					mcpCredResolver, _ = mcp.NewVaultCredentialResolver(secretStore)
				}
				mcpManager := mcp.NewConnectionManager(mcp.ManagerConfig{
					AllowLocalhost:     true,
					CredentialResolver: mcpCredResolver,
				})
				sim := simulator.NewTransactionSimulator(uuid.Nil)
				_ = mcpManager.RegisterInProcessServer("mcp-tx-conn", uuid.Nil, sim.Server())

				// Register Obsidian MCP connection for realtime execution (mirrors worker setup)
				obsidianEndpoint := os.Getenv("OBSIDIAN_MCP_ENDPOINT")
				if obsidianEndpoint == "" {
					obsidianEndpoint = "https://127.0.0.1:27124/mcp"
				}
				obsidianConn := mcp.ConnectionDefinition{
					ID:            "obsidian-mcp",
					TenantID:      uuid.Nil,
					ServerName:    "obsidian-mcp",
					Endpoint:      obsidianEndpoint,
					Transport:     mcp.TransportStreamableHTTP,
					CredentialRef: "secret:credentials/mcp/obsidian-mcp#access_token",
				}
				if conn, err := mcpManager.RegisterConnection(context.Background(), obsidianConn); err != nil {
					log.Printf("WARN: Failed to register global obsidian-mcp connection in gateway: %v", err)
				} else {
					log.Printf("Gateway registered global MCP connection: %s (%s)", conn.ID, conn.Endpoint)
				}
				if db != nil {
					var tenantIDs []uuid.UUID
					if err := db.Table("tenants").Pluck("id", &tenantIDs).Error; err == nil {
						for _, tid := range tenantIDs {
							tConn := obsidianConn
							tConn.TenantID = tid
							_, _ = mcpManager.RegisterConnection(context.Background(), tConn)
						}
					}
				}

				handlers := toolgateway.NewHandlerRegistry()
				handlers.Register("mcp:mcp-tx-conn:get_transaction", mcp.NewToolHandler(mcpManager, "mcp-tx-conn", "get_transaction"))
				handlers.Register("mcp:mcp-tx-conn:reverse_transaction", mcp.NewToolHandler(mcpManager, "mcp-tx-conn", "reverse_transaction"))
				handlers.SetDynamicFallback(func(id string) toolgateway.ToolHandler {
					if strings.HasPrefix(id, "mcp:") {
						connID, toolName, ok := mcp.ParseHandlerID(id)
						if ok {
							return mcp.NewToolHandler(mcpManager, connID, toolName)
						}
					}
					return nil
				})

				guardrail := toolgateway.NewCompositeGuardrail(
					toolgateway.NewDestructiveOperationGuardrail(),
					toolgateway.NewParameterBoundsGuardrail(5000.0),
				)

				toolGw := toolgateway.NewGateway(toolgateway.GatewayConfig{
					Registry:        toolRegistry,
					Store:           toolStore,
					Guardrail:       guardrail,
					HandlerRegistry: handlers,
					AuditLogger:     toolgateway.NewStoreAuditLogger(toolStore),
					ConfigProvider:  configProvider,
				})

				execOpts := []agentruntime.LLMExecutorOption{
					agentruntime.WithDefaultModel("gpt-4o"),
					agentruntime.WithDefaultExecutorProvider("openai"),
					agentruntime.WithToolGateway(toolGw),
					agentruntime.WithCalibrationInterval(10),
				}
				if relSvc != nil {
					execOpts = append(execOpts, agentruntime.WithReliabilityService(proxy.NewRuntimeAdapter(relSvc)))
				}

				executor, err := agentruntime.NewLLMExecutor(
					resolvingClient,
					configProvider,
					execOpts...,
				)
				if err == nil {
					if rt, err := agentruntime.New(executor); err == nil {
						agentRuntimeInstance = rt
					}
				}
			}
		}
	}

	gwHandler := gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
		JWTValidator:       jwtValidator,
		RateLimiter:        limiter,
		RegistryClient:     grpcClient,
		RegistryService:    regServiceInstance,
		ReliabilityService: relSvc,
		ExecutionClient:    execClient,
		TemporalClient:     tempClient,
		CredentialHandler:  credHandler,
		IdentityHandler:    identHandler,
		IdentityService:    identSvc,
		ToolsHandler:       toolsHandler,
		AgentRuntime:       agentRuntimeInstance,
		ReviewClient:       grpcReviewClient,
	})

	var outboxCancel context.CancelFunc
	if outboxRelay != nil {
		relayCtx, cancel := context.WithCancel(context.Background())
		outboxCancel = cancel
		go outboxRelay.Start(relayCtx)
	}

	return &Server{
		Handler:             gwHandler,
		JWTValidator:        jwtValidator,
		RateLimiter:         limiter,
		RegistryClient:      regClient,
		ReviewClient:        revClient,
		ReliabilityService:  relSvc,
		ExecutionClient:     execClient,
		TemporalClient:      tempClient,
		CredentialService:   credSvc,
		IdentityService:     identSvc,
		inProcessGRPC:       inProcessGRPC,
		inProcessReviewGRPC: inProcessReviewGRPC,
		OutboxRelay:         outboxRelay,
		outboxCancel:        outboxCancel,
	}, nil
}

// Close closes all client connections and cleans up background resources.
func (s *Server) Close() error {
	if s.outboxCancel != nil {
		s.outboxCancel()
	}
	if s.RegistryClient != nil {
		_ = s.RegistryClient.Close()
	}
	if s.ReviewClient != nil {
		_ = s.ReviewClient.Close()
	}
	if s.ReliabilityService != nil {
		_ = s.ReliabilityService.Close()
	}
	if s.ExecutionClient != nil {
		_ = s.ExecutionClient.Close()
	}
	if s.TemporalClient != nil {
		s.TemporalClient.Close()
	}
	if s.inProcessGRPC != nil {
		s.inProcessGRPC.GracefulStop()
	}
	if s.inProcessReviewGRPC != nil {
		s.inProcessReviewGRPC.GracefulStop()
	}
	return nil
}
