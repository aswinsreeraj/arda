package store

import (
	"context"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
)

// Store defines the storage operations required by the Agent Registry.
type Store interface {
	// CreateAgent inserts a new agent record.
	CreateAgent(ctx context.Context, agent *domain.Agent) error

	// CreateAgentWithInitialVersion inserts an agent and its initial draft version within a single transaction.
	CreateAgentWithInitialVersion(ctx context.Context, agent *domain.Agent, version *domain.AgentVersion) error

	// GetAgent retrieves an agent scoped to a tenant. Returns ErrNotFound if not present.
	GetAgent(ctx context.Context, tenantID, agentID uuid.UUID) (*domain.Agent, error)

	// CreateVersion inserts a new draft version for an agent.
	CreateVersion(ctx context.Context, version *domain.AgentVersion) error

	// GetVersion retrieves a specific version of an agent.
	GetVersion(ctx context.Context, agentID, versionID uuid.UUID) (*domain.AgentVersion, error)

	// GetLatestPublishedVersion retrieves the latest published version of an agent.
	GetLatestPublishedVersion(ctx context.Context, agentID uuid.UUID) (*domain.AgentVersion, error)

	// GetLatestDraftVersion retrieves the latest draft version of an agent, or nil if none.
	GetLatestDraftVersion(ctx context.Context, agentID uuid.UUID) (*domain.AgentVersion, error)

	// GetNextVersionNumber returns the next incremental version number for an agent.
	GetNextVersionNumber(ctx context.Context, agentID uuid.UUID) (int, error)

	// PublishVersion transitions a draft version to published.
	PublishVersion(ctx context.Context, agentID, versionID uuid.UUID) (*domain.AgentVersion, error)

	// UpdateDraftVersion updates an existing draft version. Rejects update if version is published.
	UpdateDraftVersion(ctx context.Context, version *domain.AgentVersion) error

	// DeleteVersion deletes an existing draft version. Rejects deletion if version is published.
	DeleteVersion(ctx context.Context, agentID, versionID uuid.UUID) error
}
