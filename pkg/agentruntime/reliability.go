package agentruntime

import (
	"context"
	"fmt"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
)

// ReliabilityDecision represents the typed outcome of a reliability evaluation.
// It uses mutually exclusive enum states rather than contradictory boolean combinations.
type ReliabilityDecision string

const (
	// ReliabilityAllow indicates that the proposed action or decision meets calibrated reliability criteria and execution may proceed.
	ReliabilityAllow ReliabilityDecision = "ALLOW"

	// ReliabilityEscalate indicates that confidence or policy requires human or supervisor review.
	ReliabilityEscalate ReliabilityDecision = "ESCALATE"

	// ReliabilityReject indicates that the proposed action or decision violates safety/policy and is rejected.
	ReliabilityReject ReliabilityDecision = "REJECT"
)

// SupportedReliabilityDecisions contains the valid ReliabilityDecision values.
var SupportedReliabilityDecisions = map[ReliabilityDecision]struct{}{
	ReliabilityAllow:    {},
	ReliabilityEscalate: {},
	ReliabilityReject:   {},
}

// ReliabilityEvaluation represents the structured result of consulting the ReliabilityService.
type ReliabilityEvaluation struct {
	Decision   ReliabilityDecision `json:"decision"`
	Confidence float64             `json:"confidence,omitempty"`
	ReasonCode string              `json:"reason_code,omitempty"`
	Reason     string              `json:"reason,omitempty"`
}

// Validate verifies that the evaluation contains a valid decision.
func (e ReliabilityEvaluation) Validate() error {
	if _, ok := SupportedReliabilityDecisions[e.Decision]; !ok {
		return fmt.Errorf("invalid reliability decision: %q", e.Decision)
	}
	return nil
}

// ReliabilityRequest carries the context required to evaluate an agent's proposed action or decision.
type ReliabilityRequest struct {
	TenantID             uuid.UUID            `json:"tenant_id"`
	AgentID              uuid.UUID            `json:"agent_id"`
	AgentVersionID       uuid.UUID            `json:"agent_version_id"`
	ExecutionID          string               `json:"execution_id,omitempty"`
	ExecutionMode        domain.ExecutionMode `json:"execution_mode,omitempty"`
	ReliabilityThreshold float64              `json:"reliability_threshold"`
	CaseDescription      string               `json:"case_description,omitempty"`
	ProposedDecision     string               `json:"proposed_decision,omitempty"`
	ActionType           string               `json:"action_type,omitempty"` // "decision" or "tool_call"
	ActionPayload        any                  `json:"action_payload,omitempty"`
}

// ReliabilityService defines the narrow boundary for evaluating actions and decisions against reliability gates.
// It is transport-independent and supports fail-closed semantics for governance.
type ReliabilityService interface {
	Evaluate(ctx context.Context, req ReliabilityRequest) (ReliabilityEvaluation, error)
}

// MockReliabilityService provides a deterministic mock implementation for unit testing.
type MockReliabilityService struct {
	EvaluateFunc func(ctx context.Context, req ReliabilityRequest) (ReliabilityEvaluation, error)
}

// Evaluate delegates to EvaluateFunc if set, or returns an allow decision.
func (m *MockReliabilityService) Evaluate(ctx context.Context, req ReliabilityRequest) (ReliabilityEvaluation, error) {
	if m == nil {
		return ReliabilityEvaluation{}, ErrNilReliabilityService
	}
	if m.EvaluateFunc != nil {
		return m.EvaluateFunc(ctx, req)
	}
	return ReliabilityEvaluation{
		Decision:   ReliabilityAllow,
		Confidence: 1.0,
		ReasonCode: "MOCK_ALLOW",
		Reason:     "Mock reliability service default allow",
	}, nil
}
