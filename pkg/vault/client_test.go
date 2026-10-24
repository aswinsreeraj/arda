package vault_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/google/uuid"
)

// mockVaultServer simulates a HashiCorp Vault KV v2 HTTP backend.
type mockVaultServer struct {
	mu            sync.RWMutex
	storage       map[string]map[string]string
	expectedToken string
	failWithCode  int
}

func newMockVaultServer(expectedToken string) *mockVaultServer {
	return &mockVaultServer{
		storage:       make(map[string]map[string]string),
		expectedToken: expectedToken,
	}
}

func (s *mockVaultServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check authentication token
	token := r.Header.Get("X-Vault-Token")
	if token == "" || (s.expectedToken != "" && token != s.expectedToken) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []string{"permission denied"},
		})
		return
	}

	if s.failWithCode > 0 {
		w.WriteHeader(s.failWithCode)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []string{"internal server error"},
		})
		return
	}

	// URL format: /v1/{mount}/data/{path...} or /v1/{mount}/metadata/{path...}
	path := r.URL.Path
	if strings.HasPrefix(path, "/v1/secret/metadata/") && r.Method == http.MethodDelete {
		subPath := strings.TrimPrefix(path, "/v1/secret/metadata/")
		delete(s.storage, subPath)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	prefix := "/v1/secret/data/"
	if !strings.HasPrefix(path, prefix) {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	subPath := strings.TrimPrefix(path, prefix)

	switch r.Method {
	case http.MethodGet:
		data, ok := s.storage[subPath]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errors": []string{"secret not found"},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"request_id": "test-req-id",
			"data": map[string]any{
				"data": data,
				"metadata": map[string]any{
					"version": 1,
				},
			},
		})

	case http.MethodPost:
		var req struct {
			Data map[string]string `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.storage[subPath] = req.Data
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"request_id": "test-req-id",
			"data": map[string]any{
				"version": 1,
			},
		})

	case http.MethodDelete:
		delete(s.storage, subPath)
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestVaultClient_ValidWriteAndRetrieval(t *testing.T) {
	mock := newMockVaultServer("test-valid-token")
	ts := httptest.NewServer(mock)
	defer ts.Close()

	client, err := vault.NewClient(vault.Config{
		Address:   ts.URL,
		Token:     "test-valid-token",
		MountPath: "secret",
		Timeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected NewClient error: %v", err)
	}

	ctx := context.Background()
	tenantA := uuid.New()

	// 1. Write multi-field secret
	secretData := map[string]string{
		"api_key": "sk-proj-test123456",
		"org_id":  "org-test-999",
	}
	err = client.WriteSecret(ctx, tenantA, "llm/openai", secretData)
	if err != nil {
		t.Fatalf("failed to write secret: %v", err)
	}

	// 2. Read full secret map
	readMap, err := client.ReadSecret(ctx, tenantA, "secret:llm/openai")
	if err != nil {
		t.Fatalf("failed to read secret: %v", err)
	}
	if readMap["api_key"] != "sk-proj-test123456" || readMap["org_id"] != "org-test-999" {
		t.Fatalf("read secret mismatch, got: %+v", readMap)
	}

	// 3. Read specific field with selector
	val, err := client.ReadSecretValue(ctx, tenantA, "secret:llm/openai#api_key")
	if err != nil {
		t.Fatalf("failed to read secret value with field: %v", err)
	}
	if val != "sk-proj-test123456" {
		t.Fatalf("expected 'sk-proj-test123456', got %q", val)
	}

	// 4. Write and read single value using convenience helper
	err = client.WriteSecretString(ctx, tenantA, "mcp/github-token", "ghp_securetoken777")
	if err != nil {
		t.Fatalf("failed to write secret string: %v", err)
	}

	singleVal, err := client.ReadSecretValue(ctx, tenantA, "secret:mcp/github-token")
	if err != nil {
		t.Fatalf("failed to read single secret value: %v", err)
	}
	if singleVal != "ghp_securetoken777" {
		t.Fatalf("expected 'ghp_securetoken777', got %q", singleVal)
	}

	// 5. Context resolution helper
	ctxWithTenant := vault.WithTenantID(ctx, tenantA)
	ctxVal, err := client.ReadSecretValueFromContext(ctxWithTenant, "secret:mcp/github-token")
	if err != nil {
		t.Fatalf("failed to read from context: %v", err)
	}
	if ctxVal != "ghp_securetoken777" {
		t.Fatalf("expected 'ghp_securetoken777', got %q", ctxVal)
	}
}

func TestVaultClient_MissingSecretHandling(t *testing.T) {
	mock := newMockVaultServer("test-valid-token")
	ts := httptest.NewServer(mock)
	defer ts.Close()

	client, err := vault.NewClient(vault.Config{
		Address: ts.URL,
		Token:   "test-valid-token",
	})
	if err != nil {
		t.Fatalf("unexpected NewClient error: %v", err)
	}

	tenantID := uuid.New()
	_, err = client.ReadSecret(context.Background(), tenantID, "secret:nonexistent-key")
	if err == nil {
		t.Fatalf("expected error for nonexistent secret, got nil")
	}

	if !errors.Is(err, vault.ErrSecretNotFound) {
		t.Fatalf("expected errors.Is(err, vault.ErrSecretNotFound) to be true, got %v", err)
	}

	// ReadSecretValue on non-existent secret
	_, err = client.ReadSecretValue(context.Background(), tenantID, "secret:nonexistent-key")
	if !errors.Is(err, vault.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound, got %v", err)
	}
}

func TestVaultClient_TenantIsolation(t *testing.T) {
	mock := newMockVaultServer("test-token")
	ts := httptest.NewServer(mock)
	defer ts.Close()

	client, err := vault.NewClient(vault.Config{
		Address: ts.URL,
		Token:   "test-token",
	})
	if err != nil {
		t.Fatalf("unexpected NewClient error: %v", err)
	}

	ctx := context.Background()
	tenantA := uuid.New()
	tenantB := uuid.New()

	// Tenant A writes a secret
	err = client.WriteSecretString(ctx, tenantA, "database-password", "super-secret-pass-A")
	if err != nil {
		t.Fatalf("failed to write tenant A secret: %v", err)
	}

	// Tenant A can read it
	valA, err := client.ReadSecretValue(ctx, tenantA, "secret:database-password")
	if err != nil || valA != "super-secret-pass-A" {
		t.Fatalf("tenant A should read own secret, got %q, err: %v", valA, err)
	}

	// Tenant B attempts to read Tenant A's secret using same key
	_, err = client.ReadSecretValue(ctx, tenantB, "secret:database-password")
	if err == nil {
		t.Fatalf("expected Tenant B to get error when reading Tenant A's key, got nil")
	}
	if !errors.Is(err, vault.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound for cross-tenant access, got: %v", err)
	}

	// Tenant B attempts path traversal to break out into Tenant A's namespace
	traversalRef := "secret:../" + tenantA.String() + "/database-password"
	_, err = client.ReadSecretValue(ctx, tenantB, traversalRef)
	if err == nil {
		t.Fatalf("expected traversal reference to be rejected, got nil")
	}
	if !errors.Is(err, vault.ErrInvalidReference) {
		t.Fatalf("expected ErrInvalidReference for traversal, got: %v", err)
	}
}

func TestVaultClient_InvalidReferencesAndForbiddenPaths(t *testing.T) {
	mock := newMockVaultServer("test-token")
	ts := httptest.NewServer(mock)
	defer ts.Close()

	client, err := vault.NewClient(vault.Config{
		Address: ts.URL,
		Token:   "test-token",
	})
	if err != nil {
		t.Fatalf("unexpected NewClient error: %v", err)
	}

	ctx := context.Background()
	validTenant := uuid.New()

	forbiddenTestCases := []struct {
		name string
		ref  string
	}{
		{"empty string", ""},
		{"empty after secret prefix", "secret:"},
		{"empty after ref prefix", "ref:"},
		{"environment prefix", "env:MY_VAR"},
		{"leading slash", "secret:/llm/openai"},
		{"trailing slash", "secret:llm/openai/"},
		{"consecutive slashes", "secret:llm//openai"},
		{"directory traversal relative", "secret:../../etc/passwd"},
		{"single dot segment", "secret:./key"},
		{"backslash path", "secret:llm\\openai"},
		{"reserved root tenants", "secret:tenants/other-id/key"},
		{"reserved root sys", "secret:sys/mounts"},
		{"reserved root auth", "secret:auth/token"},
		{"reserved root v1", "secret:v1/data"},
		{"non-alphanumeric start", "secret:-invalid-key"},
		{"space in key", "secret:llm /openai"},
		{"null byte in key", "secret:llm\x00openai"},
		{"invalid field selector", "secret:llm/openai#field with spaces!"},
	}

	for _, tc := range forbiddenTestCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.ReadSecret(ctx, validTenant, tc.ref)
			if err == nil {
				t.Fatalf("expected reference %q to be rejected, but it succeeded", tc.ref)
			}
			if !errors.Is(err, vault.ErrInvalidReference) {
				t.Fatalf("expected ErrInvalidReference for %q, got: %v", tc.ref, err)
			}
		})
	}

	// Test nil tenant ID
	t.Run("nil tenant id rejected", func(t *testing.T) {
		err := client.WriteSecretString(ctx, uuid.Nil, "test-key", "secret-val")
		if err == nil {
			t.Fatalf("expected error for nil tenant ID, got nil")
		}
		if !errors.Is(err, vault.ErrTenantRequired) {
			t.Fatalf("expected ErrTenantRequired, got: %v", err)
		}
	})

	// Test context without tenant ID
	t.Run("context without tenant id rejected", func(t *testing.T) {
		emptyCtx := context.Background()
		_, err := client.ReadSecretFromContext(emptyCtx, "secret:test-key")
		if err == nil {
			t.Fatalf("expected error for empty context tenant, got nil")
		}
		if !errors.Is(err, vault.ErrTenantRequired) {
			t.Fatalf("expected ErrTenantRequired, got: %v", err)
		}
	})
}

func TestVaultClient_AuthenticationFailureAndUnavailable(t *testing.T) {
	t.Run("invalid token returns ErrVaultAuthentication", func(t *testing.T) {
		mock := newMockVaultServer("correct-token")
		ts := httptest.NewServer(mock)
		defer ts.Close()

		client, err := vault.NewClient(vault.Config{
			Address: ts.URL,
			Token:   "wrong-bad-token",
		})
		if err != nil {
			t.Fatalf("unexpected NewClient error: %v", err)
		}

		err = client.WriteSecretString(context.Background(), uuid.New(), "key", "val")
		if err == nil {
			t.Fatalf("expected error for bad token, got nil")
		}
		if !errors.Is(err, vault.ErrVaultAuthentication) {
			t.Fatalf("expected ErrVaultAuthentication, got: %v", err)
		}
	})

	t.Run("server unavailable returns ErrVaultUnavailable", func(t *testing.T) {
		// Use an address with no listener on localhost
		client, err := vault.NewClient(vault.Config{
			Address: "http://127.0.0.1:59999",
			Token:   "any-token",
			Timeout: 500 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("unexpected NewClient error: %v", err)
		}

		err = client.WriteSecretString(context.Background(), uuid.New(), "key", "val")
		if err == nil {
			t.Fatalf("expected error when vault is down, got nil")
		}
		if !errors.Is(err, vault.ErrVaultUnavailable) {
			t.Fatalf("expected ErrVaultUnavailable, got: %v", err)
		}
	})

	t.Run("failing configuration constructor", func(t *testing.T) {
		// Empty address
		_, err := vault.NewClient(vault.Config{
			Address: "",
			Token:   "token",
		})
		if err == nil {
			t.Fatalf("expected error for empty address, got nil")
		}

		// Empty token
		_, err = vault.NewClient(vault.Config{
			Address: "http://127.0.0.1:8200",
			Token:   "",
		})
		if err == nil {
			t.Fatalf("expected error for empty token, got nil")
		}
	})
}

func TestVaultClient_ErrorsNeverContainSecretValues(t *testing.T) {
	sensitiveSecretPayload := "SUPER_SENSITIVE_API_KEY_NEVER_LEAK_12345"
	vaultToken := "SECRET_VAULT_TOKEN_67890"

	mock := newMockVaultServer(vaultToken)
	mock.failWithCode = http.StatusInternalServerError
	ts := httptest.NewServer(mock)
	defer ts.Close()

	client, err := vault.NewClient(vault.Config{
		Address: ts.URL,
		Token:   vaultToken,
	})
	if err != nil {
		t.Fatalf("unexpected NewClient error: %v", err)
	}

	tenantID := uuid.New()
	err = client.WriteSecretString(context.Background(), tenantID, "llm/secret-key", sensitiveSecretPayload)
	if err == nil {
		t.Fatalf("expected write to fail with 500 server error, got nil")
	}

	errMsg := err.Error()

	// Verify that neither the secret payload nor the Vault token is present anywhere in the error message
	if strings.Contains(errMsg, sensitiveSecretPayload) {
		t.Fatalf("CRITICAL SECURITY VIOLATION: error message leaked secret payload! Error: %s", errMsg)
	}
	if strings.Contains(errMsg, vaultToken) {
		t.Fatalf("CRITICAL SECURITY VIOLATION: error message leaked Vault auth token! Error: %s", errMsg)
	}

	// Verify domain error wrapper
	if !errors.Is(err, vault.ErrVaultUnavailable) {
		t.Fatalf("expected ErrVaultUnavailable for 500 error, got: %v", err)
	}
}

func TestMemoryStore_InterfaceContract(t *testing.T) {
	var store vault.SecretStore = vault.NewMemoryStore()

	ctx := context.Background()
	tenantA := uuid.New()
	tenantB := uuid.New()

	// 1. Write and read
	err := store.WriteSecret(ctx, tenantA, "llm/openai", map[string]string{
		"api_key": "test-key-abc",
	})
	if err != nil {
		t.Fatalf("memory write failed: %v", err)
	}

	val, err := store.ReadSecretValue(ctx, tenantA, "secret:llm/openai#api_key")
	if err != nil || val != "test-key-abc" {
		t.Fatalf("memory read value failed: got %q, err: %v", val, err)
	}

	// 2. Tenant isolation in memory
	_, err = store.ReadSecret(ctx, tenantB, "secret:llm/openai")
	if !errors.Is(err, vault.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound for tenant B, got: %v", err)
	}

	// 3. Delete secret
	err = store.DeleteSecret(ctx, tenantA, "llm/openai")
	if err != nil {
		t.Fatalf("memory delete failed: %v", err)
	}

	_, err = store.ReadSecret(ctx, tenantA, "secret:llm/openai")
	if !errors.Is(err, vault.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound after delete, got: %v", err)
	}
}

func TestVaultClient_BoundedTimeout(t *testing.T) {
	// Mock server that hangs for 200ms
	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slowServer.Close()

	// Client with 50ms timeout
	client, err := vault.NewClient(vault.Config{
		Address: slowServer.URL,
		Token:   "test-token",
		Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected NewClient error: %v", err)
	}

	err = client.WriteSecretString(context.Background(), uuid.New(), "test-key", "val")
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !errors.Is(err, vault.ErrVaultUnavailable) {
		t.Fatalf("expected ErrVaultUnavailable on timeout, got: %v", err)
	}
}

func TestRegistryConfigurationCompatibility(t *testing.T) {
	ref := vault.FormatReference("llm/openai-key")
	if !strings.HasPrefix(ref, "secret:") {
		t.Fatalf("expected reference to start with secret:, got %q", ref)
	}

	parsed, err := vault.ParseReference(ref)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if parsed.Key != "llm/openai-key" {
		t.Fatalf("expected key 'llm/openai-key', got %q", parsed.Key)
	}

	// Verify formatted with field
	refWithField := vault.FormatReferenceWithField("mcp/slack", "token")
	if refWithField != "secret:mcp/slack#token" {
		t.Fatalf("expected 'secret:mcp/slack#token', got %q", refWithField)
	}
	parsedField, err := vault.ParseReference(refWithField)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if parsedField.Key != "mcp/slack" || parsedField.Field != "token" {
		t.Fatalf("mismatch in parsed field reference: %+v", parsedField)
	}
}

func TestVaultClient_DestroySecretKVv2Metadata(t *testing.T) {
	mock := newMockVaultServer("test-token")
	ts := httptest.NewServer(mock)
	defer ts.Close()

	client, err := vault.NewClient(vault.Config{
		Address: ts.URL,
		Token:   "test-token",
	})
	if err != nil {
		t.Fatalf("unexpected NewClient error: %v", err)
	}

	tenantID := uuid.New()
	key := "credentials/llm/openai"

	// 1. Write secret
	err = client.WriteSecret(context.Background(), tenantID, key, map[string]string{
		"api_key": "fake-openai-key-to-destroy-12345",
	})
	if err != nil {
		t.Fatalf("failed to write secret: %v", err)
	}

	// 2. Destroy secret permanently (purging KV v2 metadata)
	err = client.DestroySecret(context.Background(), tenantID, key)
	if err != nil {
		t.Fatalf("failed to destroy secret: %v", err)
	}

	// 3. Confirm secret is gone
	_, err = client.ReadSecret(context.Background(), tenantID, key)
	if !errors.Is(err, vault.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound after destroy, got: %v", err)
	}
}
