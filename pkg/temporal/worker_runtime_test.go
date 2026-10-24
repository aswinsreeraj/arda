package temporal_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
	"go.temporal.io/sdk/testsuite"
)

func TestWorker_RealRuntimeWiring(t *testing.T) {
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	// 1. Setup mock config provider and mock LLM client
	configProvider := agentruntime.NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, domain.Configuration{
		PromptTemplate:       "Summarize: {{input.query}}",
		ExecutionMode:        domain.ExecutionModeAutonomous,
		ReliabilityThreshold: 0.85,
	})

	mockLLM := agentruntime.NewMockLLMClient()
	mockLLM.Response = agentruntime.LLMResponse{
		Content: "Market trends are positive with 15% growth.",
		Model:   "gpt-4o",
	}

	// 2. Construct LLMExecutor explicitly
	executor, err := agentruntime.NewLLMExecutor(mockLLM, configProvider)
	if err != nil {
		t.Fatalf("unexpected error constructing LLMExecutor: %v", err)
	}

	// 3. Construct Runtime explicitly
	rt, err := agentruntime.New(executor)
	if err != nil {
		t.Fatalf("unexpected error constructing Runtime: %v", err)
	}

	// 4. Construct activities with injected real runtime
	activities := temporal.NewAgentActivities(rt)

	// 5. Test activity execution directly
	actCtx := context.Background()
	actInput := temporal.AgentExecutionActivityInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionInput: map[string]any{
			"query": "Q3 performance",
		},
	}

	res, err := activities.AgentExecutionActivity(actCtx, actInput)
	if err != nil {
		t.Fatalf("unexpected error executing activity: %v", err)
	}

	if res.Status != "SUCCESS" {
		t.Errorf("expected activity status SUCCESS, got %s", res.Status)
	}
	if res.Output["content"] != "Market trends are positive with 15% growth." {
		t.Errorf("expected content from LLM, got %v", res.Output["content"])
	}

	// Verify exact pinned IDs were queried in config provider
	lastTenant, lastAgent, lastVer := configProvider.LastQuery()
	if lastTenant != tenantID {
		t.Errorf("expected config query tenant %s, got %s", tenantID, lastTenant)
	}
	if lastAgent != agentID {
		t.Errorf("expected config query agent %s, got %s", agentID, lastAgent)
	}
	if lastVer != versionID {
		t.Errorf("expected config query version %s, got %s", versionID, lastVer)
	}

	// 6. Test full workflow execution using Temporal TestSuite
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(temporal.AgentExecutionWorkflow)
	env.RegisterActivity(activities.AgentExecutionActivity)

	wfInput := temporal.AgentExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionInput: map[string]any{
			"query": "Q3 performance",
		},
	}

	env.ExecuteWorkflow(temporal.AgentExecutionWorkflow, wfInput)

	if !env.IsWorkflowCompleted() {
		t.Fatalf("expected workflow to complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	var wfResult temporal.AgentExecutionResult
	if err := env.GetWorkflowResult(&wfResult); err != nil {
		t.Fatalf("failed to decode workflow result: %v", err)
	}

	if wfResult.Status != "SUCCESS" {
		t.Errorf("expected workflow result status SUCCESS, got %s", wfResult.Status)
	}
	if wfResult.TenantID != tenantID || wfResult.AgentID != agentID || wfResult.AgentVersionID != versionID {
		t.Errorf("workflow result failed to preserve exact identifiers: %+v", wfResult)
	}
}

func TestWorker_MissingConfigurationFailsSafely(t *testing.T) {
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	// Empty config provider without registered config
	configProvider := agentruntime.NewMockConfigProvider()
	mockLLM := agentruntime.NewMockLLMClient()

	executor, err := agentruntime.NewLLMExecutor(mockLLM, configProvider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rt, err := agentruntime.New(executor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	activities := temporal.NewAgentActivities(rt)
	actInput := temporal.AgentExecutionActivityInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
	}

	_, err = activities.AgentExecutionActivity(context.Background(), actInput)
	if err == nil {
		t.Fatalf("expected activity error for missing config, got nil")
	}
}

func TestWorker_DependencyValidation(t *testing.T) {
	configProvider := agentruntime.NewMockConfigProvider()
	mockLLM := agentruntime.NewMockLLMClient()

	// Nil LLM client rejected
	_, err := agentruntime.NewLLMExecutor(nil, configProvider)
	if !errors.Is(err, agentruntime.ErrNilLLMClient) {
		t.Errorf("expected ErrNilLLMClient, got %v", err)
	}

	// Nil config provider rejected
	_, err = agentruntime.NewLLMExecutor(mockLLM, nil)
	if !errors.Is(err, agentruntime.ErrNilConfigProvider) {
		t.Errorf("expected ErrNilConfigProvider, got %v", err)
	}

	// Nil executor rejected in Runtime constructor
	_, err = agentruntime.New(nil)
	if !errors.Is(err, agentruntime.ErrNilExecutor) {
		t.Errorf("expected ErrNilExecutor, got %v", err)
	}
}
