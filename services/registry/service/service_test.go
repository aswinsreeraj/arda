package service

import (
	"context"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
)

type mockStore struct {
	agents          map[uuid.UUID]*domain.Agent
	versions        map[uuid.UUID]*domain.AgentVersion
	agentVersions   map[uuid.UUID][]*domain.AgentVersion
	nextVersionNum  int
	publishErr      error
	createAgentErr  error
	createVerErr    error
}

func newMockStore() *mockStore {
	return &mockStore{
		agents:        make(map[uuid.UUID]*domain.Agent),
		versions:      make(map[uuid.UUID]*domain.AgentVersion),
		agentVersions: make(map[uuid.UUID][]*domain.AgentVersion),
	}
}

func (m *mockStore) CreateAgent(ctx context.Context, agent *domain.Agent) error {
	if m.createAgentErr != nil {
		return m.createAgentErr
	}
	m.agents[agent.ID] = agent
	return nil
}

func (m *mockStore) CreateAgentWithInitialVersion(ctx context.Context, agent *domain.Agent, version *domain.AgentVersion) error {
	if m.createAgentErr != nil {
		return m.createAgentErr
	}
	m.agents[agent.ID] = agent
	m.versions[version.ID] = version
	m.agentVersions[agent.ID] = append(m.agentVersions[agent.ID], version)
	return nil
}

func (m *mockStore) GetAgent(ctx context.Context, tenantID, agentID uuid.UUID) (*domain.Agent, error) {
	agent, ok := m.agents[agentID]
	if !ok || agent.TenantID != tenantID {
		return nil, domain.NewNotFoundError("agent not found")
	}
	return agent, nil
}

func (m *mockStore) CreateVersion(ctx context.Context, version *domain.AgentVersion) error {
	if m.createVerErr != nil {
		return m.createVerErr
	}
	m.versions[version.ID] = version
	m.agentVersions[version.AgentID] = append(m.agentVersions[version.AgentID], version)
	return nil
}

func (m *mockStore) GetVersion(ctx context.Context, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	ver, ok := m.versions[versionID]
	if !ok || ver.AgentID != agentID {
		return nil, domain.NewNotFoundError("agent version not found")
	}
	return ver, nil
}

func (m *mockStore) GetLatestPublishedVersion(ctx context.Context, agentID uuid.UUID) (*domain.AgentVersion, error) {
	vers := m.agentVersions[agentID]
	for i := len(vers) - 1; i >= 0; i-- {
		if vers[i].Published {
			return vers[i], nil
		}
	}
	return nil, domain.NewNotFoundError("no published version found for agent")
}

func (m *mockStore) GetLatestDraftVersion(ctx context.Context, agentID uuid.UUID) (*domain.AgentVersion, error) {
	vers := m.agentVersions[agentID]
	for i := len(vers) - 1; i >= 0; i-- {
		if !vers[i].Published {
			return vers[i], nil
		}
	}
	return nil, nil
}

func (m *mockStore) GetNextVersionNumber(ctx context.Context, agentID uuid.UUID) (int, error) {
	if m.nextVersionNum > 0 {
		return m.nextVersionNum, nil
	}
	return len(m.agentVersions[agentID]) + 1, nil
}

func (m *mockStore) PublishVersion(ctx context.Context, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	if m.publishErr != nil {
		return nil, m.publishErr
	}
	ver, ok := m.versions[versionID]
	if !ok || ver.AgentID != agentID {
		return nil, domain.NewNotFoundError("version not found")
	}
	if ver.Published {
		return nil, domain.NewConflictError(domain.CodeAlreadyPublished, "version is already published")
	}
	ver.Published = true
	now := time.Now().UTC()
	ver.PublishedAt = &now
	return ver, nil
}

func (m *mockStore) UpdateDraftVersion(ctx context.Context, version *domain.AgentVersion) error {
	ver, ok := m.versions[version.ID]
	if !ok {
		return domain.NewNotFoundError("version not found")
	}
	if ver.Published {
		return domain.NewConflictError(domain.CodePublishedImmutable, "published version is immutable")
	}
	ver.Configuration = version.Configuration
	return nil
}

func (m *mockStore) DeleteVersion(ctx context.Context, agentID, versionID uuid.UUID) error {
	ver, ok := m.versions[versionID]
	if !ok {
		return domain.NewNotFoundError("version not found")
	}
	if ver.Published {
		return domain.NewConflictError(domain.CodePublishedImmutable, "published version is immutable")
	}
	delete(m.versions, versionID)
	return nil
}

