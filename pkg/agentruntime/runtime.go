package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

var (
	// ErrInvalidInput indicates that the provided ExecutionInput is invalid or missing required identifiers.
	ErrInvalidInput = errors.New("agentruntime: invalid execution input")

	// ErrInvalidResult indicates that the returned ExecutionResult has an invalid status or inconsistent fields.
	ErrInvalidResult = errors.New("agentruntime: invalid execution result")

	// ErrExecutionFailed indicates an unrecoverable or simulated runtime failure.
	ErrExecutionFailed = errors.New("agentruntime: execution failed")

	// ErrPermanent indicates an unrecoverable failure (e.g. invalid config, non-retryable provider rejection).
	ErrPermanent = errors.New("agentruntime: permanent execution failure")

	// ErrTransient indicates a temporary failure (e.g. network timeout, rate limit) that may be retried.
	ErrTransient = errors.New("agentruntime: transient execution failure")

	// ErrNilExecutor indicates that a nil executor was provided when creating a Runtime.
	ErrNilExecutor = errors.New("agentruntime: executor cannot be nil")

	// ErrNilLLMClient indicates that a nil LLMClient was provided.
	ErrNilLLMClient = errors.New("agentruntime: llm client cannot be nil")

	// ErrNilConfigProvider indicates that a nil AgentConfigProvider was provided.
	ErrNilConfigProvider = errors.New("agentruntime: configuration provider cannot be nil")

	// ErrConfigurationNotFound indicates that no configuration was found for the specified tenant, agent, and version.
	ErrConfigurationNotFound = errors.New("agentruntime: configuration not found")

	// ErrInvalidConfiguration indicates that the resolved configuration failed validation.
	ErrInvalidConfiguration = errors.New("agentruntime: invalid agent configuration")

	// ErrUnsupportedProvider indicates that the requested provider is not registered or supported.
	ErrUnsupportedProvider = errors.New("agentruntime: unsupported provider")

	// ErrUnsupportedModel indicates that the requested model is not supported by the selected provider.
	ErrUnsupportedModel = errors.New("agentruntime: unsupported model")

	// ErrMissingProvider indicates that no provider identifier was specified in config or defaults.
	ErrMissingProvider = errors.New("agentruntime: missing provider identifier")

	// ErrInvalidTenant indicates that the tenant ID is missing or invalid for scoped credential resolution.
	ErrInvalidTenant = errors.New("agentruntime: invalid tenant identifier")

	// ErrCredentialNotFound indicates that no credential exists in Vault for the tenant and provider.
	ErrCredentialNotFound = errors.New("agentruntime: credential not found")

	// ErrEmptyCredential indicates that the resolved credential string was empty.
	ErrEmptyCredential = errors.New("agentruntime: empty credential value")

	// ErrMissingCredential indicates that a required credential was not provided or resolver is unconfigured.
	ErrMissingCredential = errors.New("agentruntime: missing provider credential")

	// ErrInvalidOutputSchema indicates that the configured output schema is invalid.
	ErrInvalidOutputSchema = errors.New("agentruntime: invalid output schema")

	// ErrInvalidOutputJSON indicates that the model output is malformed JSON.
	ErrInvalidOutputJSON = errors.New("agentruntime: invalid output JSON")

	// ErrSchemaViolation indicates that the model output violates the configured JSON Schema.
	ErrSchemaViolation = errors.New("agentruntime: output schema violation")

	// ErrOutputTooLarge indicates that the model output exceeds the size limit.
	ErrOutputTooLarge = errors.New("agentruntime: output exceeds size limit")

	// ErrInternalValidation indicates an internal failure during schema compilation or validation.
	ErrInternalValidation = errors.New("agentruntime: internal validation failure")

	// ErrBudgetExceeded indicates that an execution budget (iterations, tokens, size) was exhausted.
	ErrBudgetExceeded = errors.New("agentruntime: execution budget exceeded")

	// ErrRepeatedToolCall indicates that an unprogressed repeated identical tool invocation was detected.
	ErrRepeatedToolCall = errors.New("agentruntime: repeated identical tool call detected")

	// ErrToolDenied indicates that a requested tool was denied by policy or authorization.
	ErrToolDenied = errors.New("agentruntime: tool execution denied")

	// ErrToolExecutionFailed indicates a failure during tool execution.
	ErrToolExecutionFailed = errors.New("agentruntime: tool execution failed")

	// ErrReliabilityRejected indicates that the Reliability Service gate rejected the action or decision.
	ErrReliabilityRejected = errors.New("agentruntime: reliability gate rejected proposed action")

	// ErrReliabilityServiceUnavailable indicates that the Reliability Service could not be reached.
	ErrReliabilityServiceUnavailable = errors.New("agentruntime: reliability service unavailable")

	// ErrNilReliabilityService indicates that a required ReliabilityService dependency was not provided.
	ErrNilReliabilityService = errors.New("agentruntime: reliability service cannot be nil")

	// ErrNilToolGateway indicates that a required ToolGateway dependency was not provided.
	ErrNilToolGateway = errors.New("agentruntime: tool gateway cannot be nil")
)

