package agentruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/google/uuid"
)

func validExecutionInput() agentruntime.ExecutionInput {
	return agentruntime.ExecutionInput{
		TenantID:       uuid.New(),
		AgentID:        uuid.New(),
		AgentVersionID: uuid.New(),
		Payload: map[string]any{
			"prompt": "Analyze incoming support ticket",
		},
	}
}

func TestRuntime_DefaultMockReturnsExpectedResult(t *testing.T) {
	rt := agentruntime.NewDefaultRuntime()
	input := validExecutionInput()

	res, err := rt.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("expected successful execution, got error: %v", err)
	}

	if res.Status != agentruntime.StatusSuccess {
		t.Errorf("expected status %s, got %s", agentruntime.StatusSuccess, res.Status)
	}

	if res.Output == nil || res.Output["message"] != "execution completed deterministically by mock runtime" {
		t.Errorf("expected deterministic mock output message, got: %+v", res.Output)
	}
}

func TestRuntime_CustomMockResult(t *testing.T) {
	mock := agentruntime.NewMockExecutor()
	mock.Result = agentruntime.ExecutionResult{
		Status: agentruntime.StatusEscalated,
		Output: map[string]any{"action": "escalate"},
		Reason: "Confidence threshold below 0.85",
		Escalation: &agentruntime.EscalationInfo{
			Reason: "Human review required",
			Target: "tier2_support",
		},
	}

	rt, err := agentruntime.New(mock)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	res, err := rt.Run(context.Background(), validExecutionInput())
	if err != nil {
		t.Fatalf("unexpected runtime error: %v", err)
	}

	if res.Status != agentruntime.StatusEscalated {
		t.Errorf("expected status %s, got %s", agentruntime.StatusEscalated, res.Status)
	}
	if res.Reason != "Confidence threshold below 0.85" {
		t.Errorf("expected reason 'Confidence threshold below 0.85', got %s", res.Reason)
	}
	if res.Escalation == nil || res.Escalation.Target != "tier2_support" {
		t.Errorf("expected escalation target 'tier2_support', got %+v", res.Escalation)
	}
}

func TestRuntime_MockConfiguredToFail(t *testing.T) {
	expectedErr := errors.New("backend model timeout")
	mock := &agentruntime.MockExecutor{
		Err: expectedErr,
	}

	rt, err := agentruntime.New(mock)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	_, err = rt.Run(context.Background(), validExecutionInput())
	if err == nil {
		t.Fatalf("expected error from runtime, got nil")
	}

	if !errors.Is(err, expectedErr) {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}
}

func TestRuntime_PayloadSimulationFailure(t *testing.T) {
	rt := agentruntime.NewDefaultRuntime()
	input := validExecutionInput()
	input.Payload["fail_simulation"] = true

	_, err := rt.Run(context.Background(), input)
	if err == nil {
		t.Fatalf("expected error when fail_simulation is true, got nil")
	}

	if !errors.Is(err, agentruntime.ErrTransient) {
		t.Errorf("expected ErrTransient, got: %v", err)
	}
}

func TestRuntime_PayloadPermanentFailure(t *testing.T) {
	rt := agentruntime.NewDefaultRuntime()
	input := validExecutionInput()
	input.Payload["fail_permanent"] = true

	_, err := rt.Run(context.Background(), input)
	if err == nil {
		t.Fatalf("expected error when fail_permanent is true, got nil")
	}

	if !errors.Is(err, agentruntime.ErrPermanent) {
		t.Errorf("expected ErrPermanent, got: %v", err)
	}
}

func TestRuntime_ExecuteFunc(t *testing.T) {
	called := false
	mock := &agentruntime.MockExecutor{
		ExecuteFunc: func(ctx context.Context, input agentruntime.ExecutionInput) (agentruntime.ExecutionResult, error) {
			called = true
			return agentruntime.ExecutionResult{
				Status: agentruntime.StatusSuccess,
				Output: map[string]any{"custom": "handler"},
			}, nil
		},
	}

	rt, err := agentruntime.New(mock)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	res, err := rt.Run(context.Background(), validExecutionInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatalf("expected custom ExecuteFunc to be invoked")
	}
	if res.Output["custom"] != "handler" {
		t.Errorf("unexpected output: %+v", res.Output)
	}
}

