package credentials_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/aswinsreeraj/arda/services/gateway/credentials"
	"github.com/google/uuid"
)

// mockVaultStore implements vault.SecretStore for in-memory unit testing.
type mockVaultStore struct {
	secrets map[string]map[string]string // key: "tenantID:path"
}

func newMockVaultStore() *mockVaultStore {
	return &mockVaultStore{
		secrets: make(map[string]map[string]string),
	}
}

func (m *mockVaultStore) makeKey(tenantID uuid.UUID, path string) string {
	return tenantID.String() + ":" + strings.Trim(path, "/")
}

func (m *mockVaultStore) WriteSecret(ctx context.Context, tenantID uuid.UUID, key string, data map[string]string) error {
	k := m.makeKey(tenantID, key)
	copied := make(map[string]string, len(data))
	for k, v := range data {
		copied[k] = v
	}
	m.secrets[k] = copied
	return nil
}

func (m *mockVaultStore) ReadSecret(ctx context.Context, tenantID uuid.UUID, ref string) (map[string]string, error) {
	parsed, err := vault.ParseReference(ref)
	if err != nil {
		return nil, err
	}
	k := m.makeKey(tenantID, parsed.Key)
	data, ok := m.secrets[k]
	if !ok {
		return nil, vault.ErrSecretNotFound
	}
	return data, nil
}

func (m *mockVaultStore) ReadSecretValue(ctx context.Context, tenantID uuid.UUID, ref string) (string, error) {
	parsed, err := vault.ParseReference(ref)
	if err != nil {
		return "", err
	}
	data, err := m.ReadSecret(ctx, tenantID, ref)
	if err != nil {
		return "", err
	}
	val, ok := data[parsed.Field]
	if !ok {
		return "", vault.ErrSecretNotFound
	}
	return val, nil
}

func (m *mockVaultStore) DeleteSecret(ctx context.Context, tenantID uuid.UUID, key string) error {
	k := m.makeKey(tenantID, key)
	delete(m.secrets, k)
	return nil
}

func setupTestService() (*credentials.Service, *mockVaultStore, credentials.Store) {
	vStore := newMockVaultStore()
	memStore := credentials.NewMemoryStore()
	svc := credentials.NewService(vStore, memStore)
	return svc, vStore, memStore
}

func TestSaveAndRetrieveLLMCredential(t *testing.T) {
	svc, vStore, _ := setupTestService()
	ctx := context.Background()
	tenantID := uuid.New()

	saveReq := credentials.SaveCredentialRequest{
		Category:    credentials.CategoryLLM,
		Provider:    "openai",
		SecretValue: "mock-llm-key-not-real-12345",
	}

	cred, err := svc.SaveCredential(ctx, tenantID, saveReq)
	if err != nil {
		t.Fatalf("unexpected error saving credential: %v", err)
	}

	if cred.Category != credentials.CategoryLLM {
		t.Errorf("expected category %s, got %s", credentials.CategoryLLM, cred.Category)
	}
	if cred.Provider != "openai" {
		t.Errorf("expected provider openai, got %s", cred.Provider)
	}
	if !strings.HasPrefix(cred.Reference, "secret:") {
		t.Errorf("expected reference to start with 'secret:', got %s", cred.Reference)
	}
	if !strings.Contains(cred.Reference, "credentials/llm/openai#api_key") {
		t.Errorf("expected reference to contain credentials/llm/openai#api_key, got %s", cred.Reference)
	}

	// Verify secret was written to Vault under the tenant's namespace
	val, err := vStore.ReadSecretValue(ctx, tenantID, cred.Reference)
	if err != nil {
		t.Fatalf("failed to read secret value from vault: %v", err)
	}
	if val != "mock-llm-key-not-real-12345" {
		t.Errorf("expected secret value 'mock-llm-key-not-real-12345', got %q", val)
	}

	// Retrieve credential metadata
	retrieved, err := svc.GetCredential(ctx, tenantID, cred.ID)
	if err != nil {
		t.Fatalf("failed to retrieve credential: %v", err)
	}
	if retrieved.ID != cred.ID {
		t.Errorf("expected ID %s, got %s", cred.ID, retrieved.ID)
	}
	if retrieved.Reference != cred.Reference {
		t.Errorf("expected reference %s, got %s", cred.Reference, retrieved.Reference)
	}
}

func TestSaveAndRetrieveMCPCredential(t *testing.T) {
	svc, vStore, _ := setupTestService()
	ctx := context.Background()
	tenantID := uuid.New()

	saveReq := credentials.SaveCredentialRequest{
		Category:    credentials.CategoryMCP,
		Provider:    "filesystem-server",
		SecretValue: "mock-mcp-token-not-real-67890",
	}

	cred, err := svc.SaveCredential(ctx, tenantID, saveReq)
	if err != nil {
		t.Fatalf("unexpected error saving credential: %v", err)
	}

	if cred.Category != credentials.CategoryMCP {
		t.Errorf("expected category %s, got %s", credentials.CategoryMCP, cred.Category)
	}
	if !strings.Contains(cred.Reference, "credentials/mcp/filesystem-server#access_token") {
		t.Errorf("expected reference to contain access_token, got %s", cred.Reference)
	}

	val, err := vStore.ReadSecretValue(ctx, tenantID, cred.Reference)
	if err != nil {
		t.Fatalf("failed to read secret value from vault: %v", err)
	}
	if val != "mock-mcp-token-not-real-67890" {
		t.Errorf("expected secret value 'mock-mcp-token-not-real-67890', got %q", val)
	}
}

