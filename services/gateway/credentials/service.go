package credentials

import (
	"context"
	"fmt"
	"strings"

	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/google/uuid"
)

// Service coordinates credential management between HashiCorp Vault and metadata storage.
type Service struct {
	vaultStore vault.SecretStore
	store      Store
}

// NewService creates a new Service instance.
func NewService(vaultStore vault.SecretStore, store Store) *Service {
	return &Service{
		vaultStore: vaultStore,
		store:      store,
	}
}

func credentialKeyAndField(category Category, provider string) (string, string) {
	cleanProvider := strings.ToLower(strings.TrimSpace(provider))
	key := fmt.Sprintf("credentials/%s/%s", category, cleanProvider)
	field := "api_key"
	if category == CategoryMCP {
		field = "access_token"
	}
	return key, field
}

// SaveCredential securely writes a mock secret to Vault and saves non-sensitive metadata.
// Raw secrets are NEVER persisted in PostgreSQL or returned in the response.
func (s *Service) SaveCredential(ctx context.Context, tenantID uuid.UUID, req SaveCredentialRequest) (*Credential, error) {
	if tenantID == uuid.Nil {
		return nil, errorsNew("missing or invalid tenant identity")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}

	cleanProvider := strings.ToLower(strings.TrimSpace(req.Provider))
	key, field := credentialKeyAndField(req.Category, cleanProvider)

	// Write secret to tenant-scoped Vault KV v2 storage
	if err := s.vaultStore.WriteSecret(ctx, tenantID, key, map[string]string{
		field: req.SecretValue,
	}); err != nil {
		return nil, fmt.Errorf("failed to write secret to vault: %w", err)
	}

	reference := vault.FormatReferenceWithField(key, field)

	cred := &Credential{
		ID:        uuid.New(),
		TenantID:  tenantID,
		Category:  req.Category,
		Provider:  cleanProvider,
		Reference: reference,
	}

	if err := s.store.Save(ctx, cred); err != nil {
		return nil, fmt.Errorf("failed to save credential metadata: %w", err)
	}

	return cred, nil
}

// GetCredential retrieves credential metadata scoped strictly to the tenant.
func (s *Service) GetCredential(ctx context.Context, tenantID, id uuid.UUID) (*Credential, error) {
	if tenantID == uuid.Nil {
		return nil, errorsNew("missing or invalid tenant identity")
	}
	if id == uuid.Nil {
		return nil, errorsNew("invalid credential id")
	}
	return s.store.Get(ctx, tenantID, id)
}

// ListCredentials lists all credential metadata for the tenant.
func (s *Service) ListCredentials(ctx context.Context, tenantID uuid.UUID) ([]Credential, error) {
	if tenantID == uuid.Nil {
		return nil, errorsNew("missing or invalid tenant identity")
	}
	return s.store.List(ctx, tenantID)
}

// DeleteCredential permanently removes the secret and all historical versions from Vault and deletes the metadata.
func (s *Service) DeleteCredential(ctx context.Context, tenantID, id uuid.UUID) error {
	if tenantID == uuid.Nil {
		return errorsNew("missing or invalid tenant identity")
	}
	if id == uuid.Nil {
		return errorsNew("invalid credential id")
	}

	cred, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return err
	}

	key, _ := credentialKeyAndField(cred.Category, cred.Provider)
	_ = s.vaultStore.DestroySecret(ctx, tenantID, key)

	return s.store.Delete(ctx, tenantID, id)
}

// VerifyCredential verifies that the secret referenced by the credential can be read from Vault.
// Does NOT return the secret value.
func (s *Service) VerifyCredential(ctx context.Context, tenantID, id uuid.UUID) (*VerifyCredentialResponse, error) {
	if tenantID == uuid.Nil {
		return nil, errorsNew("missing or invalid tenant identity")
	}
	if id == uuid.Nil {
		return nil, errorsNew("invalid credential id")
	}

	cred, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}

	// Verify retrieval through Vault client; discard secret value immediately
	val, err := s.vaultStore.ReadSecretValue(ctx, tenantID, cred.Reference)
	if err != nil {
		return nil, fmt.Errorf("vault verification failed: %w", err)
	}
	if val == "" {
		return nil, fmt.Errorf("secret reference %s resolved to empty value", cred.Reference)
	}

	return &VerifyCredentialResponse{
		ID:        cred.ID,
		Verified:  true,
		Reference: cred.Reference,
		Message:   "secret successfully read from vault",
	}, nil
}

func errorsNew(msg string) error {
	return fmt.Errorf("%s", msg)
}