func TestRuntime_InputValidation(t *testing.T) {
	rt := agentruntime.NewDefaultRuntime()
	base := validExecutionInput()

	tests := []struct {
		name      string
		modifyFn  func(in *agentruntime.ExecutionInput)
		expectErr error
	}{
		{
			name: "missing tenant_id",
			modifyFn: func(in *agentruntime.ExecutionInput) {
				in.TenantID = uuid.Nil
			},
			expectErr: agentruntime.ErrInvalidInput,
		},
		{
			name: "missing agent_id",
			modifyFn: func(in *agentruntime.ExecutionInput) {
				in.AgentID = uuid.Nil
			},
			expectErr: agentruntime.ErrInvalidInput,
		},
		{
			name: "missing agent_version_id",
			modifyFn: func(in *agentruntime.ExecutionInput) {
				in.AgentVersionID = uuid.Nil
			},
			expectErr: agentruntime.ErrInvalidInput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := base
			tt.modifyFn(&input)

			_, err := rt.Run(context.Background(), input)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !errors.Is(err, tt.expectErr) {
				t.Errorf("expected error %v, got %v", tt.expectErr, err)
			}
		})
	}
}

func TestRuntime_ResultValidation(t *testing.T) {
	tests := []struct {
		name      string
		result    agentruntime.ExecutionResult
		expectErr bool
	}{
		{
			name: "valid success",
			result: agentruntime.ExecutionResult{
				Status: agentruntime.StatusSuccess,
				Output: map[string]any{"msg": "ok"},
			},
			expectErr: false,
		},
		{
			name: "valid escalation with reason",
			result: agentruntime.ExecutionResult{
				Status: agentruntime.StatusEscalated,
				Escalation: &agentruntime.EscalationInfo{
					Reason: "need review",
				},
			},
			expectErr: false,
		},
		{
			name: "valid escalation with target",
			result: agentruntime.ExecutionResult{
				Status: agentruntime.StatusEscalated,
				Escalation: &agentruntime.EscalationInfo{
					Target: "supervisor",
				},
			},
			expectErr: false,
		},
		{
			name: "escalation missing escalation info",
			result: agentruntime.ExecutionResult{
				Status:     agentruntime.StatusEscalated,
				Escalation: nil,
			},
			expectErr: true,
		},
		{
			name: "escalation with empty reason and target",
			result: agentruntime.ExecutionResult{
				Status: agentruntime.StatusEscalated,
				Escalation: &agentruntime.EscalationInfo{
					Reason: "",
					Target: "",
				},
			},
			expectErr: true,
		},
		{
			name: "valid failure with reason",
			result: agentruntime.ExecutionResult{
				Status: agentruntime.StatusFailed,
				Reason: "guardrail policy triggered",
			},
			expectErr: false,
		},
		{
			name: "failed without reason",
			result: agentruntime.ExecutionResult{
				Status: agentruntime.StatusFailed,
				Reason: "",
			},
			expectErr: true,
		},
		{
			name: "empty status",
			result: agentruntime.ExecutionResult{
				Status: "",
			},
			expectErr: true,
		},
		{
			name: "unsupported status",
			result: agentruntime.ExecutionResult{
				Status: "UNKNOWN_STATUS",
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.result.Validate()
			if (err != nil) != tt.expectErr {
				t.Errorf("Validate() error = %v, expectErr = %v", err, tt.expectErr)
			}
			if tt.expectErr && !errors.Is(err, agentruntime.ErrInvalidResult) {
				t.Errorf("expected ErrInvalidResult, got: %v", err)
			}
		})
	}
}

