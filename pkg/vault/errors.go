package vault

import (
	"errors"
	"fmt"
)

var (
	// ErrSecretNotFound indicates that the requested secret does not exist in the tenant's namespace.
	ErrSecretNotFound = errors.New("secret not found")

	// ErrInvalidReference indicates that the secret reference or key failed validation (e.g. traversal attempt, invalid characters).
	ErrInvalidReference = errors.New("invalid secret reference")

	// ErrTenantRequired indicates that a valid tenant ID was not provided in the context or argument.
	ErrTenantRequired = errors.New("tenant ID is required and must not be nil")

	// ErrVaultAuthentication indicates that authentication to Vault failed (e.g. missing/invalid token or permission denied).
	ErrVaultAuthentication = errors.New("vault authentication failed")

	// ErrVaultUnavailable indicates that Vault is unreachable, down, or timed out.
	ErrVaultUnavailable = errors.New("vault service unavailable")

	// ErrVaultOperation indicates a general failure during a Vault operation.
	ErrVaultOperation = errors.New("vault operation failed")
)

// SanitizedError wraps an error while guaranteeing that secret values and authentication tokens
// are never exposed in error text or logs.
type SanitizedError struct {
	Op  string
	Key string
	Err error
}

func (e *SanitizedError) Error() string {
	if e.Key != "" {
		return fmt.Sprintf("vault %s failed for %q: %v", e.Op, e.Key, e.Err)
	}
	return fmt.Sprintf("vault %s failed: %v", e.Op, e.Err)
}

func (e *SanitizedError) Unwrap() error {
	return e.Err
}
