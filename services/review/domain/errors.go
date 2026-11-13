package domain

import (
	"errors"
	"fmt"
)

// DomainErrorCode defines standard machine-readable error codes.
type DomainErrorCode string

const (
	CodeValidationFailed DomainErrorCode = "VALIDATION_FAILED"
	CodeUnauthenticated  DomainErrorCode = "UNAUTHENTICATED"
	CodeForbidden        DomainErrorCode = "FORBIDDEN"
	CodeNotFound         DomainErrorCode = "NOT_FOUND"
	CodeConflict         DomainErrorCode = "CONFLICT"
	CodeInternalError    DomainErrorCode = "INTERNAL_ERROR"
)

var (
	ErrCaseNotFound        = errors.New("review case not found")
	ErrCaseAlreadyResolved = errors.New("review case is already resolved")
	ErrTenantMismatch      = errors.New("tenant mismatch: unauthorized tenant access")
	ErrInvalidDecision     = errors.New("invalid decision: must be 'approve', 'reject', or 'override'")
	ErrMissingReason       = errors.New("resolution reason is mandatory")
	ErrInvalidOverride     = errors.New("final_decision is required when decision is 'override'")
	ErrUnauthorized        = errors.New("unauthorized reviewer identity")
)

// ReviewError represents a structured domain error for review operations.
type ReviewError struct {
	Code       DomainErrorCode `json:"code"`
	Message    string          `json:"message"`
	HTTPStatus int             `json:"-"`
	Err        error           `json:"-"`
}

func (e *ReviewError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *ReviewError) Unwrap() error {
	return e.Err
}

// NewValidationError creates a VALIDATION_FAILED domain error (HTTP 400).
func NewValidationError(message string) *ReviewError {
	return &ReviewError{
		Code:       CodeValidationFailed,
		Message:    message,
		HTTPStatus: 400,
	}
}

// NewNotFoundError creates a NOT_FOUND domain error (HTTP 404).
func NewNotFoundError(message string) *ReviewError {
	return &ReviewError{
		Code:       CodeNotFound,
		Message:    message,
		HTTPStatus: 404,
		Err:        ErrCaseNotFound,
	}
}

// NewConflictError creates a CONFLICT domain error (HTTP 409).
func NewConflictError(message string, err error) *ReviewError {
	return &ReviewError{
		Code:       CodeConflict,
		Message:    message,
		HTTPStatus: 409,
		Err:        err,
	}
}

// NewForbiddenError creates a FORBIDDEN domain error (HTTP 403).
func NewForbiddenError(message string) *ReviewError {
	return &ReviewError{
		Code:       CodeForbidden,
		Message:    message,
		HTTPStatus: 403,
	}
}

// NewUnauthenticatedError creates an UNAUTHENTICATED domain error (HTTP 401).
func NewUnauthenticatedError(message string) *ReviewError {
	return &ReviewError{
		Code:       CodeUnauthenticated,
		Message:    message,
		HTTPStatus: 401,
	}
}