func TestRuntime_RejectsInvalidResultFromExecutor(t *testing.T) {
	mock := &agentruntime.MockExecutor{
		Result: agentruntime.ExecutionResult{
			Status: "INVALID_STATUS",
		},
	}
	rt, err := agentruntime.New(mock)
	if err != nil {
		t.Fatalf("unexpected error creating runtime: %v", err)
	}

	_, err = rt.Run(context.Background(), validExecutionInput())
	if err == nil {
		t.Fatalf("expected error from invalid result, got nil")
	}
	if !errors.Is(err, agentruntime.ErrInvalidResult) {
		t.Errorf("expected ErrInvalidResult, got %v", err)
	}
}

func TestRuntime_ErrorClassification(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected agentruntime.ErrorCategory
	}{
		{
			name:     "context canceled",
			err:      context.Canceled,
			expected: agentruntime.CategoryCanceled,
		},
		{
			name:     "context deadline exceeded",
			err:      context.DeadlineExceeded,
			expected: agentruntime.CategoryCanceled,
		},
		{
			name:     "invalid input error",
			err:      agentruntime.ErrInvalidInput,
			expected: agentruntime.CategoryInvalidInput,
		},
		{
			name:     "invalid result error",
			err:      agentruntime.ErrInvalidResult,
			expected: agentruntime.CategoryInvalidResult,
		},
		{
			name:     "permanent error",
			err:      agentruntime.NewPermanentError("forbidden", nil),
			expected: agentruntime.CategoryPermanent,
		},
		{
			name:     "transient error",
			err:      agentruntime.NewTransientError("rate limited", nil),
			expected: agentruntime.CategoryTransient,
		},
		{
			name:     "configuration not found error",
			err:      agentruntime.ErrConfigurationNotFound,
			expected: agentruntime.CategoryPermanent,
		},
		{
			name:     "invalid configuration error",
			err:      agentruntime.ErrInvalidConfiguration,
			expected: agentruntime.CategoryPermanent,
		},
		{
			name:     "nil config provider error",
			err:      agentruntime.ErrNilConfigProvider,
			expected: agentruntime.CategoryPermanent,
		},
		{
			name:     "nil llm client error",
			err:      agentruntime.ErrNilLLMClient,
			expected: agentruntime.CategoryPermanent,
		},
		{
			name:     "generic error defaults to transient",
			err:      errors.New("something broke"),
			expected: agentruntime.CategoryTransient,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat := agentruntime.ClassifyError(tt.err)
			if cat != tt.expected {
				t.Errorf("expected category %s, got %s", tt.expected, cat)
			}
		})
	}
}

func TestRuntime_ContextCancellation(t *testing.T) {
	rt := agentruntime.NewDefaultRuntime()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := rt.Run(ctx, validExecutionInput())
	if err == nil {
		t.Fatalf("expected context canceled error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestRuntime_NilExecutorError(t *testing.T) {
	_, err := agentruntime.New(nil)
	if err == nil {
		t.Fatalf("expected error when passing nil executor, got nil")
	}
	if !errors.Is(err, agentruntime.ErrNilExecutor) {
		t.Errorf("expected ErrNilExecutor, got: %v", err)
	}
}

func TestExecutorFunc(t *testing.T) {
	fn := agentruntime.ExecutorFunc(func(ctx context.Context, input agentruntime.ExecutionInput) (agentruntime.ExecutionResult, error) {
		return agentruntime.ExecutionResult{Status: agentruntime.StatusSuccess}, nil
	})

	rt, err := agentruntime.New(fn)
	if err != nil {
		t.Fatalf("unexpected error creating runtime: %v", err)
	}

	res, err := rt.Run(context.Background(), validExecutionInput())
	if err != nil {
		t.Fatalf("unexpected error running runtime: %v", err)
	}
	if res.Status != agentruntime.StatusSuccess {
		t.Errorf("expected status %s, got %s", agentruntime.StatusSuccess, res.Status)
	}
}
