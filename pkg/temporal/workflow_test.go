package temporal_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/google/uuid"
	sdktemporal "go.temporal.io/sdk/temporal"
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

func TestAgentExecutionActivity_DirectExecution(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()

	var capturedInput agentruntime.ExecutionInput
	mock := &agentruntime.MockExecutor{
		ExecuteFunc: func(ctx context.Context, input agentruntime.ExecutionInput) (agentruntime.ExecutionResult, error) {
			capturedInput = input
			return agentruntime.ExecutionResult{
				Status: agentruntime.StatusSuccess,
				Output: map[string]any{"result": "direct execution successful"},
			}, nil
		},
	}

	rt, err := agentruntime.New(mock)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	activities := temporal.NewAgentActivities(rt)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()
	payload := map[string]any{"task": "analyze"}

	actInput := temporal.AgentExecutionActivityInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionInput: payload,
	}

	env.RegisterActivity(activities.AgentExecutionActivity)
	val, err := env.ExecuteActivity(activities.AgentExecutionActivity, actInput)
	if err != nil {
		t.Fatalf("activity execution failed: %v", err)
	}

	var actResult temporal.AgentExecutionActivityResult
	if err := val.Get(&actResult); err != nil {
		t.Fatalf("failed to decode activity result: %v", err)
	}

	if actResult.Status != "SUCCESS" {
		t.Errorf("expected status SUCCESS, got: %s", actResult.Status)
	}
	if actResult.Output["result"] != "direct execution successful" {
		t.Errorf("unexpected output: %+v", actResult.Output)
	}

	// Verify the activity forwarded identifiers and payload correctly
	if capturedInput.TenantID != tenantID {
		t.Errorf("expected tenant ID %s, got %s", tenantID, capturedInput.TenantID)
	}
	if capturedInput.AgentID != agentID {
		t.Errorf("expected agent ID %s, got %s", agentID, capturedInput.AgentID)
	}
	if capturedInput.AgentVersionID != versionID {
		t.Errorf("expected agent version ID %s, got %s", versionID, capturedInput.AgentVersionID)
	}
	if capturedInput.Payload["task"] != "analyze" {
		t.Errorf("expected payload task 'analyze', got %+v", capturedInput.Payload)
	}
}

type testLogger struct{}

func (testLogger) Debug(msg string, keyvals ...any) {}
func (testLogger) Info(msg string, keyvals ...any)  {}
func (testLogger) Warn(msg string, keyvals ...any)  {}
func (testLogger) Error(msg string, keyvals ...any) {}

