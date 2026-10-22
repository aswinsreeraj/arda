package agentruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

var (
	// ErrInvalidInput indicates that the provided ExecutionInput is invalid or missing required identifiers.
	ErrInvalidInput = errors.New("agentruntime: invalid execution input")

	// ErrExecutionFailed indicates an unrecoverable or simulated runtime failure.
	ErrExecutionFailed = errors.New("agentruntime: execution failed")

	// ErrNilExecutor indicates that a nil executor was provided when creating a Runtime.
	ErrNilExecutor = errors.New("agentruntime: executor cannot be nil")
)

// ExecutionStatus defines the outcome status of an agent execution.
type ExecutionStatus string

const (
	// StatusSuccess denotes that agent execution completed successfully.
	StatusSuccess ExecutionStatus = "SUCCESS"

	// StatusEscalated denotes that execution required escalation to a supervisor or human.
	StatusEscalated ExecutionStatus = "ESCALATED"

	// StatusFailed denotes that execution terminated with a failure.
	StatusFailed ExecutionStatus = "FAILED"
)

// EscalationInfo carries structured escalation context when an agent execution cannot proceed autonomously.
type EscalationInfo struct {
	Reason string `json:"reason,omitempty"`
	Target string `json:"target,omitempty"`
}

// ExecutionInput holds the ARDA execution context and user payload for an agent run.
type ExecutionInput struct {
	TenantID       uuid.UUID      `json:"tenant_id"`
	AgentID        uuid.UUID      `json:"agent_id"`
	AgentVersionID uuid.UUID      `json:"agent_version_id"`
	Payload        map[string]any `json:"payload,omitempty"`
}

// Validate verifies that mandatory tenant, agent, and pinned version identifiers are present.
func (in ExecutionInput) Validate() error {
	if in.TenantID == uuid.Nil {
		return fmt.Errorf("%w: tenant_id is required", ErrInvalidInput)
	}
	if in.AgentID == uuid.Nil {
		return fmt.Errorf("%w: agent_id is required", ErrInvalidInput)
	}
	if in.AgentVersionID == uuid.Nil {
		return fmt.Errorf("%w: agent_version_id is required", ErrInvalidInput)
	}
	return nil
}

// ExecutionResult represents the structured outcome of an agent execution.
type ExecutionResult struct {
	Status     ExecutionStatus `json:"status"`
	Output     map[string]any  `json:"output,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Escalation *EscalationInfo `json:"escalation,omitempty"`
}

// AgentRuntime defines the high-level contract for invoking an agent.
// It is owned by ARDA, independent of Temporal and framework-specific types,
// enabling reuse across durable workflows and realtime execution services.
type AgentRuntime interface {
	Run(ctx context.Context, input ExecutionInput) (ExecutionResult, error)
}

// Executor represents the internal execution engine (e.g. mock executor, or future LangChainGo adapter).
type Executor interface {
	Execute(ctx context.Context, input ExecutionInput) (ExecutionResult, error)
}

// ExecutorFunc adapts a standard function signature to the Executor interface.
type ExecutorFunc func(ctx context.Context, input ExecutionInput) (ExecutionResult, error)

// Execute calls the underlying function.
func (f ExecutorFunc) Execute(ctx context.Context, input ExecutionInput) (ExecutionResult, error) {
	return f(ctx, input)
}

// Runtime is the standard implementation of AgentRuntime, delegating execution to an injected Executor.
type Runtime struct {
	executor Executor
}

// New constructs a Runtime with an explicit Executor dependency.
func New(executor Executor) (*Runtime, error) {
	if executor == nil {
		return nil, ErrNilExecutor
	}
	return &Runtime{executor: executor}, nil
}

// Run executes the agent using the injected executor after validating input and checking context.
func (r *Runtime) Run(ctx context.Context, input ExecutionInput) (ExecutionResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}
	if err := input.Validate(); err != nil {
		return ExecutionResult{}, err
	}
	return r.executor.Execute(ctx, input)
}
