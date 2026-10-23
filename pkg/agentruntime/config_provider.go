package agentruntime

import (
	"context"
	"fmt"
	"sync"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
)

// AgentConfigProvider retrieves immutable domain.Configuration for an exact pinned agent version.
// Implementations MUST scope lookups strictly by tenantID, agentID, and versionID to prevent
// version drift or cross-tenant data leakage. Implementations MUST NOT silently load the latest version.
type AgentConfigProvider interface {
	GetAgentConfig(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (domain.Configuration, error)
}

// AgentConfigProviderFunc adapts a function to the AgentConfigProvider interface.
type AgentConfigProviderFunc func(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (domain.Configuration, error)

// GetAgentConfig invokes the wrapped function.
func (f AgentConfigProviderFunc) GetAgentConfig(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (domain.Configuration, error) {
	return f(ctx, tenantID, agentID, versionID)
}

// MockConfigProvider provides a deterministic in-memory test double for AgentConfigProvider.
// It allows unit tests and local workflows to configure versioned configurations and simulate errors.
type MockConfigProvider struct {
	mu        sync.RWMutex
	configs   map[string]domain.Configuration
	err       error
	calls     int
	lastQuery struct {
		TenantID  uuid.UUID
		AgentID   uuid.UUID
		VersionID uuid.UUID
	}
}

// NewMockConfigProvider creates a new MockConfigProvider instance.
func NewMockConfigProvider() *MockConfigProvider {
	return &MockConfigProvider{
		configs: make(map[string]domain.Configuration),
	}
}

// SetConfig registers an exact pinned configuration for a specific tenant, agent, and version.
func (m *MockConfigProvider) SetConfig(tenantID, agentID, versionID uuid.UUID, cfg domain.Configuration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := configKey(tenantID, agentID, versionID)
	m.configs[key] = cfg
}

// SetError configures a simulated error to return from GetAgentConfig.
func (m *MockConfigProvider) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// Calls returns the number of times GetAgentConfig has been called.
func (m *MockConfigProvider) Calls() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.calls
}

// LastQuery returns the identifiers passed to the most recent GetAgentConfig call.
func (m *MockConfigProvider) LastQuery() (tenantID, agentID, versionID uuid.UUID) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastQuery.TenantID, m.lastQuery.AgentID, m.lastQuery.VersionID
}

// GetAgentConfig retrieves the pinned configuration or returns ErrConfigurationNotFound.
func (m *MockConfigProvider) GetAgentConfig(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (domain.Configuration, error) {
	if err := ctx.Err(); err != nil {
		return domain.Configuration{}, err
	}

	m.mu.Lock()
	m.calls++
	m.lastQuery.TenantID = tenantID
	m.lastQuery.AgentID = agentID
	m.lastQuery.VersionID = versionID
	simErr := m.err
	m.mu.Unlock()

	if simErr != nil {
		return domain.Configuration{}, simErr
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	key := configKey(tenantID, agentID, versionID)
	cfg, ok := m.configs[key]
	if !ok {
		return domain.Configuration{}, fmt.Errorf("%w: version %s for agent %s in tenant %s", ErrConfigurationNotFound, versionID, agentID, tenantID)
	}

	return cfg, nil
}

func configKey(tenantID, agentID, versionID uuid.UUID) string {
	return fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)
}
