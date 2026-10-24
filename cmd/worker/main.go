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

	// 3. Resolve LLMClient dependency (fail fast in production if unconfigured)
	var llmClient agentruntime.LLMClient
	allowMockLLM := *devMockLLMFlag || strings.EqualFold(os.Getenv("ARDA_DEV_MOCK_LLM"), "true")

	if allowMockLLM {
		log.Println("WARNING: ARDA_DEV_MOCK_LLM enabled. Using deterministic MockLLMClient for local/test execution.")
		llmClient = agentruntime.NewMockLLMClient()
	} else {
		log.Fatal("FATAL: Production LLM client is not configured and real provider integration is not yet implemented. Set ARDA_DEV_MOCK_LLM=true for local testing or inject a production LLMClient. Refusing to silently wire MockLLMClient in production.")
	}

	// 4. Construct LLMExecutor and wrap with AgentRuntime
	executor, err := agentruntime.NewLLMExecutor(llmClient, configProvider)
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
