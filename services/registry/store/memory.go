package store

import (
	"context"
	"sync"
	"time"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
)

// MemoryStore provides a thread-safe in-memory implementation of Store for tests and local development.
type MemoryStore struct {
	mu            sync.RWMutex
	agents        map[uuid.UUID]*domain.Agent
	versions      map[uuid.UUID]*domain.AgentVersion
	agentVersions map[uuid.UUID][]*domain.AgentVersion
}

// NewMemoryStore initializes an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		agents:        make(map[uuid.UUID]*domain.Agent),
		versions:      make(map[uuid.UUID]*domain.AgentVersion),
		agentVersions: make(map[uuid.UUID][]*domain.AgentVersion),
	}
}

func (m *MemoryStore) CreateAgent(ctx context.Context, agent *domain.Agent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := *agent
	m.agents[agent.ID] = &copied
	return nil
}

func (m *MemoryStore) CreateAgentWithInitialVersion(ctx context.Context, agent *domain.Agent, version *domain.AgentVersion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	agentCopy := *agent
	versionCopy := *version
	m.agents[agent.ID] = &agentCopy
	m.versions[version.ID] = &versionCopy
	m.agentVersions[agent.ID] = append(m.agentVersions[agent.ID], &versionCopy)
	return nil
}

func (m *MemoryStore) GetAgent(ctx context.Context, tenantID, agentID uuid.UUID) (*domain.Agent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	agent, ok := m.agents[agentID]
	if !ok || agent.TenantID != tenantID {
		return nil, domain.NewNotFoundError("agent not found")
	}
	copied := *agent
	return &copied, nil
}

func (m *MemoryStore) ListAgents(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.Agent, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var matched []domain.Agent
	for _, a := range m.agents {
		if a.TenantID == tenantID {
			matched = append(matched, *a)
		}
	}
	total := len(matched)
	if offset >= total {
		return []domain.Agent{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return matched[offset:end], total, nil
}

func (m *MemoryStore) CreateVersion(ctx context.Context, version *domain.AgentVersion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := *version
	m.versions[version.ID] = &copied
	m.agentVersions[version.AgentID] = append(m.agentVersions[version.AgentID], &copied)
	return nil
}

func (m *MemoryStore) GetVersion(ctx context.Context, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ver, ok := m.versions[versionID]
	if !ok || ver.AgentID != agentID {
		return nil, domain.NewNotFoundError("version not found")
	}
	copied := *ver
	return &copied, nil
}

func (m *MemoryStore) GetLatestPublishedVersion(ctx context.Context, agentID uuid.UUID) (*domain.AgentVersion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	vers := m.agentVersions[agentID]
	var latest *domain.AgentVersion
	for _, v := range vers {
		if v.Published {
			if latest == nil || v.VersionNumber > latest.VersionNumber {
				latest = v
			}
		}
	}
	if latest == nil {
		return nil, domain.NewNotFoundError("no published version found")
	}
	copied := *latest
	return &copied, nil
}

func (m *MemoryStore) GetLatestDraftVersion(ctx context.Context, agentID uuid.UUID) (*domain.AgentVersion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	vers := m.agentVersions[agentID]
	for _, v := range vers {
		if !v.Published {
			copied := *v
			return &copied, nil
		}
	}
	return nil, nil
}

func (m *MemoryStore) GetNextVersionNumber(ctx context.Context, agentID uuid.UUID) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	vers := m.agentVersions[agentID]
	maxNum := 0
	for _, v := range vers {
		if v.VersionNumber > maxNum {
			maxNum = v.VersionNumber
		}
	}
	return maxNum + 1, nil
}

func (m *MemoryStore) UpdateDraftVersion(ctx context.Context, version *domain.AgentVersion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.versions[version.ID]
	if !ok {
		return domain.NewNotFoundError("version not found")
	}
	if existing.Published {
		return domain.NewConflictError(domain.CodePublishedImmutable, "cannot update a published version")
	}
	copied := *version
	m.versions[version.ID] = &copied
	for i, v := range m.agentVersions[version.AgentID] {
		if v.ID == version.ID {
			m.agentVersions[version.AgentID][i] = &copied
			break
		}
	}
	return nil
}

func (m *MemoryStore) DeleteVersion(ctx context.Context, agentID, versionID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ver, ok := m.versions[versionID]
	if !ok || ver.AgentID != agentID {
		return domain.NewNotFoundError("version not found")
	}
	if ver.Published {
		return domain.NewConflictError(domain.CodePublishedImmutable, "cannot delete a published version")
	}
	delete(m.versions, versionID)
	// Remove from agentVersions
	var filtered []*domain.AgentVersion
	for _, v := range m.agentVersions[agentID] {
		if v.ID != versionID {
			filtered = append(filtered, v)
		}
	}
	m.agentVersions[agentID] = filtered
	return nil
}

func (m *MemoryStore) PublishVersion(ctx context.Context, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ver, ok := m.versions[versionID]
	if !ok || ver.AgentID != agentID {
		return nil, domain.NewNotFoundError("version not found")
	}
	if ver.Published {
		return nil, domain.NewConflictError(domain.CodeAlreadyPublished, "version is already published")
	}
	now := time.Now().UTC()
	ver.Published = true
	ver.PublishedAt = &now
	copied := *ver
	return &copied, nil
}