// ErrorCategory defines the classification of a runtime failure for retry and propagation decisions.
type ErrorCategory string

const (
	// CategoryInvalidInput denotes non-retryable input validation errors.
	CategoryInvalidInput ErrorCategory = "INVALID_INPUT"

	// CategoryInvalidResult denotes non-retryable execution result contract violations.
	CategoryInvalidResult ErrorCategory = "INVALID_RESULT"

	// CategoryPermanent denotes non-retryable runtime or provider errors.
	CategoryPermanent ErrorCategory = "PERMANENT"

	// CategoryTransient denotes retryable transient errors (network, timeouts, rate limits).
	CategoryTransient ErrorCategory = "TRANSIENT"

	// CategoryCanceled denotes context cancellation or deadline expiration.
	CategoryCanceled ErrorCategory = "CANCELED"
)

// Classifier is implemented by custom errors to declare an error category and retryability.
type Classifier interface {
	ErrorCategory() ErrorCategory
	IsRetryable() bool
}

// RuntimeError is a structured error carrying an explicit error classification category.
type RuntimeError struct {
	Category ErrorCategory
	Message  string
	Err      error
}

func (e *RuntimeError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *RuntimeError) Unwrap() error {
	return e.Err
}

func (e *RuntimeError) ErrorCategory() ErrorCategory {
	return e.Category
}

func (e *RuntimeError) IsRetryable() bool {
	return e.Category == CategoryTransient
}

// NewPermanentError constructs a non-retryable RuntimeError.
func NewPermanentError(message string, err error) *RuntimeError {
	return &RuntimeError{
		Category: CategoryPermanent,
		Message:  message,
		Err:      err,
	}
}

// NewTransientError constructs a retryable RuntimeError.
func NewTransientError(message string, err error) *RuntimeError {
	return &RuntimeError{
		Category: CategoryTransient,
		Message:  message,
		Err:      err,
	}
}

// ClassifyError inspects an error's sentinels, interfaces, and standard contexts to determine its ErrorCategory.
func ClassifyError(err error) ErrorCategory {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return CategoryCanceled
	}
	if errors.Is(err, ErrInvalidInput) ||
		errors.Is(err, ErrMalformedPlaceholder) ||
		errors.Is(err, ErrMissingPlaceholderValue) ||
		errors.Is(err, ErrUnsupportedValueType) ||
		errors.Is(err, ErrInvalidLLMRequest) ||
		errors.Is(err, ErrInvalidTenant) ||
		errors.Is(err, ErrMissingProvider) {
		return CategoryInvalidInput
	}
	if errors.Is(err, ErrInvalidResult) {
		return CategoryInvalidResult
	}
	if errors.Is(err, ErrPermanent) ||
		errors.Is(err, ErrConfigurationNotFound) ||
		errors.Is(err, ErrInvalidConfiguration) ||
		errors.Is(err, ErrNilConfigProvider) ||
		errors.Is(err, ErrNilLLMClient) ||
		errors.Is(err, ErrUnsupportedProvider) ||
		errors.Is(err, ErrUnsupportedModel) ||
		errors.Is(err, ErrCredentialNotFound) ||
		errors.Is(err, ErrEmptyCredential) ||
		errors.Is(err, ErrMissingCredential) ||
		errors.Is(err, ErrInvalidOutputSchema) ||
		errors.Is(err, ErrInvalidOutputJSON) ||
		errors.Is(err, ErrSchemaViolation) ||
		errors.Is(err, ErrOutputTooLarge) ||
		errors.Is(err, ErrInternalValidation) ||
		errors.Is(err, ErrBudgetExceeded) ||
		errors.Is(err, ErrRepeatedToolCall) ||
		errors.Is(err, ErrToolDenied) ||
		errors.Is(err, ErrReliabilityRejected) ||
		errors.Is(err, ErrReliabilityServiceUnavailable) ||
		errors.Is(err, ErrNilReliabilityService) ||
		errors.Is(err, ErrNilToolGateway) {
		return CategoryPermanent
	}
	if errors.Is(err, ErrTransient) {
		return CategoryTransient
	}

	var classifier Classifier
	if errors.As(err, &classifier) {
		return classifier.ErrorCategory()
	}

	// By default, generic execution failures are treated as transient to allow bounded retries
	return CategoryTransient
}

// OutcomeType represents the explicit discriminated outcome of an agent execution.
type OutcomeType string

const (
	// OutcomeTypeDecision denotes a successfully validated structured decision.
	OutcomeTypeDecision OutcomeType = "DECISION"

	// OutcomeTypeEscalation denotes that human or Reliability Service review is required.
	OutcomeTypeEscalation OutcomeType = "ESCALATION"

	// OutcomeTypeFailure denotes an execution error or failure.
	OutcomeTypeFailure OutcomeType = "FAILURE"
)

