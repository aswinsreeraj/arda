package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aswinsreeraj/arda/services/review/domain"
	"github.com/aswinsreeraj/arda/services/review/events"
	"github.com/aswinsreeraj/arda/services/review/store"
	"github.com/google/uuid"
)

// HumanReviewSignalName is the canonical Temporal signal identifier for human adjudication.
const HumanReviewSignalName = "arda.human-review.resolution"

// TemporalSignaler defines the interface for signaling waiting durable workflows in Temporal.
type TemporalSignaler interface {
	SignalWorkflow(ctx context.Context, workflowID string, runID string, signalName string, arg any) error
}

// TemporalSignalerFunc adapts a function to the TemporalSignaler interface.
type TemporalSignalerFunc func(ctx context.Context, workflowID string, runID string, signalName string, arg any) error

func (f TemporalSignalerFunc) SignalWorkflow(ctx context.Context, workflowID string, runID string, signalName string, arg any) error {
	return f(ctx, workflowID, runID, signalName, arg)
}

// HumanResolutionSignal carries adjudication metadata delivered to waiting Temporal workflows.
type HumanResolutionSignal struct {
	CaseID           string    `json:"case_id"`
	Decision         string    `json:"decision"`
	FinalDecision    string    `json:"final_decision"`
	ResolutionReason string    `json:"resolution_reason"`
	ReviewerID       string    `json:"reviewer_id"`
	ResolvedAt       time.Time `json:"resolved_at"`
	AuditRecordID    string    `json:"audit_record_id"`
}

// Service implements core Human Review business logic.
type Service struct {
	store            store.Store
	temporalSignaler TemporalSignaler
	producerSecret   string
	outboxRelay      *events.OutboxRelay
}

// Option configures Service parameters.
type Option func(*Service)

// WithTemporalSignaler injects a Temporal signaling client.
func WithTemporalSignaler(ts TemporalSignaler) Option {
	return func(s *Service) {
		s.temporalSignaler = ts
	}
}

// WithProducerSecret configures the shared secret for HMAC-SHA256 canonical event signing.
func WithProducerSecret(secret string) Option {
	return func(s *Service) {
		if strings.TrimSpace(secret) != "" {
			s.producerSecret = strings.TrimSpace(secret)
		}
	}
}

// WithOutboxRelay configures an outbox relay to enable synchronous event dispatching on commit.
func WithOutboxRelay(relay *events.OutboxRelay) Option {
	return func(s *Service) {
		s.outboxRelay = relay
	}
}

