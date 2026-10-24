package agentruntime

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ToolExecutionRequest carries the authorized context required by the Tool Gateway to authorize and dispatch a tool call.
// The security identifiers (TenantID, AgentID, AgentVersionID, ExecutionID) are populated exclusively from
// trusted execution context and can NEVER be overridden by model-generated arguments.
type ToolExecutionRequest struct {
	TenantID       uuid.UUID      `json:"tenant_id"`
	AgentID        uuid.UUID      `json:"agent_id"`
	AgentVersionID uuid.UUID      `json:"agent_version_id"`
	ExecutionID    string         `json:"execution_id"`
	ToolName       string         `json:"tool_name"`
	Arguments      map[string]any `json:"arguments"`
}

// Validate ensures that required security context and tool name are present.
func (r ToolExecutionRequest) Validate() error {
	if r.TenantID == uuid.Nil {
		return fmt.Errorf("%w: tenant_id is required for tool execution", ErrInvalidInput)
	}
	if r.AgentID == uuid.Nil {
		return fmt.Errorf("%w: agent_id is required for tool execution", ErrInvalidInput)
	}
	if r.AgentVersionID == uuid.Nil {
		return fmt.Errorf("%w: agent_version_id is required for tool execution", ErrInvalidInput)
	}
	if strings.TrimSpace(r.ToolName) == "" {
		return fmt.Errorf("%w: tool_name is required", ErrInvalidInput)
	}
	return nil
}

// ToolExecutionResponse represents the outcome of a tool execution evaluated and dispatched by the Tool Gateway.
type ToolExecutionResponse struct {
	// Allowed indicates whether the tool invocation was authorized by the gateway.
	Allowed bool `json:"allowed"`

	// Result contains structured return data from the tool invocation.
	Result map[string]any `json:"result,omitempty"`

	// Output contains string serialized output from the tool.
	Output string `json:"output,omitempty"`

	// Error contains the error or denial message when execution fails or is blocked.
	Error string `json:"error,omitempty"`

	// DenialCode indicates the machine-readable reason for a gateway denial (e.g., "UNAUTHORIZED", "GUARDRAIL_BLOCKED").
	DenialCode string `json:"denial_code,omitempty"`

	// Category indicates the error classification for failure handling.
	Category ErrorCategory `json:"category,omitempty"`
}

// ToolGateway defines the narrow boundary between the agent runtime loop and external tool backends.
// The runtime never executes tools directly, delegating authorization, permissions, guardrails,
// audit logging, and dispatching to this gateway.
type ToolGateway interface {
	ExecuteTool(ctx context.Context, req ToolExecutionRequest) (ToolExecutionResponse, error)
}

// MockToolGateway provides a deterministic tool gateway for unit testing.
type MockToolGateway struct {
	ExecuteFunc func(ctx context.Context, req ToolExecutionRequest) (ToolExecutionResponse, error)
	Calls       []ToolExecutionRequest
}

// ExecuteTool records the call and delegates to ExecuteFunc if configured.
func (m *MockToolGateway) ExecuteTool(ctx context.Context, req ToolExecutionRequest) (ToolExecutionResponse, error) {
	if m == nil {
		return ToolExecutionResponse{}, ErrNilToolGateway
	}
	m.Calls = append(m.Calls, req)
	if m.ExecuteFunc != nil {
		return m.ExecuteFunc(ctx, req)
	}
	return ToolExecutionResponse{
		Allowed: true,
		Result:  map[string]any{"status": "ok"},
		Output:  `{"status":"ok"}`,
	}, nil
}