func TestCrossTenantIsolation(t *testing.T) {
	svc, _, _ := setupTestService()
	ctx := context.Background()
	tenantA := uuid.New()
	tenantB := uuid.New()

	saveReq := credentials.SaveCredentialRequest{
		Category:    credentials.CategoryLLM,
		Provider:    "anthropic",
		SecretValue: "mock-llm-key-tenant-a",
	}

	credA, err := svc.SaveCredential(ctx, tenantA, saveReq)
	if err != nil {
		t.Fatalf("failed to save credential for tenant A: %v", err)
	}

	// Tenant B tries to get Tenant A's credential by ID
	_, err = svc.GetCredential(ctx, tenantB, credA.ID)
	if err == nil {
		t.Errorf("expected error when tenant B attempts to access tenant A's credential, got nil")
	}

	// Tenant B tries to delete Tenant A's credential by ID
	err = svc.DeleteCredential(ctx, tenantB, credA.ID)
	if err == nil {
		t.Errorf("expected error when tenant B attempts to delete tenant A's credential, got nil")
	}

	// Tenant B lists credentials - should not see Tenant A's credential
	listB, err := svc.ListCredentials(ctx, tenantB)
	if err != nil {
		t.Fatalf("unexpected error listing credentials: %v", err)
	}
	if len(listB) != 0 {
		t.Errorf("expected empty list for tenant B, got %d items", len(listB))
	}
}

func TestValidationErrors(t *testing.T) {
	svc, _, _ := setupTestService()
	ctx := context.Background()
	tenantID := uuid.New()

	tests := []struct {
		name string
		req  credentials.SaveCredentialRequest
	}{
		{
			name: "empty category",
			req: credentials.SaveCredentialRequest{
				Category:    "",
				Provider:    "openai",
				SecretValue: "val",
			},
		},
		{
			name: "invalid category",
			req: credentials.SaveCredentialRequest{
				Category:    "unsupported",
				Provider:    "openai",
				SecretValue: "val",
			},
		},
		{
			name: "empty provider",
			req: credentials.SaveCredentialRequest{
				Category:    credentials.CategoryLLM,
				Provider:    "",
				SecretValue: "val",
			},
		},
		{
			name: "empty secret value",
			req: credentials.SaveCredentialRequest{
				Category:    credentials.CategoryLLM,
				Provider:    "openai",
				SecretValue: "",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.SaveCredential(ctx, tenantID, tc.req)
			if err == nil {
				t.Errorf("expected validation error for %s, got nil", tc.name)
			}
		})
	}
}

func TestVerifyCredential(t *testing.T) {
	svc, _, _ := setupTestService()
	ctx := context.Background()
	tenantID := uuid.New()

	cred, err := svc.SaveCredential(ctx, tenantID, credentials.SaveCredentialRequest{
		Category:    credentials.CategoryLLM,
		Provider:    "openai",
		SecretValue: "mock-secret-for-verify",
	})
	if err != nil {
		t.Fatalf("failed to save credential: %v", err)
	}

	resp, err := svc.VerifyCredential(ctx, tenantID, cred.ID)
	if err != nil {
		t.Fatalf("failed to verify credential: %v", err)
	}
	if !resp.Verified {
		t.Errorf("expected credential to be verified, got false")
	}
	if resp.Reference != cred.Reference {
		t.Errorf("expected reference %s, got %s", cred.Reference, resp.Reference)
	}
}

func TestDeleteCredential(t *testing.T) {
	svc, vStore, _ := setupTestService()
	ctx := context.Background()
	tenantID := uuid.New()

	cred, err := svc.SaveCredential(ctx, tenantID, credentials.SaveCredentialRequest{
		Category:    credentials.CategoryMCP,
		Provider:    "github-tools",
		SecretValue: "mock-token-to-delete",
	})
	if err != nil {
		t.Fatalf("failed to save credential: %v", err)
	}

	// Delete credential
	if err := svc.DeleteCredential(ctx, tenantID, cred.ID); err != nil {
		t.Fatalf("failed to delete credential: %v", err)
	}

	// Should not be found in metadata store
	_, err = svc.GetCredential(ctx, tenantID, cred.ID)
	if err == nil {
		t.Errorf("expected ErrNotFound after deletion, got nil")
	}

	// Should be deleted from Vault as well
	_, err = vStore.ReadSecretValue(ctx, tenantID, cred.Reference)
	if err == nil {
		t.Errorf("expected vault read to fail after deletion, got nil")
	}
}
