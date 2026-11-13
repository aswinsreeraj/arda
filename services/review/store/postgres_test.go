package store_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/database"
	"github.com/aswinsreeraj/arda/services/review/domain"
	"github.com/aswinsreeraj/arda/services/review/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getTestPostgresStore(t *testing.T) (*database.DB, *store.PostgresStore) {
	cfg := database.LoadConfigFromEnv()
	db, err := database.Connect(cfg)
	if err != nil {
		t.Skipf("skipping store integration test: cannot connect to postgres: %v", err)
	}
	st := store.NewPostgresStore(db.DB)
	return db, st
}

func createTestTenantAndAgent(t *testing.T, db *database.DB) (uuid.UUID, uuid.UUID) {
	tenantID := uuid.New()
	err := db.Exec(`INSERT INTO tenants (id, name, status) VALUES (?, ?, 'active')`, tenantID, fmt.Sprintf("Tenant %s", tenantID.String()[:8])).Error
	require.NoError(t, err)

	agentID := uuid.New()
	err = db.Exec(`INSERT INTO agents (id, tenant_id, name) VALUES (?, ?, ?)`, agentID, tenantID, fmt.Sprintf("Agent %s", agentID.String()[:8])).Error
	require.NoError(t, err)

	return tenantID, agentID
}

func TestPostgresStore_LifecycleAndFrozenEvidence(t *testing.T) {
	db, st := getTestPostgresStore(t)
	defer db.Close()
	ctx := context.Background()

	tenantID, agentID := createTestTenantAndAgent(t, db)
	versionID := uuid.New()

	caseID := fmt.Sprintf("case_pg_%s", uuid.New().String()[:8])
	outboxID := fmt.Sprintf("evt_pg_%s", uuid.New().String()[:8])

	evidence := domain.FrozenEvidence{
		OriginalInput:        map[string]any{"account_id": "acc-999", "amount": 2500.0},
		AgentReasoning:       "Over credit limit; recommended escalate.",
		ProposedDecision:     "REJECT",
		Confidence:           0.42,
		ReliabilityThreshold: 0.85,
		EscalationReasons:    []string{"low_confidence", "credit_limit_exceeded"},
		Neighbors: []domain.ReferenceNeighbor{
			{ReferenceExampleID: "ref-101", Label: "REJECT", Similarity: 0.94},
			{ReferenceExampleID: "ref-102", Label: "REJECT", Similarity: 0.91},
		},
		ToolCalls: []domain.ToolCallEvidence{
			{
				ToolID:     "check_credit",
				ToolName:   "check_credit",
				Arguments:  map[string]any{"account_id": "acc-999"},
				Result:     map[string]any{"limit": 1000.0},
				Output:     `{"limit": 1000.0}`,
				Allowed:    true,
				DurationMs: 45,
			},
		},
	}

	rc := &domain.ReviewCase{
		ID:                   caseID,
		TenantID:             tenantID,
		ExecutionID:          "exec-pg-001",
		AgentID:              agentID,
		AgentName:            "CreditAgent",
		AgentVersionID:       versionID,
		AgentVersionNumber:   1,
		ExecutionMode:        "realtime",
		Status:               domain.CaseStatusOpen,
		ProposedDecision:     "REJECT",
		Confidence:           0.42,
		ReliabilityThreshold: 0.85,
		EscalationReasons:    evidence.EscalationReasons,
		FrozenEvidence:       evidence,
	}

	outbox := &store.OutboxEventModel{
		ID:        outboxID,
		TenantID:  tenantID,
		EventType: "arda.review.created",
		Topic:     "arda.review-lifecycle.v1",
		Payload:   map[string]any{"case_id": caseID, "status": "OPEN"},
		Headers:   map[string]string{"case_id": caseID},
		Status:    "PENDING",
	}

	// 1. CreateCase
	err := st.CreateCase(ctx, rc, outbox)
	require.NoError(t, err)

	// 2. GetCase & verify frozen evidence is intact
	fetched, err := st.GetCase(ctx, tenantID, caseID)
	require.NoError(t, err)
	assert.Equal(t, caseID, fetched.ID)
	assert.Equal(t, domain.CaseStatusOpen, fetched.Status)
	assert.Equal(t, 0.42, fetched.Confidence)
	assert.Len(t, fetched.FrozenEvidence.Neighbors, 2)
	assert.Equal(t, "ref-101", fetched.FrozenEvidence.Neighbors[0].ReferenceExampleID)
	assert.Len(t, fetched.FrozenEvidence.ToolCalls, 1)
	assert.Equal(t, "check_credit", fetched.FrozenEvidence.ToolCalls[0].ToolName)

	// 3. ListCases
	summaries, _, total, err := st.ListCases(ctx, domain.ListFilter{TenantID: tenantID, Status: domain.CaseStatusOpen})
	require.NoError(t, err)
	assert.True(t, total >= 1)
	assert.NotEmpty(t, summaries)

	// 4. ResolveCase
	resolveReq := domain.ResolveCaseRequest{
		TenantID:      tenantID,
		CaseID:        caseID,
		Decision:      domain.DecisionOverride,
		FinalDecision: "APPROVE_EXCEPTION",
		Reason:        "Executive exception granted by VP.",
		ReviewerID:    "auditor_sarah",
	}

	resOutbox := &store.OutboxEventModel{
		ID:        fmt.Sprintf("evt_res_%s", uuid.New().String()[:8]),
		TenantID:  tenantID,
		EventType: "arda.reliability.human_resolution",
		Topic:     "arda.human-resolutions.v1",
		Payload:   map[string]any{"case_id": caseID, "final_decision": "APPROVE_EXCEPTION"},
		Headers:   map[string]string{"case_id": caseID},
		Status:    "PENDING",
	}

	resolved, err := st.ResolveCase(ctx, resolveReq, "aud_pg_101", "APPROVE_EXCEPTION", resOutbox)
	require.NoError(t, err)
	assert.Equal(t, domain.CaseStatusResolved, resolved.Status)
	assert.Equal(t, "APPROVE_EXCEPTION", resolved.FinalDecision)
	assert.Equal(t, "auditor_sarah", resolved.ReviewerID)
	assert.Equal(t, "aud_pg_101", resolved.AuditRecordID)
	assert.NotNil(t, resolved.ResolvedAt)

	// 5. Verify double resolution fails
	_, err = st.ResolveCase(ctx, resolveReq, "aud_pg_102", "APPROVE_EXCEPTION", nil)
	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrCaseAlreadyResolved)

	// 6. Verify outbox records
	pending, err := st.FetchPendingOutboxEvents(ctx, 100)
	require.NoError(t, err)
	var foundCreation, foundResolution bool
	for _, p := range pending {
		if p.ID == outboxID {
			foundCreation = true
		}
		if p.ID == resOutbox.ID {
			foundResolution = true
		}
	}
	assert.True(t, foundCreation, "created outbox event must be in outbox_events")
	assert.True(t, foundResolution, "resolved outbox event must be in outbox_events")

	// Mark published
	err = st.MarkOutboxEventPublished(ctx, resOutbox.ID)
	require.NoError(t, err)
}

