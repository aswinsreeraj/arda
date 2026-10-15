package store

import (
	"time"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
)

// AgentModel maps to the agents table in PostgreSQL.
type AgentModel struct {
	ID        uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	TenantID  uuid.UUID `gorm:"column:tenant_id;not null;type:uuid"`
	Name      string    `gorm:"column:name;not null;type:text"`
	CreatedAt time.Time `gorm:"column:created_at;not null;type:timestamptz"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;type:timestamptz"`
}

// TableName explicitly specifies the database table name.
func (AgentModel) TableName() string {
	return "agents"
}

// ToDomain converts an AgentModel to domain.Agent.
func (m *AgentModel) ToDomain() *domain.Agent {
	if m == nil {
		return nil
	}
	return &domain.Agent{
		ID:        m.ID,
		TenantID:  m.TenantID,
		Name:      m.Name,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

// AgentVersionModel maps to the agent_versions table in PostgreSQL.
type AgentVersionModel struct {
	ID            uuid.UUID            `gorm:"column:id;primaryKey;type:uuid"`
	AgentID       uuid.UUID            `gorm:"column:agent_id;not null;type:uuid"`
	VersionNumber int                  `gorm:"column:version_number;not null;type:integer"`
	Configuration domain.Configuration `gorm:"column:configuration;not null;type:jsonb"`
	Published     bool                 `gorm:"column:published;not null;default:false;type:boolean"`
	CreatedAt     time.Time            `gorm:"column:created_at;not null;type:timestamptz"`
	UpdatedAt     time.Time            `gorm:"column:updated_at;not null;type:timestamptz"`
	PublishedAt   *time.Time           `gorm:"column:published_at;type:timestamptz"`
}

// TableName explicitly specifies the database table name.
func (AgentVersionModel) TableName() string {
	return "agent_versions"
}

// ToDomain converts an AgentVersionModel to domain.AgentVersion.
func (m *AgentVersionModel) ToDomain() *domain.AgentVersion {
	if m == nil {
		return nil
	}
	return &domain.AgentVersion{
		ID:            m.ID,
		AgentID:       m.AgentID,
		VersionNumber: m.VersionNumber,
		Configuration: m.Configuration,
		Published:     m.Published,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
		PublishedAt:   m.PublishedAt,
	}
}
