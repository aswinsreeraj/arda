package agentruntime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/google/uuid"
)

func TestVaultCredentialResolver_Success_ExplicitRef(t *testing.T) {
	memStore := vault.NewMemoryStore()
	tenantID := uuid.New()
	apiKey := "test-secret-openai-key-12345"

	// Write secret to Vault under tenant namespace
	err := memStore.WriteSecret(context.Background(), tenantID, "custom/openai", map[string]string{
		"api_key": apiKey,
	})
	if err != nil {
		t.Fatalf("failed to write secret to vault: %v", err)
	}

	resolver, err := agentruntime.NewVaultCredentialResolver(memStore)
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}

	resolved, err := resolver.ResolveCredential(context.Background(), tenantID, "openai", "secret:custom/openai#api_key")
	if err != nil {
		t.Fatalf("unexpected resolution error: %v", err)
	}
	if resolved != apiKey {
		t.Fatalf("expected resolved key %q, got %q", apiKey, resolved)
	}
}

func TestVaultCredentialResolver_Success_TenantDefault(t *testing.T) {
	memStore := vault.NewMemoryStore()
	tenantID := uuid.New()
	apiKey := "test-default-openai-key"

	// Write secret under tenant default path: credentials/llm/openai with field api_key
	err := memStore.WriteSecret(context.Background(), tenantID, "credentials/llm/openai", map[string]string{
		"api_key": apiKey,
	})
	if err != nil {
		t.Fatalf("failed to write default secret: %v", err)
	}

	resolver, err := agentruntime.NewVaultCredentialResolver(memStore)
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}

	// Empty credentialRef should fall back to tenant default
	resolved, err := resolver.ResolveCredential(context.Background(), tenantID, "openai", "")
	if err != nil {
		t.Fatalf("unexpected resolution error: %v", err)
	}
	if resolved != apiKey {
		t.Fatalf("expected %q, got %q", apiKey, resolved)
	}
}

func TestVaultCredentialResolver_TenantIsolation(t *testing.T) {
	memStore := vault.NewMemoryStore()
	tenantA := uuid.New()
	tenantB := uuid.New()

	apiKeyA := "tenant-a-secret-key"
	_ = memStore.WriteSecret(context.Background(), tenantA, "credentials/llm/openai", map[string]string{
		"api_key": apiKeyA,
	})

	resolver, err := agentruntime.NewVaultCredentialResolver(memStore)
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}

	// Tenant B tries to resolve credentials/llm/openai without having configured it
	_, err = resolver.ResolveCredential(context.Background(), tenantB, "openai", "")
	if err == nil {
		t.Fatal("expected error resolving credential for Tenant B, got nil")
	}
	if !errors.Is(err, agentruntime.ErrCredentialNotFound) {
		t.Fatalf("expected ErrCredentialNotFound, got %v", err)
	}

	// Tenant B tries path traversal to access Tenant A's namespace
	maliciousRef := "secret:../" + tenantA.String() + "/credentials/llm/openai#api_key"
	_, err = resolver.ResolveCredential(context.Background(), tenantB, "openai", maliciousRef)
	if err == nil {
		t.Fatal("expected path traversal reference to be rejected, got nil")
	}
	if !errors.Is(err, agentruntime.ErrInvalidConfiguration) && !errors.Is(err, agentruntime.ErrPermanent) {
		t.Fatalf("expected permanent/invalid configuration error, got %v", err)
	}
}

func TestVaultCredentialResolver_MissingAndEmptyCredential(t *testing.T) {
	memStore := vault.NewMemoryStore()
	tenantID := uuid.New()

	resolver, err := agentruntime.NewVaultCredentialResolver(memStore)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Missing credential
	_, err = resolver.ResolveCredential(context.Background(), tenantID, "anthropic", "")
	if err == nil || !errors.Is(err, agentruntime.ErrCredentialNotFound) {
		t.Fatalf("expected ErrCredentialNotFound, got %v", err)
	}

	// Empty string value in Vault
	_ = memStore.WriteSecret(context.Background(), tenantID, "credentials/llm/anthropic", map[string]string{
		"api_key": "   ",
	})
	_, err = resolver.ResolveCredential(context.Background(), tenantID, "anthropic", "")
	if err == nil || !errors.Is(err, agentruntime.ErrEmptyCredential) {
		t.Fatalf("expected ErrEmptyCredential, got %v", err)
	}
}