func TestPostgresStore_ConcurrentResolutionSafety(t *testing.T) {
	db, st := getTestPostgresStore(t)
	defer db.Close()
	ctx := context.Background()

	tenantID, agentID := createTestTenantAndAgent(t, db)
	caseID := fmt.Sprintf("case_conc_%s", uuid.New().String()[:8])

	rc := &domain.ReviewCase{
		ID:               caseID,
		TenantID:         tenantID,
		ExecutionID:      "exec-conc-1",
		AgentID:          agentID,
		AgentName:        "ConcAgent",
		AgentVersionID:   uuid.New(),
		ProposedDecision: "APPROVE",
		Status:           domain.CaseStatusOpen,
	}
	err := st.CreateCase(ctx, rc, nil)
	require.NoError(t, err)

	// Concurrently invoke ResolveCase from 10 goroutines
	concurrency := 10
	var successCount int32
	var conflictCount int32

	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		reviewerID := fmt.Sprintf("reviewer_%d", i)
		go func() {
			defer wg.Done()
			req := domain.ResolveCaseRequest{
				TenantID:   tenantID,
				CaseID:     caseID,
				Decision:   domain.DecisionApprove,
				Reason:     "Concurrent adjudication attempt",
				ReviewerID: reviewerID,
			}
			_, err := st.ResolveCase(ctx, req, fmt.Sprintf("aud_%s", reviewerID), "APPROVE", nil)
			if err == nil {
				atomic.AddInt32(&successCount, 1)
			} else {
				atomic.AddInt32(&conflictCount, 1)
			}
		}()
	}

	wg.Wait()

	// Exactly 1 resolution must succeed; all 9 others must fail with conflict
	assert.Equal(t, int32(1), successCount, "Exactly one concurrent resolution must succeed")
	assert.Equal(t, int32(concurrency-1), conflictCount, "All other concurrent resolution attempts must fail")
}
