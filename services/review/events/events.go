package events

import (
	"time"

	"github.com/google/uuid"
)

const (
	// TopicReviewCreated is the Redpanda topic for review creation lifecycle events.
	TopicReviewCreated = "arda.review-lifecycle.v1"

	// TopicReviewResolved is the Redpanda topic for authoritative human resolutions.
	// Consumed by Reliability Service feedback loop and Audit pipeline.
	TopicReviewResolved = "arda.human-resolutions.v1"

	// Canonical event types
	EventTypeReviewCreated  = "arda.review.created"
	EventTypeReviewResolved = "arda.reliability.human_resolution"

	// Default producer metadata
	DefaultProducerID = "arda.human-review-service"
	DefaultKeyID      = "v1"
	EventVersionV1    = "v1"
)

// ReviewCreatedEvent is published when a review case is created.
type ReviewCreatedEvent struct {
	EventID              string    `json:"event_id"`
	EventType            string    `json:"event_type"`
	EventVersion         string    `json:"event_version"`
	TenantID             uuid.UUID `json:"tenant_id"`
	AgentID              uuid.UUID `json:"agent_id"`
	AgentName            string    `json:"agent_name"`
	CaseID               string    `json:"case_id"`
	ExecutionID          string    `json:"execution_id"`
	ExecutionMode        string    `json:"execution_mode"`
	ProposedDecision     string    `json:"proposed_decision"`
	Confidence           float64   `json:"confidence"`
	ReliabilityThreshold float64   `json:"reliability_threshold"`
	EscalationReasons    []string  `json:"escalation_reasons"`
	CreatedAt            time.Time `json:"created_at"`
	ProducerID           string    `json:"producer_id"`
}

// ReviewResolvedEvent is published when a review case is adjudicated by a human reviewer.
// Compatible with arda-reliability-service's HumanResolutionEventDTO.
type ReviewResolvedEvent struct {
	EventID                  string    `json:"event_id"`
	EventType                string    `json:"event_type"`
	EventVersion             string    `json:"event_version"`
	TenantID                 uuid.UUID `json:"tenant_id"`
	AgentID                  uuid.UUID `json:"agent_id"`
	CaseID                   string    `json:"case_id"`
	ExecutionID              string    `json:"execution_id,omitempty"`
	CorrelationID            string    `json:"correlation_id"`
	OriginalCaseDescription  string    `json:"original_case_description"`
	ProposedDecision         string    `json:"proposed_decision"`
	OriginalProposedDecision string    `json:"original_proposed_decision,omitempty"`
	HumanDecision            string    `json:"human_decision"`
	FinalDecision            string    `json:"final_decision,omitempty"`
	DecisionType             string    `json:"decision_type"`
	ResolutionReason         string    `json:"resolution_reason"`
	ReviewerID               string    `json:"reviewer_id"`
	ResolvedAt               time.Time `json:"resolved_at"`
	Timestamp                string    `json:"timestamp"`
	ExecutionMode            string    `json:"execution_mode"`
	ProducerID               string    `json:"producer_id"`
	KeyID                    string    `json:"key_id"`
	Signature                string    `json:"signature"`
}
