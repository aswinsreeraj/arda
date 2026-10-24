package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/google/uuid"
)

type authContextKey string

const (
	apiKeyCtxKey authContextKey = "arda_agentruntime_api_key"
)

// WithAPIKey injects a tenant's resolved LLM API key into the context.
// Secrets injected here must only be read in-memory by provider adapters and never logged or serialized.
func WithAPIKey(ctx context.Context, apiKey string) context.Context {
	return context.WithValue(ctx, apiKeyCtxKey, apiKey)
}

// APIKeyFromContext retrieves the in-memory resolved API key from context.
func APIKeyFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	val, ok := ctx.Value(apiKeyCtxKey).(string)
	return val, ok && strings.TrimSpace(val) != ""
}

// CredentialResolver defines the contract for resolving a tenant-owned provider credential.
// Implementations MUST strictly isolate resolution within the authenticated tenant's namespace,
// reject cross-tenant references, and redact all secrets from errors, traces, and logs.
type CredentialResolver interface {
	ResolveCredential(ctx context.Context, tenantID uuid.UUID, provider string, credentialRef string) (string, error)
}

// VaultCredentialResolver resolves tenant BYOK provider credentials from HashiCorp Vault KV v2.
type VaultCredentialResolver struct {
	store vault.SecretStore
}

// NewVaultCredentialResolver initializes a VaultCredentialResolver with an explicit SecretStore.
func NewVaultCredentialResolver(store vault.SecretStore) (*VaultCredentialResolver, error) {
	if store == nil {
		return nil, errors.New("vault credential resolver: secret store cannot be nil")
	}
	return &VaultCredentialResolver{store: store}, nil
}

// ResolveCredential resolves a provider credential for an authenticated tenant.
// Precedence:
//  1. Explicit agent setting: If credentialRef is non-empty, resolves that reference within the tenant's namespace.
//  2. Authorized tenant default: If credentialRef is empty, resolves the tenant's default provider credential at
//     "secret:credentials/llm/<provider>#api_key".
//
// Fails closed if tenantID is nil, provider is empty, or the credential cannot be resolved.
func (r *VaultCredentialResolver) ResolveCredential(ctx context.Context, tenantID uuid.UUID, provider string, credentialRef string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if tenantID == uuid.Nil {
		return "", fmt.Errorf("%w: tenant ID is required", ErrInvalidTenant)
	}

	cleanProvider := strings.ToLower(strings.TrimSpace(provider))
	if cleanProvider == "" {
		return "", fmt.Errorf("%w: provider identifier cannot be empty", ErrMissingProvider)
	}
	if strings.ContainsAny(cleanProvider, "/\\#?&% :") {
		return "", fmt.Errorf("%w: provider %q contains invalid characters", ErrUnsupportedProvider, cleanProvider)
	}

	var targetRef string
	trimmedRef := strings.TrimSpace(credentialRef)
	if trimmedRef != "" {
		// Explicit agent setting
		parsed, err := vault.ParseReference(trimmedRef)
		if err != nil {
			return "", fmt.Errorf("%w: invalid credential reference %q: %v", ErrInvalidConfiguration, trimmedRef, err)
		}
		targetRef = parsed.Raw
	} else {
		// Authorized tenant default convention (matching services/gateway/credentials)
		targetRef = vault.FormatReferenceWithField(fmt.Sprintf("credentials/llm/%s", cleanProvider), "api_key")
	}

	val, err := r.store.ReadSecretValue(ctx, tenantID, targetRef)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return "", context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", context.DeadlineExceeded
		}
		if errors.Is(err, vault.ErrSecretNotFound) {
			return "", fmt.Errorf("%w: no credential found for provider %q in tenant namespace (ref: %s)", ErrCredentialNotFound, cleanProvider, targetRef)
		}
		if errors.Is(err, vault.ErrInvalidReference) {
			return "", fmt.Errorf("%w: invalid secret reference %s", ErrPermanent, targetRef)
		}
		if errors.Is(err, vault.ErrVaultAuthentication) {
			return "", fmt.Errorf("%w: vault authentication error during credential resolution", ErrPermanent)
		}
		if errors.Is(err, vault.ErrVaultUnavailable) {
			return "", fmt.Errorf("%w: vault service unavailable during credential resolution", ErrTransient)
		}
		return "", fmt.Errorf("%w: failed to read secret from vault", ErrPermanent)
	}

	secretVal := strings.TrimSpace(val)
	if secretVal == "" {
		return "", fmt.Errorf("%w: resolved credential for provider %q (ref: %s) is empty", ErrEmptyCredential, cleanProvider, targetRef)
	}

	return secretVal, nil
}

// MockCredentialResolver is an in-memory, thread-safe test double for CredentialResolver.
type MockCredentialResolver struct {
	mu          sync.RWMutex
	credentials map[string]string // key: "tenantID:provider:ref" or "tenantID:provider:default"
	err         error
	calls       int
}

// NewMockCredentialResolver creates a new MockCredentialResolver instance.
func NewMockCredentialResolver() *MockCredentialResolver {
	return &MockCredentialResolver{
		credentials: make(map[string]string),
	}
}

// SetCredential registers a credential for a specific tenant, provider, and reference.
func (m *MockCredentialResolver) SetCredential(tenantID uuid.UUID, provider, ref, secretVal string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%s:%s:%s", tenantID, strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(ref))
	m.credentials[key] = secretVal
}

// SetDefaultCredential registers the tenant's default credential for a provider.
func (m *MockCredentialResolver) SetDefaultCredential(tenantID uuid.UUID, provider, secretVal string) {
	defaultRef := vault.FormatReferenceWithField(fmt.Sprintf("credentials/llm/%s", strings.ToLower(strings.TrimSpace(provider))), "api_key")
	m.SetCredential(tenantID, provider, defaultRef, secretVal)
	// Also store under empty ref key for convenience
	m.SetCredential(tenantID, provider, "", secretVal)
}

// SetError configures a simulated resolution error.
func (m *MockCredentialResolver) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// Calls returns the count of ResolveCredential invocations.
func (m *MockCredentialResolver) Calls() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.calls
}

// ResolveCredential retrieves the registered mock credential or returns an error.
func (m *MockCredentialResolver) ResolveCredential(ctx context.Context, tenantID uuid.UUID, provider string, credentialRef string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	m.mu.Lock()
	m.calls++
	simErr := m.err
	m.mu.Unlock()

	if simErr != nil {
		return "", simErr
	}

	if tenantID == uuid.Nil {
		return "", fmt.Errorf("%w: tenant ID is required", ErrInvalidTenant)
	}

	cleanProvider := strings.ToLower(strings.TrimSpace(provider))
	if cleanProvider == "" {
		return "", fmt.Errorf("%w: provider is required", ErrMissingProvider)
	}

	cleanRef := strings.TrimSpace(credentialRef)
	if cleanRef == "" {
		cleanRef = vault.FormatReferenceWithField(fmt.Sprintf("credentials/llm/%s", cleanProvider), "api_key")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s:%s:%s", tenantID, cleanProvider, cleanRef)
	if val, ok := m.credentials[key]; ok {
		return val, nil
	}

	// Try empty ref fallback
	emptyKey := fmt.Sprintf("%s:%s:", tenantID, cleanProvider)
	if val, ok := m.credentials[emptyKey]; ok {
		return val, nil
	}

	return "", fmt.Errorf("%w: no mock credential for provider %q (ref: %s) in tenant %s", ErrCredentialNotFound, cleanProvider, cleanRef, tenantID)
}
