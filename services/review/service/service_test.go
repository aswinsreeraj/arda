package service

import (
	"context"
	"sync"
	"testing"

	"github.com/aswinsreeraj/arda/services/review/domain"
	"github.com/aswinsreeraj/arda/services/review/events"
	"github.com/aswinsreeraj/arda/services/review/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockTemporalSignaler struct {
	mu      sync.Mutex
	Signals []signalRecord
}

type signalRecord struct {
	WorkflowID string
	RunID      string
	SignalName string
	Arg        any
}

func (m *mockTemporalSignaler) SignalWorkflow(ctx context.Context, workflowID string, runID string, signalName string, arg any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Signals = append(m.Signals, signalRecord{
		WorkflowID: workflowID,
		RunID:      runID,
		SignalName: signalName,
		Arg:        arg,
	})
	return nil
}

func setupTestService() (*Service, *store.MemoryStore, *mockTemporalSignaler, *events.MockProducer) {
	st := store.NewMemoryStore()
	mockSig := &mockTemporalSignaler{}
	mockProd := events.NewMockProducer()
	relay := events.NewOutboxRelay(st, mockProd)
	svc := NewService(
		st,
		WithTemporalSignaler(mockSig),
		WithProducerSecret("test-secret"),
		WithOutboxRelay(relay),
	)
	return svc, st, mockSig, mockProd
}

func TestReviewService_CreateCase(t *testing.T) {
	svc, st, _, _ := setupTestService()
	ctx := context.Background()

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	req := domain.CreateCaseRequest{
		TenantID:             tenantID,
		ExecutionID:          "exec-123",
		AgentID:              agentID,
		AgentName:            "RefundAgent",
		AgentVersionID:       versionID,
		AgentVersionNumber:   1,
		ExecutionMode:        "realtime",
		ProposedDecision:     "APPROVE",
		Confidence:           0.65,
		ReliabilityThreshold: 0.85,
		EscalationReasons:    []string{"low confidence", "amount exceeds threshold"},
		FrozenEvidence: domain.FrozenEvidence{
			OriginalInput:        map[string]any{"amount": 1500, "user_id": "u1"},
			AgentReasoning:       "Customer is high value, but amount exceeds limit",
			ProposedDecision:     "APPROVE",
			Confidence:           0.65,
			ReliabilityThreshold: 0.85,
			EscalationReasons:    []string{"low confidence", "amount exceeds threshold"},
			Neighbors: []domain.ReferenceNeighbor{
				{ReferenceExampleID: "ref-1", Label: "REJECT", Similarity: 0.88},
			},
			ToolCalls: []domain.ToolCallEvidence{
				{ToolID: "get_balance", ToolName: "get_balance", Output: `{"balance": 2000}`, Allowed: true},
			},
		},
	}

	created, err := svc.CreateCase(ctx, req)
	require.NoError(t, err)
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, domain.CaseStatusOpen, created.Status)
	assert.Equal(t, tenantID, created.TenantID)
	assert.Equal(t, "APPROVE", created.ProposedDecision)
	assert.Equal(t, 0.65, created.Confidence)
	assert.Len(t, created.FrozenEvidence.Neighbors, 1)
	assert.Len(t, created.FrozenEvidence.ToolCalls, 1)

	// Verify outbox record was enqueued for review.created
	outbox, err := st.FetchPendingOutboxEvents(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, outbox, 1)
	assert.Equal(t, events.EventTypeReviewCreated, outbox[0].EventType)
	assert.Equal(t, events.TopicReviewCreated, outbox[0].Topic)
}

