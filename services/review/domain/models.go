package domain

import (
	"time"

	"github.com/google/uuid"
)

// CaseStatus represents the explicit lifecycle state of a review case.
type CaseStatus string

const (
	CaseStatusOpen     CaseStatus = "OPEN"
	CaseStatusResolved CaseStatus = "RESOLVED"
)

// SupportedCaseStatuses contains all valid review case states.
var SupportedCaseStatuses = map[CaseStatus]struct{}{
	CaseStatusOpen:     {},
	CaseStatusResolved: {},
}

// ResolutionDecision represents the action taken by a human reviewer.
type ResolutionDecision string

const (
	DecisionApprove  ResolutionDecision = "approve"
	DecisionReject   ResolutionDecision = "reject"
	DecisionOverride ResolutionDecision = "override"
)

// SupportedDecisions contains all valid resolution decisions.
var SupportedDecisions = map[ResolutionDecision]struct{}{
	DecisionApprove:  {},
	DecisionReject:   {},
	DecisionOverride: {},
}

// ReferenceNeighbor records an individual neighbor example retrieved by the Reliability Service.
type ReferenceNeighbor struct {
	ReferenceExampleID string  `json:"reference_example_id"`
	Label              string  `json:"label"`
	Similarity         float64 `json:"similarity"`
}

// ToolCallEvidence records an executed tool invocation snapshot.
type ToolCallEvidence struct {
	ToolID     string         `json:"tool_id"`
	ToolName   string         `json:"tool_name"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	Result     map[string]any `json:"result,omitempty"`
	Output     string         `json:"output,omitempty"`
	Error      string         `json:"error,omitempty"`
	Allowed    bool           `json:"allowed"`
	DenialCode string         `json:"denial_code,omitempty"`
	DurationMs int64          `json:"duration_ms"`
}

// FrozenEvidence captures the immutable execution snapshot at the moment of escalation.
type FrozenEvidence struct {
	OriginalInput        map[string]any      `json:"original_input,omitempty"`
	AgentReasoning       string              `json:"agent_reasoning,omitempty"`
	ProposedDecision     string              `json:"proposed_decision,omitempty"`
	Confidence           float64             `json:"confidence"`
	ReliabilityThreshold float64             `json:"reliability_threshold"`
	EscalationReasons    []string            `json:"escalation_reasons,omitempty"`
	Neighbors            []ReferenceNeighbor `json:"neighbors,omitempty"`
	ToolCalls            []ToolCallEvidence  `json:"tool_calls,omitempty"`
	Metadata             map[string]string   `json:"metadata,omitempty"`
}

// ReviewCase represents an auditable human review case.
type ReviewCase struct {
	ID                   string         `json:"id"`
	TenantID             uuid.UUID      `json:"tenant_id"`
	ExecutionID          string         `json:"execution_id"`
	AgentID              uuid.UUID      `json:"agent_id"`
	AgentName            string         `json:"agent_name"`
	AgentVersionID       uuid.UUID      `json:"agent_version_id"`
	AgentVersionNumber   int            `json:"agent_version_number"`
	ExecutionMode        string         `json:"execution_mode"` // "realtime" or "durable"
	Status               CaseStatus     `json:"status"`
	ProposedDecision     string         `json:"proposed_decision"`
	Confidence           float64        `json:"confidence"`
	ReliabilityThreshold float64        `json:"reliability_threshold"`
	EscalationReasons    []string       `json:"escalation_reasons"`
	FrozenEvidence       FrozenEvidence `json:"frozen_evidence"`

	// Human resolution outcomes
	Decision         ResolutionDecision `json:"decision,omitempty"`
	FinalDecision    string             `json:"final_decision,omitempty"`
	ResolutionReason string             `json:"resolution_reason,omitempty"`
	ReviewerID       string             `json:"reviewer_id,omitempty"`
	AuditRecordID    string             `json:"audit_record_id,omitempty"`
	ResolvedAt       *time.Time         `json:"resolved_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ReviewCaseSummary provides compact metadata for queue listing.
type ReviewCaseSummary struct {
	CaseID            string     `json:"case_id"`
	ExecutionID       string     `json:"execution_id"`
	AgentID           uuid.UUID  `json:"agent_id"`
	AgentName         string     `json:"agent_name"`
	ProposedDecision  string     `json:"proposed_decision"`
	Confidence        float64    `json:"confidence"`
	EscalationReasons []string   `json:"escalation_reasons"`
	Status            CaseStatus `json:"status"`
	CreatedAt         time.Time  `json:"created_at"`
}

// CreateCaseRequest encapsulates input for creating a review case.
type CreateCaseRequest struct {
	CaseID               string         `json:"case_id,omitempty"`
	TenantID             uuid.UUID      `json:"tenant_id"`
	ExecutionID          string         `json:"execution_id"`
	AgentID              uuid.UUID      `json:"agent_id"`
	AgentName            string         `json:"agent_name"`
	AgentVersionID       uuid.UUID      `json:"agent_version_id"`
	AgentVersionNumber   int            `json:"agent_version_number"`
	ExecutionMode        string         `json:"execution_mode"`
	ProposedDecision     string         `json:"proposed_decision"`
	Confidence           float64        `json:"confidence"`
	ReliabilityThreshold float64        `json:"reliability_threshold"`
	EscalationReasons    []string       `json:"escalation_reasons"`
	FrozenEvidence       FrozenEvidence `json:"frozen_evidence"`
}

// ResolveCaseRequest encapsulates parameters for adjudicating an open review case.
type ResolveCaseRequest struct {
	TenantID      uuid.UUID          `json:"tenant_id"`
	CaseID        string             `json:"case_id"`
	Decision      ResolutionDecision `json:"decision"`
	FinalDecision string             `json:"final_decision,omitempty"`
	Reason        string             `json:"reason"`
	ReviewerID    string             `json:"reviewer_id"`
}

// ResolveCaseResult contains response metadata after atomic resolution.
type ResolveCaseResult struct {
	CaseID        string    `json:"case_id"`
	Status        string    `json:"status"`
	FinalDecision string    `json:"final_decision"`
	AuditRecordID string    `json:"audit_record_id"`
	ResolvedAt    time.Time `json:"resolved_at"`
}

// ListFilter defines parameters for tenant-scoped review case querying.
type ListFilter struct {
	TenantID         uuid.UUID  `json:"tenant_id"`
	Status           CaseStatus `json:"status,omitempty"`
	AgentID          uuid.UUID  `json:"agent_id,omitempty"`
	EscalationReason string     `json:"escalation_reason,omitempty"`
	PageSize         int32      `json:"page_size"`
	PageToken        string     `json:"page_token"`
}
