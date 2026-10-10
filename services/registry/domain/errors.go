package domain

import (
	"fmt"
)

// ErrorCode is a machine-readable error classification.
type ErrorCode string

const (
	CodeNotFound           ErrorCode = "NOT_FOUND"
	CodeValidationFailed   ErrorCode = "VALIDATION_FAILED"
	CodeVersionConflict    ErrorCode = "VERSION_CONFLICT"
	CodeAlreadyPublished   ErrorCode = "ALREADY_PUBLISHED"
	CodePublishedImmutable ErrorCode = "PUBLISHED_VERSION_IMMUTABLE"
	CodeUnauthenticated    ErrorCode = "UNAUTHENTICATED"
	CodeInternalError      ErrorCode = "INTERNAL_ERROR"
)

// DomainError represents a structured domain error with error code and optional details.
type DomainError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Details []string  `json:"details,omitempty"`
	Err     error     `json:"-"`
}

func (e *DomainError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *DomainError) Unwrap() error {
	return e.Err
}

// Helper constructors for domain errors
func NewNotFoundError(message string) *DomainError {
	return &DomainError{
		Code:    CodeNotFound,
		Message: message,
	}
}

func NewValidationError(message string, details ...string) *DomainError {
	return &DomainError{
		Code:    CodeValidationFailed,
		Message: message,
		Details: details,
	}
}

func NewConflictError(code ErrorCode, message string) *DomainError {
	return &DomainError{
		Code:    code,
		Message: message,
	}
}

func NewInternalError(message string, err error) *DomainError {
	return &DomainError{
		Code:    CodeInternalError,
		Message: message,
		Err:     err,
	}
}

func NewUnauthenticatedError(message string) *DomainError {
	return &DomainError{
		Code:    CodeUnauthenticated,
		Message: message,
	}
}