func TestReviewService_ListCasesAndFilters(t *testing.T) {
	svc, _, _, _ := setupTestService()
	ctx := context.Background()

	tenantA := uuid.New()
	tenantB := uuid.New()
	agent1 := uuid.New()
	agent2 := uuid.New()

	// Seed cases for Tenant A
	_, err := svc.CreateCase(ctx, domain.CreateCaseRequest{
		TenantID:          tenantA,
		ExecutionID:       "exec-A1",
		AgentID:           agent1,
		AgentName:         "Agent1",
		AgentVersionID:    uuid.New(),
		ProposedDecision:  "APPROVE",
		EscalationReasons: []string{"flag_fraud"},
	})
	require.NoError(t, err)

	_, err = svc.CreateCase(ctx, domain.CreateCaseRequest{
		TenantID:          tenantA,
		ExecutionID:       "exec-A2",
		AgentID:           agent2,
		AgentName:         "Agent2",
		AgentVersionID:    uuid.New(),
		ProposedDecision:  "REJECT",
		EscalationReasons: []string{"unsupported_region"},
	})
	require.NoError(t, err)

	// Seed case for Tenant B
	_, err = svc.CreateCase(ctx, domain.CreateCaseRequest{
		TenantID:          tenantB,
		ExecutionID:       "exec-B1",
		AgentID:           agent1,
		AgentName:         "Agent1",
		AgentVersionID:    uuid.New(),
		ProposedDecision:  "APPROVE",
		EscalationReasons: []string{"flag_fraud"},
	})
	require.NoError(t, err)

	// 1. List for Tenant A: expect 2
	casesA, _, totalA, err := svc.ListCases(ctx, domain.ListFilter{TenantID: tenantA})
	require.NoError(t, err)
	assert.Equal(t, int32(2), totalA)
	assert.Len(t, casesA, 2)

	// 2. Filter by Agent 1 for Tenant A: expect 1
	casesAgent1, _, totalAgent1, err := svc.ListCases(ctx, domain.ListFilter{TenantID: tenantA, AgentID: agent1})
	require.NoError(t, err)
	assert.Equal(t, int32(1), totalAgent1)
	assert.Equal(t, "Agent1", casesAgent1[0].AgentName)

	// 3. Filter by escalation reason: expect 1
	casesFraud, _, totalFraud, err := svc.ListCases(ctx, domain.ListFilter{TenantID: tenantA, EscalationReason: "fraud"})
	require.NoError(t, err)
	assert.Equal(t, int32(1), totalFraud)
	assert.Equal(t, "exec-A1", casesFraud[0].ExecutionID)

	// 4. Tenant B isolation: Tenant B only sees 1 case
	casesB, _, totalB, err := svc.ListCases(ctx, domain.ListFilter{TenantID: tenantB})
	require.NoError(t, err)
	assert.Equal(t, int32(1), totalB)
	assert.Equal(t, "exec-B1", casesB[0].ExecutionID)
}

func TestReviewService_ResolutionSemantics(t *testing.T) {
	svc, st, mockSig, mockProd := setupTestService()
	ctx := context.Background()

	tenantID := uuid.New()
	agentID := uuid.New()

	// Case 1: Approve
	case1, err := svc.CreateCase(ctx, domain.CreateCaseRequest{
		TenantID:         tenantID,
		ExecutionID:      "exec-durable-1",
		AgentID:          agentID,
		AgentName:        "Agent1",
		AgentVersionID:   uuid.New(),
		ExecutionMode:    "durable",
		ProposedDecision: "APPROVE",
		FrozenEvidence: domain.FrozenEvidence{
			AgentReasoning: "Customer account verified.",
		},
	})
	require.NoError(t, err)

	res1, err := svc.ResolveCase(ctx, domain.ResolveCaseRequest{
		TenantID:   tenantID,
		CaseID:     case1.ID,
		Decision:   domain.DecisionApprove,
		Reason:     "Auditor confirms valid documentation.",
		ReviewerID: "reviewer_bob",
	})
	require.NoError(t, err)
	assert.Equal(t, "resolved", res1.Status)
	assert.Equal(t, "APPROVE", res1.FinalDecision)
	assert.NotEmpty(t, res1.AuditRecordID)

	// Verify case is now RESOLVED in store
	stored1, err := st.GetCase(ctx, tenantID, case1.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.CaseStatusResolved, stored1.Status)
	assert.Equal(t, "APPROVE", stored1.FinalDecision)
	assert.Equal(t, "reviewer_bob", stored1.ReviewerID)

	// Verify Temporal signal sent for durable execution
	assert.NotEmpty(t, mockSig.Signals)
	lastSig := mockSig.Signals[len(mockSig.Signals)-1]
	assert.Equal(t, "exec-durable-1", lastSig.WorkflowID)
	assert.Equal(t, HumanReviewSignalName, lastSig.SignalName)
	sigPayload := lastSig.Arg.(HumanResolutionSignal)
	assert.Equal(t, "APPROVE", sigPayload.FinalDecision)

	// Case 2: Reject
	case2, err := svc.CreateCase(ctx, domain.CreateCaseRequest{
		TenantID:         tenantID,
		ExecutionID:      "exec-realtime-2",
		AgentID:          agentID,
		AgentName:        "Agent1",
		AgentVersionID:   uuid.New(),
		ExecutionMode:    "realtime",
		ProposedDecision: "APPROVE",
		FrozenEvidence: domain.FrozenEvidence{
			AgentReasoning: "Transaction flagged.",
		},
	})
	require.NoError(t, err)

	sigCountBefore := len(mockSig.Signals)
	res2, err := svc.ResolveCase(ctx, domain.ResolveCaseRequest{
		TenantID:   tenantID,
		CaseID:     case2.ID,
		Decision:   domain.DecisionReject,
		Reason:     "Policy violation confirmed.",
		ReviewerID: "reviewer_bob",
	})
	require.NoError(t, err)
	assert.Equal(t, "REJECT", res2.FinalDecision)

	// Realtime execution must NOT signal Temporal
	assert.Equal(t, sigCountBefore, len(mockSig.Signals), "Realtime execution must not signal Temporal")

	// Case 3: Override
	case3, err := svc.CreateCase(ctx, domain.CreateCaseRequest{
		TenantID:         tenantID,
		ExecutionID:      "exec-durable-3",
		AgentID:          agentID,
		AgentName:        "Agent1",
		AgentVersionID:   uuid.New(),
		ExecutionMode:    "durable",
		ProposedDecision: "APPROVE",
	})
	require.NoError(t, err)

	res3, err := svc.ResolveCase(ctx, domain.ResolveCaseRequest{
		TenantID:      tenantID,
		CaseID:        case3.ID,
		Decision:      domain.DecisionOverride,
		FinalDecision: "HOLD",
		Reason:        "Merchant is on watchlist, customer unreachable.",
		ReviewerID:    "reviewer_alice",
	})
	require.NoError(t, err)
	assert.Equal(t, "HOLD", res3.FinalDecision)

	// Flush outbox events and check mock producer
	flushed, err := events.NewOutboxRelay(st, mockProd).Flush(ctx)
	require.NoError(t, err)
	assert.True(t, flushed >= 3)
}