func TestRegistryService_CreateAndGetAgent(t *testing.T) {
	ctx := context.Background()
	mock := newMockStore()
	svc := NewRegistryService(mock)

	tenantA := uuid.New()
	tenantB := uuid.New()

	t.Run("fails without tenant id", func(t *testing.T) {
		_, err := svc.CreateAgent(ctx, uuid.Nil, CreateAgentRequest{Name: "Bot"})
		if err == nil {
			t.Fatalf("expected error without tenant id, got nil")
		}
	})

	t.Run("creates agent with flat configuration", func(t *testing.T) {
		threshold := 0.8
		req := CreateAgentRequest{
			Name:                 "Customer Bot",
			PromptTemplate:       "Support prompt",
			Tools:                []string{"kb_search"},
			ReliabilityThreshold: &threshold,
			ExecutionMode:        domain.ExecutionModeSupervised,
		}
		detail, err := svc.CreateAgent(ctx, tenantA, req)
		if err != nil {
			t.Fatalf("unexpected error creating agent: %v", err)
		}
		if detail.Agent.Name != "Customer Bot" {
			t.Errorf("expected name 'Customer Bot', got %s", detail.Agent.Name)
		}
		if detail.DraftVersion == nil {
			t.Fatalf("expected draft version 1 to be created, got nil")
		}
		if detail.DraftVersion.VersionNumber != 1 {
			t.Errorf("expected version 1, got %d", detail.DraftVersion.VersionNumber)
		}

		// Retrieve agent as Tenant A
		got, err := svc.GetAgent(ctx, tenantA, detail.Agent.ID)
		if err != nil {
			t.Fatalf("expected Tenant A to retrieve agent, got: %v", err)
		}
		if got.Agent.ID != detail.Agent.ID {
			t.Errorf("retrieved agent id mismatch")
		}

		// Attempt to retrieve agent as Tenant B (must fail with not found)
		_, errB := svc.GetAgent(ctx, tenantB, detail.Agent.ID)
		if errB == nil {
			t.Fatalf("expected Tenant B lookup to fail, but succeeded")
		}
		domErr, ok := errB.(*domain.DomainError)
		if !ok || domErr.Code != domain.CodeNotFound {
			t.Errorf("expected NOT_FOUND domain error, got %v", errB)
		}
	})
}

func TestRegistryService_VersionLifecycleAndImmutability(t *testing.T) {
	ctx := context.Background()
	mock := newMockStore()
	svc := NewRegistryService(mock)

	tenantID := uuid.New()
	agent := &domain.Agent{
		ID:       uuid.New(),
		TenantID: tenantID,
		Name:     "Governance Agent",
	}
	_ = mock.CreateAgent(ctx, agent)

	cfg1 := domain.Configuration{
		PromptTemplate:       "Evaluate decisions",
		Tools:                []string{"gate"},
		ReliabilityThreshold: 0.9,
		ExecutionMode:        domain.ExecutionModeAutonomous,
	}

	// Create draft version 1
	v1, err := svc.CreateDraftVersion(ctx, tenantID, agent.ID, cfg1)
	if err != nil {
		t.Fatalf("failed to create draft version: %v", err)
	}
	if v1.Published {
		t.Errorf("expected draft version to have published=false")
	}

	// Update draft version 1 configuration (allowed)
	cfg1Updated := cfg1
	cfg1Updated.PromptTemplate = "Updated prompt"
	updatedV1, err := svc.UpdateDraftVersion(ctx, tenantID, agent.ID, v1.ID, cfg1Updated)
	if err != nil {
		t.Fatalf("expected draft update to succeed, got: %v", err)
	}
	if updatedV1.Configuration.PromptTemplate != "Updated prompt" {
		t.Errorf("draft configuration not updated")
	}

	// Publish version 1
	pubV1, err := svc.PublishVersion(ctx, tenantID, agent.ID, v1.ID)
	if err != nil {
		t.Fatalf("failed to publish version: %v", err)
	}
	if !pubV1.Published || pubV1.PublishedAt == nil {
		t.Errorf("expected published=true and non-nil published_at")
	}

	// Attempt to re-publish (must return conflict)
	_, errRepub := svc.PublishVersion(ctx, tenantID, agent.ID, v1.ID)
	if errRepub == nil {
		t.Fatalf("expected conflict on republishing, got nil")
	}

	// Attempt to modify published version (must return conflict)
	_, errModify := svc.UpdateDraftVersion(ctx, tenantID, agent.ID, v1.ID, cfg1)
	if errModify == nil {
		t.Fatalf("expected error modifying published version, got nil")
	}

	// Attempt to delete published version (must return conflict)
	errDelete := svc.DeleteDraftVersion(ctx, tenantID, agent.ID, v1.ID)
	if errDelete == nil {
		t.Fatalf("expected error deleting published version, got nil")
	}

	// Resolve latest published version (must return v1)
	latest, err := svc.GetLatestPublishedVersion(ctx, tenantID, agent.ID)
	if err != nil {
		t.Fatalf("failed to get latest published version: %v", err)
	}
	if latest.ID != v1.ID {
		t.Errorf("expected latest published version to be v1")
	}

	// Create draft version 2
	cfg2 := cfg1
	cfg2.ReliabilityThreshold = 0.95
	v2, err := svc.CreateDraftVersion(ctx, tenantID, agent.ID, cfg2)
	if err != nil {
		t.Fatalf("failed to create v2 draft: %v", err)
	}
	if v2.VersionNumber != 2 {
		t.Errorf("expected version number 2, got %d", v2.VersionNumber)
	}

	// Publish version 2
	pubV2, err := svc.PublishVersion(ctx, tenantID, agent.ID, v2.ID)
	if err != nil {
		t.Fatalf("failed to publish v2: %v", err)
	}
	if !pubV2.Published {
		t.Errorf("expected v2 to be published")
	}

	// Verify latest published version is now v2, and v1 is still published
	latestNow, err := svc.GetLatestPublishedVersion(ctx, tenantID, agent.ID)
	if err != nil {
		t.Fatalf("failed to get latest published version: %v", err)
	}
	if latestNow.ID != v2.ID {
		t.Errorf("expected latest published version to be v2")
	}

	v1Check, _ := mock.GetVersion(ctx, agent.ID, v1.ID)
	if !v1Check.Published {
		t.Errorf("historical version v1 was modified; must remain published")
	}
}
