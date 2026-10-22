package temporal_test

import (
	"context"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
)

func TestWorker_ConstructorDefaults(t *testing.T) {
	cfg := temporal.Config{
		Address:   "127.0.0.1:59999",
		Namespace: "default",
		TaskQueue: "test-task-queue",
	}

	// Dialing an offline address creates a valid client handle without blocking in Temporal SDK
	c, err := client.Dial(client.Options{
		HostPort:  cfg.Address,
		Namespace: cfg.Namespace,
	})
	if err != nil {
		t.Skipf("skipping worker constructor test due to client dial failure: %v", err)
	}
	defer c.Close()

	// 1. Verify NewWorker with defaults creates a non-nil deterministic mock runtime
	w, err := temporal.NewWorker(cfg, c)
	if err != nil {
		t.Fatalf("failed to construct worker with default options: %v", err)
	}
	defer w.Stop()

	if w.Runtime() == nil {
		t.Errorf("expected worker to have an initialized AgentRuntime, got nil")
	}

	if w.Config().TaskQueue != "test-task-queue" {
		t.Errorf("expected task queue test-task-queue, got %s", w.Config().TaskQueue)
	}
}

func TestWorker_ConstructorWithCustomRuntime(t *testing.T) {
	cfg := temporal.Config{
		Address:   "127.0.0.1:59999",
		Namespace: "default",
		TaskQueue: "test-custom-queue",
	}

	c, err := client.Dial(client.Options{
		HostPort:  cfg.Address,
		Namespace: cfg.Namespace,
	})
	if err != nil {
		t.Skipf("skipping worker constructor test due to client dial failure: %v", err)
	}
	defer c.Close()

	customMock := agentruntime.NewMockExecutor()
	customMock.Result = agentruntime.ExecutionResult{
		Status: agentruntime.StatusSuccess,
		Output: map[string]any{"source": "custom-runtime"},
	}
	customRuntime, err := agentruntime.New(customMock)
	if err != nil {
		t.Fatalf("failed to create custom runtime: %v", err)
	}

	// 2. Test NewWorker with WithAgentRuntime option
	w, err := temporal.NewWorker(cfg, c, temporal.WithAgentRuntime(customRuntime))
	if err != nil {
		t.Fatalf("failed to construct worker with custom runtime option: %v", err)
	}
	defer w.Stop()

	if w.Runtime() != customRuntime {
		t.Errorf("expected worker runtime to match injected custom runtime")
	}

	// 3. Test NewWorkerWithRuntime convenience constructor
	w2, err := temporal.NewWorkerWithRuntime(cfg, c, customRuntime)
	if err != nil {
		t.Fatalf("failed to construct worker with NewWorkerWithRuntime: %v", err)
	}
	defer w2.Stop()

	if w2.Runtime() != customRuntime {
		t.Errorf("expected worker runtime from NewWorkerWithRuntime to match custom runtime")
	}
}

func TestWorker_DirectRuntimeExecution(t *testing.T) {
	cfg := temporal.Config{
		Address:   "127.0.0.1:59999",
		Namespace: "default",
		TaskQueue: "test-queue",
	}

	c, err := client.Dial(client.Options{
		HostPort:  cfg.Address,
		Namespace: cfg.Namespace,
	})
	if err != nil {
		t.Skipf("skipping due to dial failure: %v", err)
	}
	defer c.Close()

	w, err := temporal.NewWorker(cfg, c)
	if err != nil {
		t.Fatalf("failed to initialize worker: %v", err)
	}
	defer w.Stop()

	// Verify the worker's runtime is fully functional directly (realtime compatibility)
	rt := w.Runtime()
	if rt == nil {
		t.Fatalf("worker runtime is nil")
	}

	res, err := rt.Run(context.Background(), agentruntime.ExecutionInput{
		// Missing TenantID triggers validation failure confirming execution through runtime.Run
		TenantID: uuid.Nil,
	})
	// This will fail validation because tenant_id is nil, which confirms it executed through runtime.Run!
	if err == nil {
		t.Fatalf("expected validation error from empty input, got nil")
	}
	_ = res
}
