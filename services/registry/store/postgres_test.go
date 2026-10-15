package store_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/database"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/aswinsreeraj/arda/services/registry/store"
	"github.com/google/uuid"
)

func getTestStore(t *testing.T) (*database.DB, *store.PostgresStore) {
	cfg := database.LoadConfigFromEnv()
	db, err := database.Connect(cfg)
	if err != nil {
		t.Skipf("skipping store integration test: cannot connect to postgres: %v", err)
	}
	st := store.NewPostgresStore(db.DB)
	return db, st
}

func createTestTenant(t *testing.T, db *database.DB, name string) uuid.UUID {
	id := uuid.New()
	err := db.Exec(`INSERT INTO tenants (id, name, status) VALUES (?, ?, 'active')`, id, name).Error
	if err != nil {
		t.Fatalf("failed to create test tenant: %v", err)
	}
	return id
}

func TestStore_AgentCRUDAndScoping(t *testing.T) {
	db, st := getTestStore(t)
	defer db.Close()
	ctx := context.Background()

	tenantA := createTestTenant(t, db, fmt.Sprintf("Tenant A %s", uuid.New().String()[:8]))
	tenantB := createTestTenant(t, db, fmt.Sprintf("Tenant B %s", uuid.New().String()[:8]))

	agent := &domain.Agent{
		TenantID: tenantA,
		Name:     "Support Bot A",
	}

	// 1. Create agent
	if err := st.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}
	if agent.ID == uuid.Nil {
		t.Fatalf("expected non-nil agent ID after creation")
	}

	// 2. Tenant A can get its own agent
	fetched, err := st.GetAgent(ctx, tenantA, agent.ID)
	if err != nil {
		t.Fatalf("expected tenant A to get agent, got: %v", err)
	}
	if fetched.Name != agent.Name {
		t.Errorf("expected name %s, got %s", agent.Name, fetched.Name)
	}

	// 3. Tenant B CANNOT get Tenant A's agent (cross-tenant read isolation)
	_, err = st.GetAgent(ctx, tenantB, agent.ID)
	if err == nil {
		t.Fatalf("expected not found error for cross-tenant access, got nil")
	}
	domErr, ok := err.(*domain.DomainError)
	if !ok || domErr.Code != domain.CodeNotFound {
		t.Fatalf("expected CodeNotFound for cross-tenant access, got: %v", err)
	}

	// 4. Missing agent returns CodeNotFound
	_, err = st.GetAgent(ctx, tenantA, uuid.New())
	if err == nil {
		t.Fatalf("expected not found error for nonexistent agent, got nil")
	}
}

func TestStore_VersionCreationAndLookup(t *testing.T) {
	db, st := getTestStore(t)
	defer db.Close()
	ctx := context.Background()

	tenant := createTestTenant(t, db, fmt.Sprintf("Tenant %s", uuid.New().String()[:8]))
	agent := &domain.Agent{TenantID: tenant, Name: "Versioned Agent"}
	if err := st.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// 1. Get next version number on new agent
	nextNum, err := st.GetNextVersionNumber(ctx, agent.ID)
	if err != nil {
		t.Fatalf("failed to get next version number: %v", err)
	}
	if nextNum != 1 {
		t.Errorf("expected next version number 1, got %d", nextNum)
	}

	// 2. Create draft version
	cfg := domain.Configuration{
		PromptTemplate:       "Answer support queries.",
		Tools:                []string{"search_kb", "send_email"},
		ReliabilityThreshold: 0.90,
		ExecutionMode:        domain.ExecutionModeSupervised,
		Guardrails:           map[string]any{"max_tokens": float64(1000)},
		Budgets:              map[string]any{"max_cost_usd": 0.50},
	}
	ver := &domain.AgentVersion{
		AgentID:       agent.ID,
		VersionNumber: nextNum,
		Configuration: cfg,
	}
	if err := st.CreateVersion(ctx, ver); err != nil {
		t.Fatalf("failed to create version: %v", err)
	}

	// 3. Get version by ID
	fetchedVer, err := st.GetVersion(ctx, agent.ID, ver.ID)
	if err != nil {
		t.Fatalf("failed to get version: %v", err)
	}
	if fetchedVer.VersionNumber != 1 {
		t.Errorf("expected version number 1, got %d", fetchedVer.VersionNumber)
	}
	if fetchedVer.Published {
		t.Errorf("expected draft version to be unpublished")
	}

	// 4. Cross-agent version lookup fails
	otherAgent := &domain.Agent{TenantID: tenant, Name: "Other Agent"}
	if err := st.CreateAgent(ctx, otherAgent); err != nil {
		t.Fatalf("failed to create other agent: %v", err)
	}
	_, err = st.GetVersion(ctx, otherAgent.ID, ver.ID)
	if err == nil {
		t.Fatalf("expected error querying version with wrong agent ID, got nil")
	}

	// 5. Get latest draft version
	draftVer, err := st.GetLatestDraftVersion(ctx, agent.ID)
	if err != nil {
		t.Fatalf("failed to get latest draft version: %v", err)
	}
	if draftVer == nil || draftVer.ID != ver.ID {
		t.Fatalf("expected draft version %s, got %v", ver.ID, draftVer)
	}
}

