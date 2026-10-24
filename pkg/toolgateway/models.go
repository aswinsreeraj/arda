package toolgateway

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ToolClassification categorizes tools into safe read-only or state-mutating operations.
type ToolClassification string

const (
	ClassificationRead  ToolClassification = "read"
	ClassificationWrite ToolClassification = "write"
)

// InvocationStatus tracks the lifecycle of an invocation for idempotency and write safety.
type InvocationStatus string

const (
	StatusPending   InvocationStatus = "PENDING"
	StatusCompleted InvocationStatus = "COMPLETED"
	StatusFailed    InvocationStatus = "FAILED"
)

var (
	ErrToolNotFound           = errors.New("tool definition not found")
	ErrToolDisabled           = errors.New("tool is disabled")
	ErrInvalidToolDefinition  = errors.New("invalid tool definition")
	ErrGuardrailDenied        = errors.New("guardrail blocked tool execution")
	ErrIdempotencyKeyMismatch = errors.New("idempotency key reused with different arguments")
	ErrInvocationInProgress   = errors.New("tool invocation currently in progress")
	ErrUnauthorizedTool       = errors.New("unauthorized tool execution")
	ErrAgentToolNotAllowed    = errors.New("tool not allowed by agent configuration")
	ErrOversizedResult        = errors.New("tool result exceeds maximum size of 64KB")
	ErrHandlerNotFound        = errors.New("tool execution handler not found")
)

// Standard Denial Codes
const (
	DenialCodeUnauthenticated        = "UNAUTHENTICATED"
	DenialCodeUnauthorized           = "UNAUTHORIZED"
	DenialCodeToolNotFound           = "TOOL_NOT_FOUND"
	DenialCodeToolDisabled           = "TOOL_DISABLED"
	DenialCodeAgentNotAllowed        = "AGENT_NOT_ALLOWED"
	DenialCodeSchemaValidationFailed = "SCHEMA_VALIDATION_FAILED"
	DenialCodeGuardrailDenied        = "GUARDRAIL_DENIED"
	DenialCodeIdempotencyConflict    = "IDEMPOTENCY_CONFLICT"
	DenialCodeInvocationInProgress   = "INVOCATION_IN_PROGRESS"
	DenialCodeResultTooLarge         = "RESULT_TOO_LARGE"
	DenialCodeExecutionFailed        = "EXECUTION_FAILED"
)

// ToolDefinition represents a registered tool contract in the catalog.
type ToolDefinition struct {
	ID                  string             `json:"id" gorm:"primaryKey"`
	TenantID            uuid.UUID          `json:"tenant_id" gorm:"type:uuid;index;not null"`
	Name                string             `json:"name" gorm:"index;not null"`
	Description         string             `json:"description"`
	Classification      ToolClassification `json:"classification" gorm:"not null"`
	InputSchema         map[string]any     `json:"input_schema" gorm:"serializer:json"`
	Version             int                `json:"version" gorm:"not null;default:1"`
	Enabled             bool               `json:"enabled" gorm:"not null;default:true"`
	RequiredPermissions []string           `json:"required_permissions" gorm:"serializer:json"`
	HandlerID           string             `json:"handler_id" gorm:"not null"`
	CreatedAt           time.Time          `json:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
}

// Validate ensures that required fields of a tool definition are well-formed.
func (t ToolDefinition) Validate() error {
	if t.TenantID == uuid.Nil {
		return fmt.Errorf("%w: tenant_id is required", ErrInvalidToolDefinition)
	}
	if t.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidToolDefinition)
	}
	if t.Classification != ClassificationRead && t.Classification != ClassificationWrite {
		return fmt.Errorf("%w: classification must be 'read' or 'write'", ErrInvalidToolDefinition)
	}
	if t.InputSchema == nil {
		return fmt.Errorf("%w: input_schema is required", ErrInvalidToolDefinition)
	}
	if t.HandlerID == "" {
		return fmt.Errorf("%w: handler_id is required", ErrInvalidToolDefinition)
	}
	return nil
}

// ToolInvocation persists invocation state for idempotency and write deduplication.
type ToolInvocation struct {
	InvocationID   string           `json:"invocation_id" gorm:"primaryKey"`
	TenantID       uuid.UUID        `json:"tenant_id" gorm:"type:uuid;index;not null"`
	AgentID        uuid.UUID        `json:"agent_id" gorm:"type:uuid;index;not null"`
	AgentVersionID uuid.UUID        `json:"agent_version_id" gorm:"type:uuid;not null"`
	ExecutionID    string           `json:"execution_id" gorm:"index;not null"`
	ToolID         string           `json:"tool_id" gorm:"index;not null"`
	ToolName       string           `json:"tool_name" gorm:"not null"`
	ArgumentsHash  string           `json:"arguments_hash" gorm:"not null"`
	Status         InvocationStatus `json:"status" gorm:"not null"`
	Result         map[string]any   `json:"result,omitempty" gorm:"serializer:json"`
	Output         string           `json:"output,omitempty" gorm:"type:text"`
	Error          string           `json:"error,omitempty"`
	DenialCode     string           `json:"denial_code,omitempty"`
	DurationMs     int64            `json:"duration_ms"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// AuditEvent represents a structured, sanitized governance record of a tool invocation.
type AuditEvent struct {
	EventID            string             `json:"event_id" gorm:"primaryKey"`
	TenantID           uuid.UUID          `json:"tenant_id" gorm:"type:uuid;index;not null"`
	ExecutionID        string             `json:"execution_id" gorm:"index;not null"`
	AgentID            uuid.UUID          `json:"agent_id" gorm:"type:uuid;index;not null"`
	AgentVersionID     uuid.UUID          `json:"agent_version_id" gorm:"type:uuid;not null"`
	ToolID             string             `json:"tool_id" gorm:"index;not null"`
	ToolName           string             `json:"tool_name" gorm:"not null"`
	ToolVersion        int                `json:"tool_version" gorm:"not null"`
	InvocationID       string             `json:"invocation_id" gorm:"index;not null"`
	Classification     ToolClassification `json:"classification" gorm:"not null"`
	Allowed            bool               `json:"allowed" gorm:"not null"`
	DenialCode         string             `json:"denial_code,omitempty"`
	DenialReason       string             `json:"denial_reason,omitempty"`
	FailureCategory    string             `json:"failure_category,omitempty"`
	SanitizedArguments map[string]any     `json:"sanitized_arguments,omitempty" gorm:"serializer:json"`
	DurationMs         int64              `json:"duration_ms" gorm:"not null"`
	Timestamp          time.Time          `json:"timestamp" gorm:"not null"`
}
