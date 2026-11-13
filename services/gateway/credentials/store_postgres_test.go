package credentials_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/pkg/database"
	"github.com/aswinsreeraj/arda/services/gateway/credentials"
	"github.com/google/uuid"
)

func TestPostgresStore_DuplicateSave(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://arda_user:arda_password@127.0.0.1:5432/arda?sslmode=disable"
	}

	conn, err := database.Connect(database.Config{URL: dbURL})
	if err != nil {
		t.Skipf("skipping postgres test: %v", err)
	}
	defer conn.Close()

	store := credentials.NewPostgresStore(conn.DB)
	ctx := context.Background()

	// Create a dummy tenant in postgres first so foreign key doesn't fail
	tenantID := uuid.New()
	err = conn.DB.Exec("INSERT INTO tenants (id, name, status, created_at, updated_at) VALUES (?, 'Test Tenant', 'active', NOW(), NOW())", tenantID).Error
	if err != nil {
		t.Fatalf("failed to insert tenant: %v", err)
	}
	defer func() {
		conn.DB.Exec("DELETE FROM credentials WHERE tenant_id = ?", tenantID)
		conn.DB.Exec("DELETE FROM tenants WHERE id = ?", tenantID)
	}()

	initialID := uuid.New()
	cred1 := &credentials.Credential{
		ID:        initialID,
		TenantID:  tenantID,
		Category:  credentials.CategoryLLM,
		Provider:  "openai",
		Reference: "secret:credentials/llm/openai#api_key",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := store.Save(ctx, cred1); err != nil {
		t.Fatalf("first save failed: %v", err)
	}

	// 1. Save again with identical values but a new random ID
	credIdentical := &credentials.Credential{
		ID:        uuid.New(),
		TenantID:  tenantID,
		Category:  credentials.CategoryLLM,
		Provider:  "openai",
		Reference: "secret:credentials/llm/openai#api_key_v2",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.Save(ctx, credIdentical); err != nil {
		t.Fatalf("second save (identical) failed: %v", err)
	}
	if credIdentical.ID != initialID {
		t.Errorf("expected credIdentical to reuse initial ID %s, got %s", initialID, credIdentical.ID)
	}

	// 2. Save again with mixed case provider ("OpenAI") and whitespace
	credMixedCase := &credentials.Credential{
		ID:        uuid.New(),
		TenantID:  tenantID,
		Category:  credentials.CategoryLLM,
		Provider:  "  OpenAI  ",
		Reference: "secret:credentials/llm/openai#api_key_v3",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.Save(ctx, credMixedCase); err != nil {
		t.Fatalf("third save (mixed case) failed: %v", err)
	}
	if credMixedCase.ID != initialID {
		t.Errorf("expected credMixedCase to reuse initial ID %s, got %s", initialID, credMixedCase.ID)
	}

	// 3. Save again with uppercase category ("LLM")
	credUpperCat := &credentials.Credential{
		ID:        uuid.New(),
		TenantID:  tenantID,
		Category:  "LLM",
		Provider:  "OPENAI",
		Reference: "secret:credentials/llm/openai#api_key_v4",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.Save(ctx, credUpperCat); err != nil {
		t.Fatalf("fourth save (uppercase category) failed: %v", err)
	}
	if credUpperCat.ID != initialID {
		t.Errorf("expected credUpperCat to reuse initial ID %s, got %s", initialID, credUpperCat.ID)
	}

	// Count credentials in DB - must strictly be 1
	var count int64
	conn.DB.Model(&credentials.CredentialModel{}).Where("tenant_id = ?", tenantID).Count(&count)
	t.Logf("Total credentials in DB for tenant: %d", count)
	if count != 1 {
		t.Errorf("expected exactly 1 credential in postgres, got %d", count)
	}

	// Verify List returns exactly 1 item and has the updated reference
	list, err := store.List(ctx, tenantID)
	if err != nil {
		t.Fatalf("store.List failed: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected List to return 1 item, got %d", len(list))
	}
	if list[0].ID != initialID {
		t.Errorf("expected List item ID %s, got %s", initialID, list[0].ID)
	}
	if list[0].Reference != "secret:credentials/llm/openai#api_key_v4" {
		t.Errorf("expected latest reference to be persisted, got %s", list[0].Reference)
	}
}
