package agentruntime

import (
	"context"
	"fmt"
)

// MockExecutor provides deterministic agent execution for development, unit testing,
// and initial system increment validation without requiring real LLM providers or external services.
type MockExecutor struct {
	Result      ExecutionResult
	Err         error
	ExecuteFunc func(ctx context.Context, input ExecutionInput) (ExecutionResult, error)
}

// Execute fulfills the Executor interface, returning predetermined outputs or simulated errors.
func (m *MockExecutor) Execute(ctx context.Context, input ExecutionInput) (ExecutionResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}

	if m.ExecuteFunc != nil {
		return m.ExecuteFunc(ctx, input)
	}

	if m.Err != nil {
		return ExecutionResult{}, m.Err
	}

	// Controlled failure simulation via payload flags
	if input.Payload != nil {
		if fail, ok := input.Payload["fail_simulation"].(bool); ok && fail {
			return ExecutionResult{}, fmt.Errorf("%w: simulated failure requested in payload", ErrExecutionFailed)
		}
		if failStr, ok := input.Payload["fail_simulation"].(string); ok && failStr == "FAIL_SIMULATION" {
			return ExecutionResult{}, fmt.Errorf("%w: simulated failure requested in payload", ErrExecutionFailed)
		}
	}

	return m.Result, nil
}

// NewMockExecutor creates a MockExecutor with a default successful deterministic execution result.
func NewMockExecutor() *MockExecutor {
	return &MockExecutor{
		Result: ExecutionResult{
			Status: StatusSuccess,
			Output: map[string]any{
				"status":  "completed",
				"message": "execution completed deterministically by mock runtime",
			},
		},
	}
}

// NewDefaultRuntime creates a Runtime instance backed by a deterministic MockExecutor.
// This is suitable as the default runtime for workers in local development and testing.
func NewDefaultRuntime() *Runtime {
	rt, _ := New(NewMockExecutor())
	return rt
}