func TestAgentExecutionActivity_ErrorPropagation(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}

	t.Run("runtime execution error produces retryable application error without leaking raw details", func(t *testing.T) {
		env := suite.NewTestActivityEnvironment()
		mock := &agentruntime.MockExecutor{
			Err: errors.New("upstream LLM failure with sensitive token: Bearer secret-token-123"),
		}
		rt, _ := agentruntime.New(mock)
		activities := temporal.NewAgentActivities(rt)
		env.RegisterActivity(activities.AgentExecutionActivity)

		input := temporal.AgentExecutionActivityInput{
			TenantID:       uuid.New(),
			AgentID:        uuid.New(),
			AgentVersionID: uuid.New(),
		}

		_, err := env.ExecuteActivity(activities.AgentExecutionActivity, input)
		if err == nil {
			t.Fatalf("expected error from activity, got nil")
		}

		var appErr *sdktemporal.ApplicationError
		if !errors.As(err, &appErr) {
			t.Fatalf("expected ApplicationError, got: %T (%v)", err, err)
		}
		if appErr.Type() != "AgentExecutionTransientFailure" {
			t.Errorf("expected error type AgentExecutionTransientFailure, got: %s", appErr.Type())
		}
		if appErr.NonRetryable() {
			t.Errorf("expected execution failure to be retryable")
		}
		// Verify raw error details are NOT exposed in public error message
		if strings.Contains(appErr.Error(), "secret-token-123") {
			t.Errorf("expected sensitive error details to be redacted from public error message, got: %s", appErr.Error())
		}
	})

	t.Run("permanent runtime error produces non-retryable application error", func(t *testing.T) {
		env := suite.NewTestActivityEnvironment()
		mock := &agentruntime.MockExecutor{
			Err: agentruntime.NewPermanentError("unauthorized model access", nil),
		}
		rt, _ := agentruntime.New(mock)
		activities := temporal.NewAgentActivities(rt)
		env.RegisterActivity(activities.AgentExecutionActivity)

		input := temporal.AgentExecutionActivityInput{
			TenantID:       uuid.New(),
			AgentID:        uuid.New(),
			AgentVersionID: uuid.New(),
		}

		_, err := env.ExecuteActivity(activities.AgentExecutionActivity, input)
		if err == nil {
			t.Fatalf("expected error from activity, got nil")
		}

		var appErr *sdktemporal.ApplicationError
		if !errors.As(err, &appErr) {
			t.Fatalf("expected ApplicationError, got: %T (%v)", err, err)
		}
		if appErr.Type() != "PermanentExecutionError" {
			t.Errorf("expected error type PermanentExecutionError, got: %s", appErr.Type())
		}
		if !appErr.NonRetryable() {
			t.Errorf("expected permanent error to be non-retryable")
		}
	})

	t.Run("invalid input produces non-retryable application error", func(t *testing.T) {
		env := suite.NewTestActivityEnvironment()
		rt := agentruntime.NewDefaultRuntime()
		activities := temporal.NewAgentActivities(rt)
		env.RegisterActivity(activities.AgentExecutionActivity)

		// Missing TenantID and AgentID triggers validation error
		input := temporal.AgentExecutionActivityInput{
			TenantID:       uuid.Nil,
			AgentID:        uuid.Nil,
			AgentVersionID: uuid.Nil,
		}

		_, err := env.ExecuteActivity(activities.AgentExecutionActivity, input)
		if err == nil {
			t.Fatalf("expected validation error from activity, got nil")
		}

		var appErr *sdktemporal.ApplicationError
		if !errors.As(err, &appErr) {
			t.Fatalf("expected ApplicationError, got: %T (%v)", err, err)
		}
		if appErr.Type() != "InvalidInputError" {
			t.Errorf("expected error type InvalidInputError, got: %s", appErr.Type())
		}
		if !appErr.NonRetryable() {
			t.Errorf("expected invalid input error to be non-retryable")
		}
	})

	t.Run("invalid runtime result produces non-retryable application error", func(t *testing.T) {
		env := suite.NewTestActivityEnvironment()
		mock := &agentruntime.MockExecutor{
			Result: agentruntime.ExecutionResult{
				Status: agentruntime.StatusEscalated, // missing Escalation info triggers validation error
			},
		}
		// Construct directly to test activity boundary validation
		activities := temporal.NewAgentActivities(mock)
		env.RegisterActivity(activities.AgentExecutionActivity)

		input := temporal.AgentExecutionActivityInput{
			TenantID:       uuid.New(),
			AgentID:        uuid.New(),
			AgentVersionID: uuid.New(),
		}

		_, err := env.ExecuteActivity(activities.AgentExecutionActivity, input)
		if err == nil {
			t.Fatalf("expected error for invalid execution result, got nil")
		}

		var appErr *sdktemporal.ApplicationError
		if !errors.As(err, &appErr) {
			t.Fatalf("expected ApplicationError, got: %T (%v)", err, err)
		}
		if appErr.Type() != "InvalidResultError" {
			t.Errorf("expected error type InvalidResultError, got: %s", appErr.Type())
		}
		if !appErr.NonRetryable() {
			t.Errorf("expected invalid result error to be non-retryable")
		}
	})

	t.Run("context cancellation preserves cancellation semantics", func(t *testing.T) {
		rt := agentruntime.NewDefaultRuntime()
		activities := temporal.NewAgentActivities(rt)
		activities.SetLogger(testLogger{})

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // pre-canceled

		input := temporal.AgentExecutionActivityInput{
			TenantID:       uuid.New(),
			AgentID:        uuid.New(),
			AgentVersionID: uuid.New(),
		}

		_, err := activities.AgentExecutionActivity(ctx, input)
		if err == nil {
			t.Fatalf("expected error for canceled activity, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got: %T (%v)", err, err)
		}
		var appErr *sdktemporal.ApplicationError
		if errors.As(err, &appErr) {
			t.Errorf("expected cancellation not to be wrapped in ApplicationError, got: %v", appErr)
		}
	})
}