// NewService constructs a Human Review Service.
func NewService(st store.Store, opts ...Option) *Service {
	s := &Service{
		store:          st,
		producerSecret: "arda-producer-secret-default",
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// CreateCase registers a new review case with frozen evidence and enqueues the review.created outbox event.
func (s *Service) CreateCase(ctx context.Context, req domain.CreateCaseRequest) (*domain.ReviewCase, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	caseID := strings.TrimSpace(req.CaseID)
	if caseID == "" {
		caseID = fmt.Sprintf("case_%s", uuid.New().String()[:12])
	}

	now := time.Now().UTC()
	rc := &domain.ReviewCase{
		ID:                   caseID,
		TenantID:             req.TenantID,
		ExecutionID:          req.ExecutionID,
		AgentID:              req.AgentID,
		AgentName:            req.AgentName,
		AgentVersionID:       req.AgentVersionID,
		AgentVersionNumber:   req.AgentVersionNumber,
		ExecutionMode:        req.ExecutionMode,
		Status:               domain.CaseStatusOpen,
		ProposedDecision:     req.ProposedDecision,
		Confidence:           req.Confidence,
		ReliabilityThreshold: req.ReliabilityThreshold,
		EscalationReasons:    req.EscalationReasons,
		FrozenEvidence:       req.FrozenEvidence,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	// Prepare review.created outbox event
	createdEv := events.ReviewCreatedEvent{
		EventID:              fmt.Sprintf("evt-created-%s", uuid.New().String()[:12]),
		EventType:            events.EventTypeReviewCreated,
		EventVersion:         events.EventVersionV1,
		TenantID:             rc.TenantID,
		AgentID:              rc.AgentID,
		AgentName:            rc.AgentName,
		CaseID:               rc.ID,
		ExecutionID:          rc.ExecutionID,
		ExecutionMode:        rc.ExecutionMode,
		ProposedDecision:     rc.ProposedDecision,
		Confidence:           rc.Confidence,
		ReliabilityThreshold: rc.ReliabilityThreshold,
		EscalationReasons:    rc.EscalationReasons,
		CreatedAt:            now,
		ProducerID:           events.DefaultProducerID,
	}

	var payloadMap map[string]any
	b, _ := json.Marshal(createdEv)
	_ = json.Unmarshal(b, &payloadMap)

	outboxModel := &store.OutboxEventModel{
		ID:        createdEv.EventID,
		TenantID:  rc.TenantID,
		EventType: events.EventTypeReviewCreated,
		Topic:     events.TopicReviewCreated,
		Payload:   payloadMap,
		Headers: map[string]string{
			"event_type": events.EventTypeReviewCreated,
			"case_id":    rc.ID,
		},
		Status:    "PENDING",
		CreatedAt: now,
	}

	if err := s.store.CreateCase(ctx, rc, outboxModel); err != nil {
		return nil, err
	}

	// Best-effort immediate outbox flush
	if s.outboxRelay != nil {
		go func() {
			flushCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = s.outboxRelay.Flush(flushCtx)
		}()
	}

	return rc, nil
}

// GetCase retrieves a review case with frozen evidence under strict tenant isolation.
func (s *Service) GetCase(ctx context.Context, tenantID uuid.UUID, caseID string) (*domain.ReviewCase, error) {
	if tenantID == uuid.Nil {
		return nil, domain.NewUnauthenticatedError("unauthenticated tenant context")
	}
	if strings.TrimSpace(caseID) == "" {
		return nil, domain.NewValidationError("case_id is required")
	}
	return s.store.GetCase(ctx, tenantID, strings.TrimSpace(caseID))
}

// ListCases retrieves tenant-scoped review cases with filtering and pagination.
func (s *Service) ListCases(ctx context.Context, filter domain.ListFilter) ([]domain.ReviewCaseSummary, string, int32, error) {
	if filter.TenantID == uuid.Nil {
		return nil, "", 0, domain.NewUnauthenticatedError("unauthenticated tenant context")
	}
	return s.store.ListCases(ctx, filter)
}

// ResolveCase adjudicates an open review case and performs authoritative atomic state transition.
func (s *Service) ResolveCase(ctx context.Context, req domain.ResolveCaseRequest) (*domain.ResolveCaseResult, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	// Retrieve existing case to verify tenant scope, open status, and extract original evidence
	existing, err := s.store.GetCase(ctx, req.TenantID, req.CaseID)
	if err != nil {
		return nil, err
	}

	if existing.Status != domain.CaseStatusOpen {
		return nil, domain.NewConflictError("review case is already resolved", domain.ErrCaseAlreadyResolved)
	}

	// Authoritative final decision logic
	var finalDecision string
	switch req.Decision {
	case domain.DecisionApprove:
		finalDecision = existing.ProposedDecision
	case domain.DecisionReject:
		finalDecision = "REJECT"
	case domain.DecisionOverride:
		finalDecision = strings.TrimSpace(req.FinalDecision)
	}

	now := time.Now().UTC()
	auditRecordID := fmt.Sprintf("aud_%s", uuid.New().String()[:8])
	eventID := fmt.Sprintf("evt-res-%s", uuid.New().String()[:12])

	// Original case description for feedback indexing
	desc := existing.FrozenEvidence.AgentReasoning
	if desc == "" && existing.FrozenEvidence.OriginalInput != nil {
		if promptVal, ok := existing.FrozenEvidence.OriginalInput["prompt"].(string); ok && promptVal != "" {
			desc = promptVal
		} else if inputVal, ok := existing.FrozenEvidence.OriginalInput["input"].(string); ok && inputVal != "" {
			desc = inputVal
		} else {
			b, _ := json.Marshal(existing.FrozenEvidence.OriginalInput)
			desc = string(b)
		}
	}
	if desc == "" {
		desc = fmt.Sprintf("Case %s adjudicated by %s", existing.ID, req.ReviewerID)
	}

	resEvent := events.ReviewResolvedEvent{
		EventID:                  eventID,
		EventType:                events.EventTypeReviewResolved,
		EventVersion:             events.EventVersionV1,
		TenantID:                 existing.TenantID,
		AgentID:                  existing.AgentID,
		CaseID:                   existing.ID,
		ExecutionID:              existing.ExecutionID,
		CorrelationID:            existing.ExecutionID,
		OriginalCaseDescription:  desc,
		ProposedDecision:         existing.ProposedDecision,
		OriginalProposedDecision: existing.ProposedDecision,
		HumanDecision:            finalDecision,
		FinalDecision:            finalDecision,
		DecisionType:             string(req.Decision),
		ResolutionReason:         req.Reason,
		ReviewerID:               req.ReviewerID,
		ResolvedAt:               now,
		Timestamp:                events.FormatCanonicalTimestamp(now),
		ExecutionMode:            existing.ExecutionMode,
		ProducerID:               events.DefaultProducerID,
		KeyID:                    events.DefaultKeyID,
	}

	// Compute HMAC-SHA256 signature
	if err := events.SignResolutionEvent(&resEvent, s.producerSecret); err != nil {
		return nil, fmt.Errorf("failed to sign resolution event: %w", err)
	}

	var payloadMap map[string]any
	b, _ := json.Marshal(resEvent)
	_ = json.Unmarshal(b, &payloadMap)

	outboxModel := &store.OutboxEventModel{
		ID:        eventID,
		TenantID:  existing.TenantID,
		EventType: events.EventTypeReviewResolved,
		Topic:     events.TopicReviewResolved,
		Payload:   payloadMap,
		Headers: map[string]string{
			"event_type": events.EventTypeReviewResolved,
			"case_id":    existing.ID,
			"signature":  resEvent.Signature,
		},
		Status:    "PENDING",
		CreatedAt: now,
	}

	// Atomic database commit
	resolvedCase, err := s.store.ResolveCase(ctx, req, auditRecordID, finalDecision, outboxModel)
	if err != nil {
		return nil, err
	}

	// 1. Signal waiting Temporal workflow if durable execution
	if s.temporalSignaler != nil && isDurableExecution(resolvedCase.ExecutionMode) {
		signalPayload := HumanResolutionSignal{
			CaseID:           resolvedCase.ID,
			Decision:         string(resolvedCase.Decision),
			FinalDecision:    resolvedCase.FinalDecision,
			ResolutionReason: resolvedCase.ResolutionReason,
			ReviewerID:       resolvedCase.ReviewerID,
			ResolvedAt:       now,
			AuditRecordID:    auditRecordID,
		}

		workflowID := resolvedCase.ExecutionID
		sigCtx, sigCancel := context.WithTimeout(ctx, 10*time.Second)
		defer sigCancel()
		if err := s.temporalSignaler.SignalWorkflow(sigCtx, workflowID, "", HumanReviewSignalName, signalPayload); err != nil {
			log.Printf("WARN: failed to signal Temporal workflow %s: %v", workflowID, err)
		}
	}

	// 2. Best-effort immediate outbox relay flush
	if s.outboxRelay != nil {
		go func() {
			flushCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = s.outboxRelay.Flush(flushCtx)
		}()
	}

	return &domain.ResolveCaseResult{
		CaseID:        resolvedCase.ID,
		Status:        "resolved",
		FinalDecision: resolvedCase.FinalDecision,
		AuditRecordID: auditRecordID,
		ResolvedAt:    now,
	}, nil
}

func isDurableExecution(mode string) bool {
	m := strings.ToLower(strings.TrimSpace(mode))
	return m == "durable" || m == "async"
}