func TestReviewService_ResolutionValidationAndPreventDoubleResolution(t *testing.T) {
	svc, _, _, _ := setupTestService()
	ctx := context.Background()

	tenantID := uuid.New()
	c, err := svc.CreateCase(ctx, domain.CreateCaseRequest{
		TenantID:         tenantID,
		ExecutionID:      "exec-val-1",
		AgentID:          uuid.New(),
		AgentName:        "Agent1",
		AgentVersionID:   uuid.New(),
		ProposedDecision: "APPROVE",
	})
	require.NoError(t, err)

	// 1. Missing reason
	_, err = svc.ResolveCase(ctx, domain.ResolveCaseRequest{
		TenantID:   tenantID,
		CaseID:     c.ID,
		Decision:   domain.DecisionApprove,
		Reason:     "",
		ReviewerID: "rev1",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mandatory")

	// 2. Override without final_decision
	_, err = svc.ResolveCase(ctx, domain.ResolveCaseRequest{
		TenantID:      tenantID,
		CaseID:        c.ID,
		Decision:      domain.DecisionOverride,
		FinalDecision: "",
		Reason:        "Valid reason",
		ReviewerID:    "rev1",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "final_decision is required")

	// 3. Successful resolution
	_, err = svc.ResolveCase(ctx, domain.ResolveCaseRequest{
		TenantID:   tenantID,
		CaseID:     c.ID,
		Decision:   domain.DecisionApprove,
		Reason:     "Looks good",
		ReviewerID: "rev1",
	})
	require.NoError(t, err)

	// 4. Double resolution must fail
	_, err = svc.ResolveCase(ctx, domain.ResolveCaseRequest{
		TenantID:   tenantID,
		CaseID:     c.ID,
		Decision:   domain.DecisionReject,
		Reason:     "Trying to reject after approve",
		ReviewerID: "rev2",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already resolved")
}

func TestReviewService_TenantIsolation(t *testing.T) {
	svc, _, _, _ := setupTestService()
	ctx := context.Background()

	tenantA := uuid.New()
	tenantB := uuid.New()

	caseA, err := svc.CreateCase(ctx, domain.CreateCaseRequest{
		TenantID:         tenantA,
		ExecutionID:      "exec-iso-1",
		AgentID:          uuid.New(),
		AgentName:        "AgentA",
		AgentVersionID:   uuid.New(),
		ProposedDecision: "APPROVE",
	})
	require.NoError(t, err)

	// Tenant B cannot retrieve Tenant A's case
	_, err = svc.GetCase(ctx, tenantB, caseA.ID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Tenant B cannot resolve Tenant A's case
	_, err = svc.ResolveCase(ctx, domain.ResolveCaseRequest{
		TenantID:   tenantB,
		CaseID:     caseA.ID,
		Decision:   domain.DecisionApprove,
		Reason:     "Attempt cross-tenant resolution",
		ReviewerID: "hacker_bob",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