func TestAgentExecutionWorkflow_Success(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	mock := agentruntime.NewMockExecutor()
	mock.Result = agentruntime.ExecutionResult{
		Status: agentruntime.StatusSuccess,
		Output: map[string]any{
			"summary": "Agent successfully processed query",
		},
	}
	rt, err := agentruntime.New(mock)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	agentActivities := temporal.NewAgentActivities(rt)
	env.RegisterWorkflow(temporal.AgentExecutionWorkflow)
	env.RegisterActivity(agentActivities.AgentExecutionActivity)

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	input := temporal.AgentExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionInput: map[string]any{"query": "test query"},
	}

	env.ExecuteWorkflow(temporal.AgentExecutionWorkflow, input)

	if !env.IsWorkflowCompleted() {
		t.Fatalf("expected workflow to complete")
	}

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	var result temporal.AgentExecutionResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("failed to decode workflow result: %v", err)
	}

	if result.TenantID != tenantID {
		t.Errorf("expected tenant ID %s, got %s", tenantID, result.TenantID)
	}
	if result.AgentID != agentID {
		t.Errorf("expected agent ID %s, got %s", agentID, result.AgentID)
	}
	if result.AgentVersionID != versionID {
		t.Errorf("expected agent version ID %s, got %s", versionID, result.AgentVersionID)
	}
	if result.Status != "SUCCESS" {
		t.Errorf("expected status SUCCESS, got %s", result.Status)
	}
	if result.Output == nil || result.Output["summary"] != "Agent successfully processed query" {
		t.Errorf("expected output to contain summary, got: %+v", result.Output)
	}
}

func TestAgentExecutionWorkflow_EscalationResult(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	mock := agentruntime.NewMockExecutor()
	mock.Result = agentruntime.ExecutionResult{
		Status: agentruntime.StatusEscalated,
		Reason: "Requires human authorization",
		Escalation: &agentruntime.EscalationInfo{
			Reason: "Transaction value exceeds policy threshold",
			Target: "compliance_officer",
		},
	}
	rt, _ := agentruntime.New(mock)
	agentActivities := temporal.NewAgentActivities(rt)

	env.RegisterWorkflow(temporal.AgentExecutionWorkflow)
	env.RegisterActivity(agentActivities.AgentExecutionActivity)

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	input := temporal.AgentExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionInput: map[string]any{"amount": 100000},
	}

	env.ExecuteWorkflow(temporal.AgentExecutionWorkflow, input)

	if !env.IsWorkflowCompleted() {
		t.Fatalf("expected workflow to complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	var result temporal.AgentExecutionResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("failed to decode workflow result: %v", err)
	}

	if result.Status != "ESCALATED" {
		t.Errorf("expected status ESCALATED, got %s", result.Status)
	}
	if result.Reason != "Requires human authorization" {
		t.Errorf("expected reason 'Requires human authorization', got %s", result.Reason)
	}
	if result.Escalation == nil || result.Escalation.Target != "compliance_officer" {
		t.Errorf("expected escalation target compliance_officer, got: %+v", result.Escalation)
	}
}

