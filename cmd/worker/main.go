package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/aswinsreeraj/arda/services/gateway/proxy"
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

	// 1. Connect to Agent Registry gRPC service for configuration retrieval
	regProxyClient, err := proxy.NewRegistryClient(*registryAddrFlag)
	if err != nil {
		log.Fatalf("FATAL: Failed to connect to Agent Registry gRPC at %s: %v", *registryAddrFlag, err)
	}
	defer regProxyClient.Close()

	// 2. Initialize Registry configuration provider for exact pinned version retrieval
	configProvider, err := agentruntime.NewRegistryConfigProvider(regProxyClient.Client())
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize registry configuration provider: %v", err)
	}

	// 3. Initialize Provider Registry and register available adapters
	providerRegistry := agentruntime.NewProviderRegistry()
	openAIAdapter := agentruntime.NewOpenAIAdapter()
	if err := providerRegistry.Register(openAIAdapter); err != nil {
		log.Fatalf("FATAL: Failed to register OpenAI provider adapter: %v", err)
	}

	allowMockLLM := *devMockLLMFlag || strings.EqualFold(os.Getenv("ARDA_DEV_MOCK_LLM"), "true")
	if allowMockLLM {
		log.Println("WARNING: ARDA_DEV_MOCK_LLM enabled. Registering MockLLMClient for local/test execution.")
		mockClient := agentruntime.NewMockLLMClient()
		if err := providerRegistry.Register(mockClient); err != nil {
			log.Fatalf("FATAL: Failed to register mock provider adapter: %v", err)
		}
	}

	// 4. Initialize Vault client and Tenant Credential Resolver for BYOK
	vaultCfg := vault.ConfigFromEnv()
	var credResolver agentruntime.CredentialResolver

	if vaultCfg.Address != "" && vaultCfg.Token != "" {
		vaultClient, err := vault.NewClient(vaultCfg)
		if err != nil {
			log.Fatalf("FATAL: Failed to initialize Vault client: %v", err)
		}
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

	// 5. Initialize ResolvingLLMClient with dynamic provider routing and BYOK credentials
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

	// 6. Construct LLMExecutor and wrap with AgentRuntime
	executor, err := agentruntime.NewLLMExecutor(
		resolvingClient,
		configProvider,
		agentruntime.WithDefaultModel("gpt-4o"),
		agentruntime.WithDefaultExecutorProvider(defaultProvider),
	)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize LLM executor: %v", err)
	}

	rt, err := agentruntime.New(executor)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize agent runtime: %v", err)
	}

	// 5. Initialize Temporal Worker with real AgentRuntime
	w, err := temporal.NewWorker(cfg, nil, temporal.WithAgentRuntime(rt))
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
