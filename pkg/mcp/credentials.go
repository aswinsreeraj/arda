package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/google/uuid"
)

// CredentialResolver resolves tenant-scoped credentials from HashiCorp Vault.
type CredentialResolver interface {
	Resolve(ctx context.Context, tenantID uuid.UUID, ref string) (string, error)
}

// VaultCredentialResolver resolves secrets via a vault.SecretStore.
type VaultCredentialResolver struct {
	store vault.SecretStore
}

// NewVaultCredentialResolver constructs a resolver backed by Vault.
func NewVaultCredentialResolver(store vault.SecretStore) (*VaultCredentialResolver, error) {
	if store == nil {
		return nil, errors.New("secret store cannot be nil")
	}
	return &VaultCredentialResolver{store: store}, nil
}

// Resolve resolves a tenant-scoped secret reference.
// It ensures that tenant boundaries are strictly respected and redacts any secret leakage from errors.
func (r *VaultCredentialResolver) Resolve(ctx context.Context, tenantID uuid.UUID, ref string) (string, error) {
	if tenantID == uuid.Nil {
		return "", errors.New("cannot resolve credential: nil tenant ID")
	}
	if strings.TrimSpace(ref) == "" {
		return "", nil // No credential required
	}

	secret, err := r.store.ReadSecretValue(ctx, tenantID, ref)
	if err != nil {
		// Redact raw reference in returned error to avoid leaking secret paths
		return "", fmt.Errorf("failed to resolve tenant credential from vault: %w", sanitizeVaultError(err))
	}

	return secret, nil
}

func sanitizeVaultError(err error) error {
	if errors.Is(err, vault.ErrSecretNotFound) {
		return vault.ErrSecretNotFound
	}
	if errors.Is(err, vault.ErrVaultAuthentication) {
		return vault.ErrVaultAuthentication
	}
	return errors.New("vault secret resolution error")
}