func TestAgentExecutionWorkflow_ActivityFailurePropagation(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	mock := &agentruntime.MockExecutor{
		Err: errors.New("simulated runtime internal error"),
	}
	rt, _ := agentruntime.New(mock)
	agentActivities := temporal.NewAgentActivities(rt)

	env.RegisterWorkflow(temporal.AgentExecutionWorkflow)
	env.RegisterActivity(agentActivities.AgentExecutionActivity)

	input := temporal.AgentExecutionInput{
		TenantID:       uuid.New(),
		AgentID:        uuid.New(),
		AgentVersionID: uuid.New(),
	}

	env.ExecuteWorkflow(temporal.AgentExecutionWorkflow, input)

	if !env.IsWorkflowCompleted() {
		t.Fatalf("expected workflow to complete with error")
	}

	err := env.GetWorkflowError()
	if err == nil {
		t.Fatalf("expected workflow to fail when activity fails, but got nil")
	}

	if !strings.Contains(err.Error(), "agent activity execution failed") {
		t.Errorf("expected error message to mention activity failure, got: %v", err)
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

func TestTemporal_CredentialsNeverInWorkflowInputsOrHistoryOrErrors(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	fakeSecret := "sk-fake-secret-never-expose-in-temporal-history-11111"

	// Mock executor that fails with an error string containing a secret
	mock := &agentruntime.MockExecutor{
		Err: errors.New("upstream failure containing secret " + fakeSecret),
	}
	rt, _ := agentruntime.New(mock)
	agentActivities := temporal.NewAgentActivities(rt)

	env.RegisterWorkflow(temporal.AgentExecutionWorkflow)
	env.RegisterActivity(agentActivities.AgentExecutionActivity)

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	input := temporal.AgentExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionInput: map[string]any{
			"prompt": "safe prompt text",
		},
	}

	env.ExecuteWorkflow(temporal.AgentExecutionWorkflow, input)

	if !env.IsWorkflowCompleted() {
		t.Fatalf("expected workflow to complete (with error)")
	}

	wfErr := env.GetWorkflowError()
	if wfErr == nil {
		t.Fatalf("expected workflow error, got nil")
	}

	// Verify that the workflow application error does NOT leak the fake secret
	errMsg := wfErr.Error()
	if strings.Contains(errMsg, fakeSecret) {
		t.Fatalf("CRITICAL SECURITY VIOLATION: Temporal workflow error leaked fake secret: %s", errMsg)
	}
}

func TestTemporal_StructuredOutput_Propagation(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	expectedStructured := map[string]any{
		"rating":   5.0,
		"approved": true,
	}

	mock := &agentruntime.MockExecutor{
		Result: agentruntime.ExecutionResult{
			Status: agentruntime.StatusSuccess,
			Output: map[string]any{
				"content":           `{"rating": 5, "approved": true}`,
				"structured_output": expectedStructured,
			},
			StructuredOutput: expectedStructured,
		},
	}
	rt, _ := agentruntime.New(mock)
	agentActivities := temporal.NewAgentActivities(rt)

	env.RegisterWorkflow(temporal.AgentExecutionWorkflow)
	env.RegisterActivity(agentActivities.AgentExecutionActivity)

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	input := temporal.AgentExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionInput: map[string]any{"text": "evaluate"},
	}

	env.ExecuteWorkflow(temporal.AgentExecutionWorkflow, input)

	if !env.IsWorkflowCompleted() {
		t.Fatalf("expected workflow to complete")
	}

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	var result temporal.AgentExecutionResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("failed to decode workflow result: %v", err)
	}

	if result.StructuredOutput == nil {
		t.Fatal("expected non-nil StructuredOutput on AgentExecutionResult")
	}
	soMap, ok := result.StructuredOutput.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got: %T", result.StructuredOutput)
	}
	if soMap["approved"] != true {
		t.Errorf("got approved %v, want true", soMap["approved"])
	}
}

func TestTemporal_StructuredOutput_InvalidOutputNonRetryable(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}

	// 1. Verify Activity execution returns non-retryable PermanentExecutionError
	t.Run("activity error classification", func(t *testing.T) {
		actEnv := suite.NewTestActivityEnvironment()
		mock := &agentruntime.MockExecutor{
			Err: agentruntime.NewPermanentError("model output violates schema", agentruntime.ErrSchemaViolation),
		}
		rt, _ := agentruntime.New(mock)
		agentActivities := temporal.NewAgentActivities(rt)
		actEnv.RegisterActivity(agentActivities.AgentExecutionActivity)

		tenantID := uuid.New()
		agentID := uuid.New()
		versionID := uuid.New()

		input := temporal.AgentExecutionActivityInput{
			TenantID:       tenantID,
			AgentID:        agentID,
			AgentVersionID: versionID,
		}

		_, err := actEnv.ExecuteActivity(agentActivities.AgentExecutionActivity, input)
		if err == nil {
			t.Fatalf("expected error from activity, got nil")
		}

		var appErr *sdktemporal.ApplicationError
		if !errors.As(err, &appErr) {
			t.Fatalf("expected ApplicationError, got: %T (%v)", err, err)
		}
		if appErr.Type() != "PermanentExecutionError" {
			t.Errorf("expected error type PermanentExecutionError, got: %s", appErr.Type())
		}
		if !appErr.NonRetryable() {
			t.Errorf("expected PermanentExecutionError to be non-retryable")
		}
	})

	// 2. Verify Workflow terminates on non-retryable error without infinite retries
	t.Run("workflow termination", func(t *testing.T) {
		wfEnv := suite.NewTestWorkflowEnvironment()
		mock := &agentruntime.MockExecutor{
			Err: agentruntime.NewPermanentError("model output violates schema", agentruntime.ErrSchemaViolation),
		}
		rt, _ := agentruntime.New(mock)
		agentActivities := temporal.NewAgentActivities(rt)

		wfEnv.RegisterWorkflow(temporal.AgentExecutionWorkflow)
		wfEnv.RegisterActivity(agentActivities.AgentExecutionActivity)

		tenantID := uuid.New()
		agentID := uuid.New()
		versionID := uuid.New()

		input := temporal.AgentExecutionInput{
			TenantID:       tenantID,
			AgentID:        agentID,
			AgentVersionID: versionID,
		}

		wfEnv.ExecuteWorkflow(temporal.AgentExecutionWorkflow, input)

		if !wfEnv.IsWorkflowCompleted() {
			t.Fatalf("expected workflow to complete (with error)")
		}

		wfErr := wfEnv.GetWorkflowError()
		if wfErr == nil {
			t.Fatalf("expected workflow error on schema violation, got nil")
		}

		// Verify error message reflects permanent execution failure
		if !strings.Contains(wfErr.Error(), "PermanentExecutionError") && !strings.Contains(wfErr.Error(), "permanent runtime execution failure") {
			t.Errorf("expected workflow error to indicate PermanentExecutionError, got: %v", wfErr)
		}
	})
}