func TestStore_PublishingAndImmutability(t *testing.T) {
	db, st := getTestStore(t)
	defer db.Close()
	ctx := context.Background()

	tenant := createTestTenant(t, db, fmt.Sprintf("Tenant %s", uuid.New().String()[:8]))
	agent := &domain.Agent{TenantID: tenant, Name: "Immutable Agent"}
	if err := st.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	cfg := domain.Configuration{
		PromptTemplate:       "Initial draft prompt",
		Tools:                []string{"tool1"},
		ReliabilityThreshold: 0.85,
		ExecutionMode:        domain.ExecutionModeSupervised,
	}
	ver := &domain.AgentVersion{
		AgentID:       agent.ID,
		VersionNumber: 1,
		Configuration: cfg,
	}
	if err := st.CreateVersion(ctx, ver); err != nil {
		t.Fatalf("failed to create version: %v", err)
	}

	// 1. Update draft version succeeds
	ver.Configuration.PromptTemplate = "Updated draft prompt"
	if err := st.UpdateDraftVersion(ctx, ver); err != nil {
		t.Fatalf("failed to update draft version: %v", err)
	}

	// Verify updated configuration persisted
	updatedVer, err := st.GetVersion(ctx, agent.ID, ver.ID)
	if err != nil {
		t.Fatalf("failed to get updated version: %v", err)
	}
	if updatedVer.Configuration.PromptTemplate != "Updated draft prompt" {
		t.Errorf("expected updated prompt template, got %s", updatedVer.Configuration.PromptTemplate)
	}

	// 2. Publish version
	pubVer, err := st.PublishVersion(ctx, agent.ID, ver.ID)
	if err != nil {
		t.Fatalf("failed to publish version: %v", err)
	}
	if !pubVer.Published {
		t.Fatalf("expected version to be published")
	}
	if pubVer.PublishedAt == nil {
		t.Fatalf("expected non-nil published_at timestamp")
	}

	// 3. Latest published version returns the published version
	latestPub, err := st.GetLatestPublishedVersion(ctx, agent.ID)
	if err != nil {
		t.Fatalf("failed to get latest published version: %v", err)
	}
	if latestPub.ID != pubVer.ID {
		t.Errorf("expected latest published version %s, got %s", pubVer.ID, latestPub.ID)
	}

	// 4. Publishing already published version returns CodeAlreadyPublished
	_, err = st.PublishVersion(ctx, agent.ID, ver.ID)
	if err == nil {
		t.Fatalf("expected error re-publishing version, got nil")
	}
	domErr, ok := err.(*domain.DomainError)
	if !ok || domErr.Code != domain.CodeAlreadyPublished {
		t.Fatalf("expected CodeAlreadyPublished, got: %v", err)
	}

	// 5. Modifying published version is strictly rejected with CodePublishedImmutable
	ver.Configuration.PromptTemplate = "Illegal modification"
	err = st.UpdateDraftVersion(ctx, ver)
	if err == nil {
		t.Fatalf("expected error modifying published version, got nil")
	}
	domErr, ok = err.(*domain.DomainError)
	if !ok || domErr.Code != domain.CodePublishedImmutable {
		t.Fatalf("expected CodePublishedImmutable, got: %v", err)
	}

	// 6. Deleting published version is strictly rejected with CodePublishedImmutable
	err = st.DeleteVersion(ctx, agent.ID, ver.ID)
	if err == nil {
		t.Fatalf("expected error deleting published version, got nil")
	}
	domErr, ok = err.(*domain.DomainError)
	if !ok || domErr.Code != domain.CodePublishedImmutable {
		t.Fatalf("expected CodePublishedImmutable, got: %v", err)
	}
}

func TestStore_TransactionRollback(t *testing.T) {
	db, st := getTestStore(t)
	defer db.Close()
	ctx := context.Background()

	tenant := createTestTenant(t, db, fmt.Sprintf("Tenant %s", uuid.New().String()[:8]))

	// 1. Successful atomic creation of agent + initial version
	agentSuccess := &domain.Agent{TenantID: tenant, Name: "Tx Success Agent"}
	verSuccess := &domain.AgentVersion{
		VersionNumber: 1,
		Configuration: domain.Configuration{
			PromptTemplate:       "Valid prompt",
			Tools:                []string{"toolA"},
			ReliabilityThreshold: 0.80,
			ExecutionMode:        domain.ExecutionModeAutonomous,
		},
	}
	if err := st.CreateAgentWithInitialVersion(ctx, agentSuccess, verSuccess); err != nil {
		t.Fatalf("failed to create agent with initial version: %v", err)
	}

	// Verify both exist
	if _, err := st.GetAgent(ctx, tenant, agentSuccess.ID); err != nil {
		t.Fatalf("expected agent to exist after successful tx: %v", err)
	}
	if _, err := st.GetVersion(ctx, agentSuccess.ID, verSuccess.ID); err != nil {
		t.Fatalf("expected version to exist after successful tx: %v", err)
	}

	// 2. Failed atomic creation: trigger duplicate key constraint failure on version ID
	agentFail := &domain.Agent{TenantID: tenant, Name: "Tx Fail Agent"}
	verFail := &domain.AgentVersion{
		ID:            verSuccess.ID, // Primary key collision with already inserted version
		VersionNumber: 1,
		Configuration: domain.Configuration{
			PromptTemplate: "Prompt",
			Tools:          []string{},
		},
	}
	err := st.CreateAgentWithInitialVersion(ctx, agentFail, verFail)
	if err == nil {
		t.Fatalf("expected transaction to fail due to duplicate version ID, got nil")
	}

	// Verify that agent was NOT created (rolled back completely)
	_, err = st.GetAgent(ctx, tenant, agentFail.ID)
	if err == nil {
		t.Fatalf("CRITICAL: agent was created despite version insert failure (transaction rollback failed!)")
	}
}

