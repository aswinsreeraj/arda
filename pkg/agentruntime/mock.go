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
		if perm, ok := input.Payload["fail_permanent"].(bool); ok && perm {
			return ExecutionResult{}, fmt.Errorf("%w: simulated permanent failure requested in payload", ErrPermanent)
		}
		if fail, ok := input.Payload["fail_simulation"].(bool); ok && fail {
			return ExecutionResult{}, fmt.Errorf("%w: simulated failure requested in payload", ErrTransient)
		}
		if failStr, ok := input.Payload["fail_simulation"].(string); ok && failStr == "FAIL_SIMULATION" {
			return ExecutionResult{}, fmt.Errorf("%w: simulated failure requested in payload", ErrTransient)
		}
	}

	return m.Result, nil
}

// Run fulfills the AgentRuntime interface, allowing MockExecutor to be passed directly as an AgentRuntime.
func (m *MockExecutor) Run(ctx context.Context, input ExecutionInput) (ExecutionResult, error) {
	return m.Execute(ctx, input)
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
// This constructor cannot fail because its mock executor dependency is fixed and non-nil.
func NewDefaultRuntime() *Runtime {
	return &Runtime{
		executor: NewMockExecutor(),
	}
}
