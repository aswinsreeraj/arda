package domain

import (
	"strings"

	"github.com/google/uuid"
)

// Validate validates CreateCaseRequest parameters.
func (r *CreateCaseRequest) Validate() error {
	if r.TenantID == uuid.Nil {
		return NewValidationError("tenant_id is required")
	}
	if strings.TrimSpace(r.ExecutionID) == "" {
		return NewValidationError("execution_id is required")
	}
	if r.AgentID == uuid.Nil {
		return NewValidationError("agent_id is required")
	}
	if r.AgentVersionID == uuid.Nil {
		return NewValidationError("agent_version_id is required")
	}
	// Agents may only propose APPROVE or REJECT (or leave empty if escalated without a decision).
	// Escalation is a routing action, never a proposed decision.
	trimmedProposed := strings.TrimSpace(r.ProposedDecision)
	if strings.EqualFold(trimmedProposed, "ESCALATE") {
		r.ProposedDecision = ""
	} else {
		r.ProposedDecision = trimmedProposed
	}
	if strings.TrimSpace(r.ExecutionMode) == "" {
		r.ExecutionMode = "realtime"
	}
	return nil
}

// Validate validates ResolveCaseRequest parameters.
func (r *ResolveCaseRequest) Validate() error {
	if r.TenantID == uuid.Nil {
		return NewValidationError("tenant_id is required")
	}
	if strings.TrimSpace(r.CaseID) == "" {
		return NewValidationError("case_id is required")
	}
	if strings.TrimSpace(r.ReviewerID) == "" {
		return NewValidationError("reviewer_id is required")
	}

	normDecision := ResolutionDecision(strings.ToLower(strings.TrimSpace(string(r.Decision))))
	if _, ok := SupportedDecisions[normDecision]; !ok {
		return NewValidationError("invalid decision: must be 'approve', 'reject', or 'override'")
	}
	r.Decision = normDecision

	trimmedReason := strings.TrimSpace(r.Reason)
	if trimmedReason == "" {
		return NewValidationError("resolution reason is mandatory")
	}
	r.Reason = trimmedReason

	if r.Decision == DecisionOverride {
		trimmedFinal := strings.TrimSpace(r.FinalDecision)
		if trimmedFinal == "" {
			return NewValidationError("final_decision is required when decision is 'override'")
		}
		r.FinalDecision = trimmedFinal
	}

	return nil
}
