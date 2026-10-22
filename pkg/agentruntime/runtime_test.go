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

	if !errors.Is(err, agentruntime.ErrExecutionFailed) {
		t.Errorf("expected ErrExecutionFailed, got: %v", err)
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