func TestVaultCredentialResolver_InvalidTenantAndProvider(t *testing.T) {
	memStore := vault.NewMemoryStore()
	resolver, _ := agentruntime.NewVaultCredentialResolver(memStore)

	// Nil tenant
	_, err := resolver.ResolveCredential(context.Background(), uuid.Nil, "openai", "")
	if err == nil || !errors.Is(err, agentruntime.ErrInvalidTenant) {
		t.Fatalf("expected ErrInvalidTenant, got %v", err)
	}

	// Empty provider
	_, err = resolver.ResolveCredential(context.Background(), uuid.New(), "", "")
	if err == nil || !errors.Is(err, agentruntime.ErrMissingProvider) {
		t.Fatalf("expected ErrMissingProvider, got %v", err)
	}

	// Provider with illegal URL characters
	_, err = resolver.ResolveCredential(context.Background(), uuid.New(), "https://evil.com/api", "")
	if err == nil || !errors.Is(err, agentruntime.ErrUnsupportedProvider) {
		t.Fatalf("expected ErrUnsupportedProvider, got %v", err)
	}
}

func TestVaultCredentialResolver_RotationAndRevocation(t *testing.T) {
	memStore := vault.NewMemoryStore()
	tenantID := uuid.New()
	resolver, _ := agentruntime.NewVaultCredentialResolver(memStore)

	// 1. Initial key
	_ = memStore.WriteSecret(context.Background(), tenantID, "credentials/llm/openai", map[string]string{
		"api_key": "initial-key-v1",
	})
	res1, err := resolver.ResolveCredential(context.Background(), tenantID, "openai", "")
	if err != nil || res1 != "initial-key-v1" {
		t.Fatalf("expected initial-key-v1, got %q (err: %v)", res1, err)
	}

	// 2. Rotate key
	_ = memStore.WriteSecret(context.Background(), tenantID, "credentials/llm/openai", map[string]string{
		"api_key": "rotated-key-v2",
	})
	res2, err := resolver.ResolveCredential(context.Background(), tenantID, "openai", "")
	if err != nil || res2 != "rotated-key-v2" {
		t.Fatalf("expected rotated-key-v2, got %q (err: %v)", res2, err)
	}

	// 3. Revoke/delete key
	_ = memStore.DeleteSecret(context.Background(), tenantID, "credentials/llm/openai")
	_, err = resolver.ResolveCredential(context.Background(), tenantID, "openai", "")
	if err == nil || !errors.Is(err, agentruntime.ErrCredentialNotFound) {
		t.Fatalf("expected ErrCredentialNotFound after revocation, got %v", err)
	}
}

func TestVaultCredentialResolver_Redaction(t *testing.T) {
	memStore := vault.NewMemoryStore()
	tenantID := uuid.New()
	resolver, _ := agentruntime.NewVaultCredentialResolver(memStore)

	_, err := resolver.ResolveCredential(context.Background(), tenantID, "nonexistent", "")
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "sk-") {
			t.Fatalf("error message contains raw secret token: %s", errStr)
		}
	}
}

func TestVaultCredentialResolver_ContextCancellation(t *testing.T) {
	memStore := vault.NewMemoryStore()
	resolver, _ := agentruntime.NewVaultCredentialResolver(memStore)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := resolver.ResolveCredential(ctx, uuid.New(), "openai", "")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestMockCredentialResolver(t *testing.T) {
	mock := agentruntime.NewMockCredentialResolver()
	tenantID := uuid.New()

	mock.SetDefaultCredential(tenantID, "openai", "mock-openai-key")
	mock.SetCredential(tenantID, "anthropic", "secret:custom/anthropic#key", "mock-anthropic-key")

	res1, err := mock.ResolveCredential(context.Background(), tenantID, "openai", "")
	if err != nil || res1 != "mock-openai-key" {
		t.Fatalf("expected mock-openai-key, got %q (err: %v)", res1, err)
	}

	res2, err := mock.ResolveCredential(context.Background(), tenantID, "anthropic", "secret:custom/anthropic#key")
	if err != nil || res2 != "mock-anthropic-key" {
		t.Fatalf("expected mock-anthropic-key, got %q (err: %v)", res2, err)
	}

	if mock.Calls() != 2 {
		t.Fatalf("expected 2 calls, got %d", mock.Calls())
	}

	// Test error simulation
	simErr := errors.New("simulated resolver failure")
	mock.SetError(simErr)
	_, err = mock.ResolveCredential(context.Background(), tenantID, "openai", "")
	if !errors.Is(err, simErr) {
		t.Fatalf("expected simulated error, got %v", err)
	}
}