// SupportedOutcomeTypes is the set of recognized outcome constants.
var SupportedOutcomeTypes = map[OutcomeType]struct{}{
	OutcomeTypeDecision:   {},
	OutcomeTypeEscalation: {},
	OutcomeTypeFailure:    {},
}

// ExecutionMetadata captures execution telemetry, budgets, and reliability details.
type ExecutionMetadata struct {
	Iterations          int    `json:"iterations"`
	ToolInvocations     int    `json:"tool_invocations"`
	PromptTokens        int    `json:"prompt_tokens,omitempty"`
	CompletionTokens    int    `json:"completion_tokens,omitempty"`
	TotalTokens         int    `json:"total_tokens,omitempty"`
	ReliabilityDecision string `json:"reliability_decision,omitempty"`
	ToolDenialCategory  string `json:"tool_denial_category,omitempty"`
}

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

// SupportedExecutionStatuses is the set of recognized execution status constants.
var SupportedExecutionStatuses = map[ExecutionStatus]struct{}{
	StatusSuccess:   {},
	StatusEscalated: {},
	StatusFailed:    {},
}

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
	Status           ExecutionStatus    `json:"status"`
	Outcome          OutcomeType        `json:"outcome,omitempty"`
	Output           map[string]any     `json:"output,omitempty"`
	StructuredOutput any                `json:"structured_output,omitempty"`
	Reason           string             `json:"reason,omitempty"`
	Escalation       *EscalationInfo    `json:"escalation,omitempty"`
	Metadata         *ExecutionMetadata `json:"metadata,omitempty"`
}

// IsDecision returns true if the execution completed as a validated decision.
func (r ExecutionResult) IsDecision() bool {
	return r.Status == StatusSuccess && (r.Outcome == "" || r.Outcome == OutcomeTypeDecision)
}

// IsEscalation returns true if the execution outcome is an escalation.
func (r ExecutionResult) IsEscalation() bool {
	return r.Status == StatusEscalated && (r.Outcome == "" || r.Outcome == OutcomeTypeEscalation)
}

// IsFailure returns true if the execution resulted in a failure.
func (r ExecutionResult) IsFailure() bool {
	return r.Status == StatusFailed && (r.Outcome == "" || r.Outcome == OutcomeTypeFailure)
}

// Validate enforces valid status values and structural constraints on execution results.
func (r ExecutionResult) Validate() error {
	if r.Status == "" {
		return fmt.Errorf("%w: status cannot be empty", ErrInvalidResult)
	}
	if _, ok := SupportedExecutionStatuses[r.Status]; !ok {
		return fmt.Errorf("%w: unsupported status %q", ErrInvalidResult, r.Status)
	}

	outcome := r.Outcome
	if outcome != "" {
		if _, ok := SupportedOutcomeTypes[outcome]; !ok {
			return fmt.Errorf("%w: unsupported outcome %q", ErrInvalidResult, outcome)
		}
	}

	switch r.Status {
	case StatusSuccess:
		if outcome != "" && outcome != OutcomeTypeDecision {
			return fmt.Errorf("%w: success status cannot have outcome %q", ErrInvalidResult, outcome)
		}
	case StatusEscalated:
		if outcome != "" && outcome != OutcomeTypeEscalation {
			return fmt.Errorf("%w: escalated status cannot have outcome %q", ErrInvalidResult, outcome)
		}
		if r.Escalation == nil {
			return fmt.Errorf("%w: escalation status requires escalation details", ErrInvalidResult)
		}
		if strings.TrimSpace(r.Escalation.Reason) == "" && strings.TrimSpace(r.Escalation.Target) == "" {
			return fmt.Errorf("%w: escalation status requires non-empty reason or target", ErrInvalidResult)
		}
	case StatusFailed:
		if outcome != "" && outcome != OutcomeTypeFailure {
			return fmt.Errorf("%w: failed status cannot have outcome %q", ErrInvalidResult, outcome)
		}
		if strings.TrimSpace(r.Reason) == "" {
			return fmt.Errorf("%w: failed status requires a non-empty explanation reason", ErrInvalidResult)
		}
	}
	return nil
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

// Run executes the agent using the injected executor after validating input, checking context, and validating output.
func (r *Runtime) Run(ctx context.Context, input ExecutionInput) (ExecutionResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}
	if err := input.Validate(); err != nil {
		return ExecutionResult{}, err
	}

	res, err := r.executor.Execute(ctx, input)
	if err != nil {
		return ExecutionResult{}, err
	}

	if res.Outcome == "" {
		switch res.Status {
		case StatusSuccess:
			res.Outcome = OutcomeTypeDecision
		case StatusEscalated:
			res.Outcome = OutcomeTypeEscalation
		case StatusFailed:
			res.Outcome = OutcomeTypeFailure
		}
	}

	if err := res.Validate(); err != nil {
		return ExecutionResult{}, err
	}

	return res, nil
}
