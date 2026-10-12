package temporal_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/temporal"
	"go.temporal.io/sdk/testsuite"
)

// Unit Test Note:
// The tests in this file execute in-memory using the Temporal Go SDK Test Environment
// (go.temporal.io/sdk/testsuite). They do not require a live Temporal Server or Docker container,
// ensuring fast, deterministic, reproducible unit testing in CI.

func TestNoopWorkflow_Success(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(temporal.NoopWorkflow)
	env.RegisterActivity(temporal.NoopActivity)

	input := temporal.NoopWorkflowInput{
		Message: "verification payload",
	}

	env.ExecuteWorkflow(temporal.NoopWorkflow, input)

	if !env.IsWorkflowCompleted() {
		t.Fatalf("expected workflow to complete")
	}

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	var result temporal.NoopWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("failed to decode workflow result: %v", err)
	}

	if result.ActivityResult.Status != "SUCCESS" {
		t.Fatalf("expected status SUCCESS, got: %s", result.ActivityResult.Status)
	}
	if !strings.Contains(result.ActivityResult.Message, "verification payload") {
		t.Fatalf("expected result message to contain input payload, got: %s", result.ActivityResult.Message)
	}
	if result.ActivityResult.ExecutedBy != "NoopActivity" {
		t.Fatalf("expected ExecutedBy NoopActivity, got: %s", result.ActivityResult.ExecutedBy)
	}
}

func TestNoopWorkflow_ActivityFailurePropagation(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(temporal.NoopWorkflow)
	env.RegisterActivity(temporal.NoopActivity)

	// "FAIL_SIMULATION" triggers an application error in NoopActivity
	input := temporal.NoopWorkflowInput{
		Message: "FAIL_SIMULATION",
	}

	env.ExecuteWorkflow(temporal.NoopWorkflow, input)

	if !env.IsWorkflowCompleted() {
		t.Fatalf("expected workflow to complete (with error)")
	}

	err := env.GetWorkflowError()
	if err == nil {
		t.Fatalf("expected workflow to return an error when activity fails, but got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "simulated activity execution failure") && !strings.Contains(errMsg, "activity execution failed") {
		t.Fatalf("expected error message to contain simulated failure text, got: %s", errMsg)
	}
}

func TestNoopActivity_DirectExecution(t *testing.T) {
	ctx := context.Background()

	// 1. Success case
	res, err := temporal.NoopActivity(ctx, temporal.NoopActivityInput{
		Message: "direct test",
	})
	if err != nil {
		t.Fatalf("unexpected activity error: %v", err)
	}
	if res.Status != "SUCCESS" || !strings.Contains(res.Message, "direct test") {
		t.Fatalf("unexpected activity result: %+v", res)
	}

	// 2. Failure simulation case
	_, err = temporal.NoopActivity(ctx, temporal.NoopActivityInput{
		Message: "FAIL_SIMULATION",
	})
	if err == nil {
		t.Fatalf("expected error for FAIL_SIMULATION, got nil")
	}
}

func TestConfig_Defaults(t *testing.T) {
	cfg := temporal.ConfigFromEnv()
	if cfg.Address == "" {
		t.Fatalf("expected default address to be non-empty")
	}
	if cfg.Namespace == "" {
		t.Fatalf("expected default namespace to be non-empty")
	}
	if cfg.TaskQueue == "" {
		t.Fatalf("expected default task queue to be non-empty")
	}
}

func TestClient_UnavailableServerReportsError(t *testing.T) {
	// Attempt to start workflow on a non-listening port
	badCfg := temporal.Config{
		Address:   "127.0.0.1:59998",
		Namespace: "default",
		TaskQueue: "test-queue",
	}

	client, err := temporal.NewClient(badCfg)
	if err != nil {
		// client.Dial may fail immediately depending on network, which is also valid
		return
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200)
	defer cancel()

	_, err = client.ExecuteNoopWorkflow(ctx, "test-wf-id", temporal.NoopWorkflowInput{
		Message: "test",
	})
	if err == nil {
		t.Fatalf("expected error when temporal server is offline, got nil")
	}
}