func TestAgentExecutionWorkflow_EscalationOutcome(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	wfEnv := suite.NewTestWorkflowEnvironment()

	mock := &agentruntime.MockExecutor{
		Result: agentruntime.ExecutionResult{
			Status:  agentruntime.StatusEscalated,
			Outcome: agentruntime.OutcomeTypeEscalation,
			Escalation: &agentruntime.EscalationInfo{
				Reason: "requires supervisory human review",
				Target: "compliance_officer",
			},
		},
	}
	rt, _ := agentruntime.New(mock)
	agentActivities := temporal.NewAgentActivities(rt)

	wfEnv.RegisterWorkflow(temporal.AgentExecutionWorkflow)
	wfEnv.RegisterActivity(agentActivities.AgentExecutionActivity)

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	input := temporal.AgentExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
	}

	wfEnv.ExecuteWorkflow(temporal.AgentExecutionWorkflow, input)

	if !wfEnv.IsWorkflowCompleted() {
		t.Fatalf("expected workflow to complete")
	}
	if err := wfEnv.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	var res temporal.AgentExecutionResult
	if err := wfEnv.GetWorkflowResult(&res); err != nil {
		t.Fatalf("failed to decode result: %v", err)
	}

	if res.Status != "ESCALATED" {
		t.Errorf("expected status ESCALATED, got %s", res.Status)
	}
	if res.Escalation == nil || res.Escalation.Target != "compliance_officer" {
		t.Errorf("unexpected escalation: %+v", res.Escalation)
	}
}

func TestAgentExecutionActivity_ToolDenialNonRetryable(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	actEnv := suite.NewTestActivityEnvironment()

	mock := &agentruntime.MockExecutor{
		Err: agentruntime.NewPermanentError("tool execution denied by gateway", agentruntime.ErrToolDenied),
	}
	rt, _ := agentruntime.New(mock)
	agentActivities := temporal.NewAgentActivities(rt)
	actEnv.RegisterActivity(agentActivities.AgentExecutionActivity)

	input := temporal.AgentExecutionActivityInput{
		TenantID:       uuid.New(),
		AgentID:        uuid.New(),
		AgentVersionID: uuid.New(),
	}

	_, err := actEnv.ExecuteActivity(agentActivities.AgentExecutionActivity, input)
	if err == nil {
		t.Fatalf("expected error from activity, got nil")
	}

	var appErr *sdktemporal.ApplicationError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected ApplicationError, got: %T (%v)", err, err)
	}
	if appErr.Type() != "PermanentExecutionError" {
		t.Errorf("expected PermanentExecutionError, got: %s", appErr.Type())
	}
	if !appErr.NonRetryable() {
		t.Errorf("expected PermanentExecutionError to be non-retryable to prevent retry bypass of tool authorization")
	}
}
