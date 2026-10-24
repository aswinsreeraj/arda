package execution

import (
	"fmt"
	"strings"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
)

// SubmitExecutionRequest defines the parameters for initiating an agent execution.
type SubmitExecutionRequest struct {
	AgentID        uuid.UUID            `json:"agent_id"`
	AgentVersionID uuid.UUID            `json:"agent_version_id"`
	VersionID      uuid.UUID            `json:"version_id,omitempty"` // Alias for AgentVersionID
	Input          map[string]any       `json:"input,omitempty"`
	ExecutionMode  domain.ExecutionMode `json:"execution_mode,omitempty"`
	Mode           string               `json:"mode,omitempty"`         // "async" or "sync" (defaults to "async")
	ExecutionID    string               `json:"execution_id,omitempty"` // Optional idempotency or tracking key
}

// Validate ensures that all required identifiers and execution mode parameters are valid.
func (r *SubmitExecutionRequest) Validate() error {
	if r.AgentVersionID == uuid.Nil && r.VersionID != uuid.Nil {
		r.AgentVersionID = r.VersionID
	}

	if r.AgentID == uuid.Nil {
		return NewValidationError("agent_id is required and must be a valid UUID")
	}
	if r.AgentVersionID == uuid.Nil {
		return NewValidationError("agent_version_id is required and must be a valid UUID")
	}

	if r.Mode != "" {
		normalizedMode := strings.ToLower(strings.TrimSpace(r.Mode))
		if normalizedMode == "sync" {
			return NewValidationError("synchronous execution mode is not supported; use 'async'")
		}
		if normalizedMode != "async" {
			return NewValidationError(fmt.Sprintf("unsupported mode: %q; supported modes are: async", r.Mode))
		}
		r.Mode = normalizedMode
	} else {
		r.Mode = "async"
	}

	if r.ExecutionMode != "" {
		normalizedMode := domain.ExecutionMode(strings.ToLower(strings.TrimSpace(string(r.ExecutionMode))))
		if _, ok := domain.SupportedExecutionModes[normalizedMode]; !ok {
			return NewValidationError(fmt.Sprintf("unsupported execution_mode: %q; supported modes are: autonomous, supervised, human_in_the_loop, gated", r.ExecutionMode))
		}
		r.ExecutionMode = normalizedMode
	}

	return nil
}

// SubmitExecutionResponse represents the response payload for an accepted execution submission.
type SubmitExecutionResponse struct {
	WorkflowID     string               `json:"workflow_id"`
	RunID          string               `json:"run_id"`
	AgentID        uuid.UUID            `json:"agent_id"`
	AgentVersionID uuid.UUID            `json:"agent_version_id"`
	Status         string               `json:"status"` // "ACCEPTED"
	ExecutionMode  domain.ExecutionMode `json:"execution_mode,omitempty"`
	Mode           string               `json:"mode,omitempty"` // "async"
}

// ErrorCode defines standardized error codes for the execution service.
type ErrorCode string

const (
	CodeValidationFailed    ErrorCode = "VALIDATION_FAILED"
	CodeUnauthenticated     ErrorCode = "UNAUTHENTICATED"
	CodeForbidden           ErrorCode = "FORBIDDEN"
	CodeNotFound            ErrorCode = "NOT_FOUND"
	CodeConflict            ErrorCode = "CONFLICT"
	CodeUpstreamError       ErrorCode = "UPSTREAM_ERROR"
	CodeUpstreamUnavailable ErrorCode = "UPSTREAM_UNAVAILABLE"
	CodeInternalError       ErrorCode = "INTERNAL_ERROR"
)

// ExecutionError represents a structured error returned by the execution service.
type ExecutionError struct {
	Code       ErrorCode `json:"code"`
	Message    string    `json:"message"`
	HTTPStatus int       `json:"-"`
	Err        error     `json:"-"`
}

func (e *ExecutionError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *ExecutionError) Unwrap() error {
	return e.Err
}

// NewValidationError constructs an error indicating invalid or malformed request parameters.
func NewValidationError(message string) *ExecutionError {
	return &ExecutionError{
		Code:       CodeValidationFailed,
		Message:    message,
		HTTPStatus: 400,
	}
}

// NewUnauthenticatedError constructs an error indicating missing or invalid authentication credentials.
func NewUnauthenticatedError(message string) *ExecutionError {
	return &ExecutionError{
		Code:       CodeUnauthenticated,
		Message:    message,
		HTTPStatus: 401,
	}
}

// NewForbiddenError constructs an error indicating that the authenticated identity lacks permission.
func NewForbiddenError(message string) *ExecutionError {
	return &ExecutionError{
		Code:       CodeForbidden,
		Message:    message,
		HTTPStatus: 403,
	}
}

// NewNotFoundError constructs an error indicating a missing agent, version, or tenant resource.
func NewNotFoundError(message string) *ExecutionError {
	return &ExecutionError{
		Code:       CodeNotFound,
		Message:    message,
		HTTPStatus: 404,
	}
}

// NewConflictError constructs an error indicating duplicate execution or conflicting state.
func NewConflictError(message string, err error) *ExecutionError {
	return &ExecutionError{
		Code:       CodeConflict,
		Message:    message,
		HTTPStatus: 409,
		Err:        err,
	}
}

// NewUpstreamError constructs an error indicating a failure communicating with an upstream dependency (e.g. Temporal, Registry).
func NewUpstreamError(message string, err error) *ExecutionError {
	return &ExecutionError{
		Code:       CodeUpstreamError,
		Message:    message,
		HTTPStatus: 502,
		Err:        err,
	}
}

// NewUpstreamUnavailableError constructs an error indicating an upstream dependency is temporarily unavailable.
func NewUpstreamUnavailableError(message string, err error) *ExecutionError {
	return &ExecutionError{
		Code:       CodeUpstreamUnavailable,
		Message:    message,
		HTTPStatus: 503,
		Err:        err,
	}
}

// NewInternalError constructs an unexpected internal server error.
func NewInternalError(message string, err error) *ExecutionError {
	return &ExecutionError{
		Code:       CodeInternalError,
		Message:    message,
		HTTPStatus: 500,
		Err:        err,
	}
}
