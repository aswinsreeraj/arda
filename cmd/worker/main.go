package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/database"
	"github.com/aswinsreeraj/arda/pkg/mcp"
	"github.com/aswinsreeraj/arda/pkg/mcp/simulator"
	"github.com/aswinsreeraj/arda/pkg/platformconfig"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/pkg/toolgateway"
	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/aswinsreeraj/arda/services/gateway/proxy"
	reviewgrpc "github.com/aswinsreeraj/arda/services/review/grpc"
	reviewservice "github.com/aswinsreeraj/arda/services/review/service"
	reviewstore "github.com/aswinsreeraj/arda/services/review/store"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func loadEnv(filepath string) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.TrimSpace(parts[1])
			if os.Getenv(k) == "" {
				_ = os.Setenv(k, v)
			}
		}
	}
}

func main() {
	loadEnv(".env")
	envCfg := temporal.ConfigFromEnv()

	registryDefaultAddr := os.Getenv("REGISTRY_GRPC_ADDR")
	if registryDefaultAddr == "" {
		registryDefaultAddr = "127.0.0.1:50051"
	}

	addressFlag := flag.String("address", envCfg.Address, "Temporal server host:port")
	namespaceFlag := flag.String("namespace", envCfg.Namespace, "Temporal namespace")
	taskQueueFlag := flag.String("task-queue", envCfg.TaskQueue, "Temporal task queue name")
	registryAddrFlag := flag.String("registry-address", registryDefaultAddr, "Agent Registry gRPC server host:port")
	devMockLLMFlag := flag.Bool("dev-mock-llm", false, "Enable deterministic MockLLMClient for local testing/development only")
	flag.Parse()

	cfg := temporal.Config{
		Address:   *addressFlag,
		Namespace: *namespaceFlag,
		TaskQueue: *taskQueueFlag,
	}

	log.Printf("Starting ARDA Temporal Worker...")
	log.Printf("  Temporal Address: %s", cfg.Address)
	log.Printf("  Namespace:        %s", cfg.Namespace)
	log.Printf("  Task Queue:       %s", cfg.TaskQueue)
	log.Printf("  Registry Address: %s", *registryAddrFlag)

	// Agent Registry gRPC connection
	regProxyClient, err := proxy.NewRegistryClient(*registryAddrFlag)
	if err != nil {
		log.Fatalf("FATAL: Failed to connect to Agent Registry gRPC at %s: %v", *registryAddrFlag, err)
	}
	defer regProxyClient.Close()

	configProvider, err := agentruntime.NewRegistryConfigProvider(regProxyClient.Client())
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize registry configuration provider: %v", err)
	}

	// Supported LLM provider adapters
	providerRegistry := agentruntime.NewProviderRegistry()
	if err := providerRegistry.RegisterWithAliases(agentruntime.NewOpenAIAdapter(), "gpt"); err != nil {
		log.Fatalf("FATAL: Failed to register OpenAI provider adapter: %v", err)
	}
	if err := providerRegistry.RegisterWithAliases(agentruntime.NewAnthropicAdapter(), "claude"); err != nil {
		log.Fatalf("FATAL: Failed to register Anthropic provider adapter: %v", err)
	}
	if err := providerRegistry.RegisterWithAliases(agentruntime.NewGeminiAdapter(), "gemini"); err != nil {
		log.Fatalf("FATAL: Failed to register Google Gemini provider adapter: %v", err)
	}
	if err := providerRegistry.RegisterWithAliases(agentruntime.NewGroqAdapter(), "groqcloud"); err != nil {
		log.Fatalf("FATAL: Failed to register Groq provider adapter: %v", err)
	}
	if err := providerRegistry.RegisterWithAliases(agentruntime.NewGrokAdapter(), "xai"); err != nil {
		log.Fatalf("FATAL: Failed to register Grok provider adapter: %v", err)
	}
	if err := providerRegistry.Register(agentruntime.NewOpenRouterAdapter()); err != nil {
		log.Fatalf("FATAL: Failed to register OpenRouter provider adapter: %v", err)
	}

	allowMockLLM := *devMockLLMFlag || strings.EqualFold(os.Getenv("ARDA_DEV_MOCK_LLM"), "true")
	if allowMockLLM {
		log.Println("WARNING: ARDA_DEV_MOCK_LLM enabled. Registering MockLLMClient for local/test execution.")
		mockClient := agentruntime.NewMockLLMClient()
		if err := providerRegistry.Register(mockClient); err != nil {
			log.Fatalf("FATAL: Failed to register mock provider adapter: %v", err)
		}
	}

	// Tenant BYOK credential resolution via HashiCorp Vault
	vaultCfg := vault.ConfigFromEnv()
	var credResolver agentruntime.CredentialResolver
	var vaultClient vault.SecretStore

	if vaultCfg.Address != "" && vaultCfg.Token != "" {
		vc, err := vault.NewClient(vaultCfg)
		if err != nil {
			log.Fatalf("FATAL: Failed to initialize Vault client: %v", err)
		}
		vaultClient = vc
		vaultResolver, err := agentruntime.NewVaultCredentialResolver(vaultClient)
		if err != nil {
			log.Fatalf("FATAL: Failed to initialize Vault credential resolver: %v", err)
		}
		credResolver = vaultResolver
		log.Printf("  Vault Address:    %s (mount: %s)", vaultCfg.Address, vaultCfg.MountPath)
	} else if allowMockLLM {
		log.Println("WARNING: Vault not configured; using MockCredentialResolver in local dev mode.")
		credResolver = agentruntime.NewMockCredentialResolver()
	} else {
		log.Fatal("FATAL: Vault configuration (VAULT_ADDR and VAULT_TOKEN) is required for production BYOK credential resolution. Refusing to run in production without secret storage. Set ARDA_DEV_MOCK_LLM=true for local mock testing.")
	}

	defaultProvider := "openai"
	if allowMockLLM && (vaultCfg.Address == "" || vaultCfg.Token == "") {
		defaultProvider = "mock"
	}
	resolvingClient, err := agentruntime.NewResolvingLLMClient(
		providerRegistry,
		credResolver,
		agentruntime.WithDefaultProvider(defaultProvider),
		agentruntime.WithResolvingDefaultModel("gpt-4o"),
	)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize dynamic resolving LLM client: %v", err)
	}

	// Initialize Database Connection
	db, err := database.Connect(database.LoadConfigFromEnv())
	if err != nil {
		log.Printf("WARN: Database connection failed: %v", err)
	} else {
		defer db.Close()
	}

	// Initialize Tool Registry & Audit Store for Tool Gateway
	var toolStore toolgateway.Store
	if db != nil {
		if gs, err := toolgateway.NewGormStore(db.DB); err == nil {
			toolStore = gs
		} else {
			toolStore = toolgateway.NewMemoryStore()
		}
	} else {
		toolStore = toolgateway.NewMemoryStore()
	}
	toolRegistry := toolgateway.NewRegistry(toolStore)

	// Initialize MCP Connection Manager with local simulator fallback and Vault credential resolver
	var mcpCredResolver mcp.CredentialResolver
	if vaultClient != nil {
		mcpCredResolver, _ = mcp.NewVaultCredentialResolver(vaultClient)
	}
	mcpManager := mcp.NewConnectionManager(mcp.ManagerConfig{
		AllowLocalhost:     true,
		CredentialResolver: mcpCredResolver,
	})
	sim := simulator.NewTransactionSimulator(uuid.Nil)
	_ = mcpManager.RegisterInProcessServer("mcp-tx-conn", uuid.Nil, sim.Server())

	// Register Obsidian MCP connection definition
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
		log.Printf("ERROR: Failed to register global obsidian-mcp connection: %v", err)
	} else {
		log.Printf("Successfully registered global MCP connection: %s", conn.ID)
	}

	// Register Obsidian connection for all active tenants in DB
	if db != nil {
		var tenantIDs []uuid.UUID
		if err := db.DB.Table("tenants").Pluck("id", &tenantIDs).Error; err == nil {
			for _, tid := range tenantIDs {
				tConn := obsidianConn
				tConn.TenantID = tid
				if _, err := mcpManager.RegisterConnection(context.Background(), tConn); err != nil {
					log.Printf("WARN: Failed to register obsidian-mcp connection for tenant %s: %v", tid, err)
				} else {
					log.Printf("Successfully registered MCP connection: %s for tenant %s", tConn.ID, tid)
				}
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
		agentruntime.WithDefaultExecutorProvider(defaultProvider),
		agentruntime.WithToolGateway(toolGw),
		agentruntime.WithCalibrationInterval(10), // Fallback if platform config store unavailable
	}
	if db != nil {
		if platformCfgStore, err := platformconfig.NewPostgresStore(db.DB); err == nil {
			execOpts = append(execOpts,
				agentruntime.WithCalibrationIntervalProvider(func(ctx context.Context) int {
					return platformCfgStore.GetSnapshot(ctx).CalibrationSamplingInterval
				}),
				agentruntime.WithDefaultThresholdProvider(func(ctx context.Context) float64 {
					return platformCfgStore.GetSnapshot(ctx).DefaultReliabilityThreshold
				}),
				agentruntime.WithRetrievalTopKProvider(func(ctx context.Context) int {
					return platformCfgStore.GetSnapshot(ctx).RetrievalTopK
				}),
			)
			log.Printf("  Platform Config:  live PostgreSQL-backed settings enabled")
		} else {
			log.Printf("WARN: Failed to initialize platform config store in worker: %v", err)
		}
	}
	relAddr := os.Getenv("RELIABILITY_SERVICE_ADDR")
	if relAddr != "" {
		if relClient, err := proxy.NewReliabilityClient(relAddr); err == nil {
			execOpts = append(execOpts, agentruntime.WithReliabilityService(proxy.NewRuntimeAdapter(relClient)))
			log.Printf("Worker connected to Reliability Service at %s", relAddr)
		} else {
			log.Printf("WARN: Failed to connect to Reliability Service at %s: %v", relAddr, err)
		}
	}

	executor, err := agentruntime.NewLLMExecutor(
		resolvingClient,
		configProvider,
		execOpts...,
	)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize LLM executor: %v", err)
	}

	rt, err := agentruntime.New(executor)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize agent runtime: %v", err)
	}

	// Initialize ReviewServiceClient so escalated executions create review cases
	var reviewClient ardav1.ReviewServiceClient
	if db != nil {
		revStore := reviewstore.NewPostgresStore(db.DB)
		var signaler reviewservice.TemporalSignaler
		if tempClient, err := temporal.NewClient(cfg); err == nil {
			signaler = tempClient.RawClient()
		}
		revSvc := reviewservice.NewService(
			revStore,
			reviewservice.WithTemporalSignaler(signaler),
		)
		reviewGRPCServer := reviewgrpc.NewServer(revSvc)
		lis := bufconn.Listen(1024 * 1024)
		go func() {
			_ = reviewGRPCServer.Serve(lis)
		}()
		conn, err := grpc.NewClient("passthrough://bufnet-worker-review",
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return lis.Dial()
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err == nil {
			reviewClient = ardav1.NewReviewServiceClient(conn)
			log.Printf("  Review Service:   in-process connected (bufconn)")
		}
	}

	workerOpts := []temporal.WorkerOption{
		temporal.WithAgentRuntime(rt),
	}
	if reviewClient != nil {
		workerOpts = append(workerOpts, temporal.WithReviewClient(reviewClient))
	}

	w, err := temporal.NewWorker(cfg, nil, workerOpts...)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize Temporal worker: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("ARDA Temporal Worker listening on task queue %q. Press Ctrl+C to stop.", cfg.TaskQueue)
		if err := w.Start(); err != nil {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Fatalf("Worker start error: %v", err)
	case sig := <-sigCh:
		log.Printf("Received shutdown signal (%v). Gracefully stopping worker...", sig)
		w.Stop()
		log.Printf("ARDA Temporal Worker stopped cleanly.")
	}
}
