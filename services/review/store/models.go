package store

import (
	"time"

	"github.com/aswinsreeraj/arda/services/review/domain"
	"github.com/google/uuid"
)

// ReviewCaseModel maps to the review_cases table in PostgreSQL.
type ReviewCaseModel struct {
	ID                   string                `gorm:"column:id;primaryKey;type:varchar(64)"`
	TenantID             uuid.UUID             `gorm:"column:tenant_id;not null;type:uuid;index"`
	ExecutionID          string                `gorm:"column:execution_id;not null;type:varchar(128);index"`
	AgentID              uuid.UUID             `gorm:"column:agent_id;not null;type:uuid;index"`
	AgentName            string                `gorm:"column:agent_name;not null;type:varchar(255)"`
	AgentVersionID       uuid.UUID             `gorm:"column:agent_version_id;not null;type:uuid"`
	AgentVersionNumber   int                   `gorm:"column:agent_version_number;not null;default:1"`
	ExecutionMode        string                `gorm:"column:execution_mode;not null;default:'realtime';type:varchar(64)"`
	Status               string                `gorm:"column:status;not null;default:'OPEN';type:varchar(32);index"`
	ProposedDecision     string                `gorm:"column:proposed_decision;not null;type:text"`
	Confidence           float64               `gorm:"column:confidence;not null;default:0.0"`
	ReliabilityThreshold float64               `gorm:"column:reliability_threshold;not null;default:0.0"`
	EscalationReasons    []string              `gorm:"column:escalation_reasons;not null;serializer:json"`
	FrozenEvidence       domain.FrozenEvidence `gorm:"column:frozen_evidence;not null;serializer:json"`

	Decision         string     `gorm:"column:decision;type:varchar(32)"`
	FinalDecision    string     `gorm:"column:final_decision;type:varchar(64)"`
	ResolutionReason string     `gorm:"column:resolution_reason;type:text"`
	ReviewerID       string     `gorm:"column:reviewer_id;type:varchar(128)"`
	AuditRecordID    string     `gorm:"column:audit_record_id;type:varchar(64)"`
	ResolvedAt       *time.Time `gorm:"column:resolved_at;type:timestamptz"`

	CreatedAt time.Time `gorm:"column:created_at;not null;type:timestamptz"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;type:timestamptz"`
}

// TableName returns the PostgreSQL table name.
func (ReviewCaseModel) TableName() string {
	return "review_cases"
}

// ToDomain converts the GORM model to domain.ReviewCase.
func (m *ReviewCaseModel) ToDomain() *domain.ReviewCase {
	if m == nil {
		return nil
	}
	return &domain.ReviewCase{
		ID:                   m.ID,
		TenantID:             m.TenantID,
		ExecutionID:          m.ExecutionID,
		AgentID:              m.AgentID,
		AgentName:            m.AgentName,
		AgentVersionID:       m.AgentVersionID,
		AgentVersionNumber:   m.AgentVersionNumber,
		ExecutionMode:        m.ExecutionMode,
		Status:               domain.CaseStatus(m.Status),
		ProposedDecision:     m.ProposedDecision,
		Confidence:           m.Confidence,
		ReliabilityThreshold: m.ReliabilityThreshold,
		EscalationReasons:    m.EscalationReasons,
		FrozenEvidence:       m.FrozenEvidence,
		Decision:             domain.ResolutionDecision(m.Decision),
		FinalDecision:        m.FinalDecision,
		ResolutionReason:     m.ResolutionReason,
		ReviewerID:           m.ReviewerID,
		AuditRecordID:        m.AuditRecordID,
		ResolvedAt:           m.ResolvedAt,
		CreatedAt:            m.CreatedAt,
		UpdatedAt:            m.UpdatedAt,
	}
}

// OutboxEventModel maps to the outbox_events table in PostgreSQL.
type OutboxEventModel struct {
	ID           string            `gorm:"column:id;primaryKey;type:varchar(64)"`
	TenantID     uuid.UUID         `gorm:"column:tenant_id;not null;type:uuid;index"`
	EventType    string            `gorm:"column:event_type;not null;type:varchar(128)"`
	Topic        string            `gorm:"column:topic;not null;type:varchar(128)"`
	Payload      map[string]any    `gorm:"column:payload;not null;serializer:json"`
	Headers      map[string]string `gorm:"column:headers;not null;serializer:json"`
	Status       string            `gorm:"column:status;not null;default:'PENDING';type:varchar(32);index"`
	RetryCount   int               `gorm:"column:retry_count;not null;default:0"`
	ErrorMessage string            `gorm:"column:error_message;type:text"`
	CreatedAt    time.Time         `gorm:"column:created_at;not null;type:timestamptz"`
	PublishedAt  *time.Time        `gorm:"column:published_at;type:timestamptz"`
}

// TableName returns the PostgreSQL table name.
func (OutboxEventModel) TableName() string {
	return "outbox_events"
}