func TestStore_JSONBRoundTrip(t *testing.T) {
	db, st := getTestStore(t)
	defer db.Close()
	ctx := context.Background()

	tenant := createTestTenant(t, db, fmt.Sprintf("Tenant %s", uuid.New().String()[:8]))
	agent := &domain.Agent{TenantID: tenant, Name: "JSONB Agent"}
	if err := st.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	originalCfg := domain.Configuration{
		PromptTemplate:       "System: You are an agent.\nUser: {{input}}",
		Tools:                []string{"mcp:weather", "mcp:calculator", "db:query"},
		ReliabilityThreshold: 0.945,
		ExecutionMode:        domain.ExecutionModeHumanInTheLoop,
		Guardrails: map[string]any{
			"pii_filter":     true,
			"toxicity_limit": float64(0.05),
			"blocked_words":  []any{"secret", "password"},
		},
		Budgets: map[string]any{
			"max_tokens_per_call": float64(4096),
			"daily_usd_limit":     float64(25.50),
		},
	}

	ver := &domain.AgentVersion{
		AgentID:       agent.ID,
		VersionNumber: 1,
		Configuration: originalCfg,
	}
	if err := st.CreateVersion(ctx, ver); err != nil {
		t.Fatalf("failed to create version: %v", err)
	}

	fetched, err := st.GetVersion(ctx, agent.ID, ver.ID)
	if err != nil {
		t.Fatalf("failed to get version: %v", err)
	}

	// Verify field values
	if fetched.Configuration.PromptTemplate != originalCfg.PromptTemplate {
		t.Errorf("prompt template mismatch")
	}
	if !reflect.DeepEqual(fetched.Configuration.Tools, originalCfg.Tools) {
		t.Errorf("tools mismatch: got %v, want %v", fetched.Configuration.Tools, originalCfg.Tools)
	}
	if fetched.Configuration.ReliabilityThreshold != originalCfg.ReliabilityThreshold {
		t.Errorf("threshold mismatch")
	}
	if fetched.Configuration.ExecutionMode != originalCfg.ExecutionMode {
		t.Errorf("execution mode mismatch")
	}
	if !reflect.DeepEqual(fetched.Configuration.Guardrails, originalCfg.Guardrails) {
		t.Errorf("guardrails mismatch: got %v, want %v", fetched.Configuration.Guardrails, originalCfg.Guardrails)
	}
	if !reflect.DeepEqual(fetched.Configuration.Budgets, originalCfg.Budgets) {
		t.Errorf("budgets mismatch: got %v, want %v", fetched.Configuration.Budgets, originalCfg.Budgets)
	}
}

func TestStore_UniqueConstraintConflict(t *testing.T) {
	db, st := getTestStore(t)
	defer db.Close()
	ctx := context.Background()

	tenant := createTestTenant(t, db, fmt.Sprintf("Tenant %s", uuid.New().String()[:8]))
	agent := &domain.Agent{TenantID: tenant, Name: "Conflict Agent"}
	if err := st.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	ver1 := &domain.AgentVersion{
		AgentID:       agent.ID,
		VersionNumber: 1,
		Configuration: domain.Configuration{PromptTemplate: "v1"},
	}
	if err := st.CreateVersion(ctx, ver1); err != nil {
		t.Fatalf("failed to create v1: %v", err)
	}

	// Attempt to create another version with version_number = 1
	verDuplicate := &domain.AgentVersion{
		AgentID:       agent.ID,
		VersionNumber: 1, // Duplicate
		Configuration: domain.Configuration{PromptTemplate: "duplicate v1"},
	}
	err := st.CreateVersion(ctx, verDuplicate)
	if err == nil {
		t.Fatalf("expected unique constraint violation on duplicate version_number, got nil")
	}
	domErr, ok := err.(*domain.DomainError)
	if !ok || domErr.Code != domain.CodeVersionConflict {
		t.Fatalf("expected CodeVersionConflict, got: %v", err)
	}
}
