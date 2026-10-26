package gateway

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"gorm.io/gorm"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/aswinsreeraj/arda/services/gateway/credentials"
	gatewayhttp "github.com/aswinsreeraj/arda/services/gateway/http"
	"github.com/aswinsreeraj/arda/services/gateway/identity"
	"github.com/aswinsreeraj/arda/services/gateway/proxy"
	"github.com/aswinsreeraj/arda/services/gateway/ratelimit"
	"github.com/aswinsreeraj/arda/pkg/toolgateway"
	registrygrpc "github.com/aswinsreeraj/arda/services/registry/grpc"
	"github.com/aswinsreeraj/arda/services/registry/service"
	"github.com/aswinsreeraj/arda/services/registry/store"
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
	VaultAddress           string
	VaultToken             string
	VaultMount             string
}

// Server encapsulates the API Gateway HTTP server and dependencies.
type Server struct {
	Handler            http.Handler
	JWTValidator       *auth.TokenValidator
	RateLimiter        *ratelimit.Limiter
	RegistryClient     *proxy.RegistryClient
	ReliabilityService proxy.ReliabilityService
	ExecutionClient    proxy.ExecutionClient
	TemporalClient     *temporal.Client
	CredentialService  *credentials.Service
	IdentityService    *identity.Service
	inProcessGRPC      *registrygrpc.Server
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
	var inProcessGRPC *registrygrpc.Server
	if cfg.RegistryGRPCAddr != "" {
		rc, err := proxy.NewRegistryClient(cfg.RegistryGRPCAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to registry grpc service: %w", err)
		}
		regClient = rc
	} else if db != nil {
		// Spawn in-process gRPC server over bufconn so Gateway talks to Registry exclusively via gRPC
		regStore := store.NewPostgresStore(db)
		regService := service.NewRegistryService(regStore)
		inProcessGRPC = registrygrpc.NewServer(regService)
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
	identHandler := identity.NewHandler(identSvc, gatewayhttp.WriteErrorResponse)

	var grpcClient ardav1.AgentRegistryServiceClient
	if regClient != nil {
		grpcClient = regClient.Client()
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

	gwHandler := gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
		JWTValidator:       jwtValidator,
		RateLimiter:        limiter,
		RegistryClient:     grpcClient,
		ReliabilityService: relSvc,
		ExecutionClient:    execClient,
		TemporalClient:     tempClient,
		CredentialHandler:  credHandler,
		IdentityHandler:    identHandler,
		ToolsHandler:       toolsHandler,
	})

	return &Server{
		Handler:            gwHandler,
		JWTValidator:       jwtValidator,
		RateLimiter:        limiter,
		RegistryClient:     regClient,
		ReliabilityService: relSvc,
		ExecutionClient:    execClient,
		TemporalClient:     tempClient,
		CredentialService:  credSvc,
		IdentityService:    identSvc,
		inProcessGRPC:      inProcessGRPC,
	}, nil
}

// Close closes all client connections and cleans up background resources.
func (s *Server) Close() error {
	if s.RegistryClient != nil {
		_ = s.RegistryClient.Close()
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
	return nil
}
