package gateway_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	reviewdomain "github.com/aswinsreeraj/arda/services/review/domain"
	reviewevents "github.com/aswinsreeraj/arda/services/review/events"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// TestE2E_HumanReview_CompleteLifecycle exercises the entire 10-step lifecycle for both realtime and durable modes.
func TestE2E_HumanReview_CompleteLifecycle(t *testing.T) {
	gw, st, revSvc, cleanup := setupTestGatewayWithReview(t)
	defer cleanup()

	ts := httptest.NewServer(gw)
	defer ts.Close()

	tenantID := uuid.New()
	reviewerToken, err := auth.GenerateTestToken(auth.Identity{
		UserID:   "reviewer_dave",
		TenantID: tenantID,
		Role:     auth.RoleReviewer,
	}, getTestConfig(), time.Hour)
	require.NoError(t, err)

	agentID := uuid.New()
	versionID := uuid.New()

	// ---------------------------------------------------------
	// 1. Realtime Execution Escalation -> Review Case Created
	// ---------------------------------------------------------
	createReq1 := reviewdomain.CreateCaseRequest{
		TenantID:             tenantID,
		ExecutionID:          "exec_rt_01",
		AgentID:              agentID,
		AgentName:            "FinancialRiskAssessor",
		AgentVersionID:       versionID,
		AgentVersionNumber:   1,
		ExecutionMode:        "realtime",
		ProposedDecision:     "APPROVE",
		Confidence:           0.58,
		ReliabilityThreshold: 0.80,
		EscalationReasons:    []string{"Confidence 0.58 below policy threshold 0.80", "High loan amount"},
		FrozenEvidence: reviewdomain.FrozenEvidence{
			OriginalInput:        map[string]any{"customer_id": "cust_101", "loan_amount": 75000},
			AgentReasoning:       "Customer has good credit history but debt ratio is borderline.",
			ProposedDecision:     "APPROVE",
			Confidence:           0.58,
			ReliabilityThreshold: 0.80,
			EscalationReasons:    []string{"Confidence 0.58 below policy threshold 0.80", "High loan amount"},
			Neighbors: []reviewdomain.ReferenceNeighbor{
				{
					ReferenceExampleID: "case_hist_99",
					Label:              "APPROVE",
					Similarity:         0.89,
				},
			},
		},
	}

	case1, err := revSvc.CreateCase(t.Context(), createReq1)
	require.NoError(t, err)
	require.NotNil(t, case1)
	assert.Equal(t, reviewdomain.CaseStatusOpen, case1.Status)

	// ---------------------------------------------------------
	// 2. Retrieve Queue via GET /api/v1/reviews
	// ---------------------------------------------------------
	resp, body := doRequest(ts, "GET", "/api/v1/reviews?status=OPEN", reviewerToken, nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var listData struct {
		Cases []struct {
			CaseID           string  `json:"case_id"`
			ExecutionID      string  `json:"execution_id"`
			ProposedDecision string  `json:"proposed_decision"`
			Status           string  `json:"status"`
			Confidence       float64 `json:"confidence"`
		} `json:"cases"`
		TotalCount int32 `json:"total_count"`
	}
	require.NoError(t, json.Unmarshal(body, &listData))
	assert.GreaterOrEqual(t, listData.TotalCount, int32(1))
	assert.Equal(t, case1.ID, listData.Cases[0].CaseID)
	assert.Equal(t, "APPROVE", listData.Cases[0].ProposedDecision)

	// ---------------------------------------------------------
	// 3. Retrieve Case & Frozen Evidence via GET /api/v1/reviews/{id}
	// ---------------------------------------------------------
	resp, body = doRequest(ts, "GET", "/api/v1/reviews/"+case1.ID, reviewerToken, nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var detailData map[string]any
	require.NoError(t, json.Unmarshal(body, &detailData))
	assert.Equal(t, case1.ID, detailData["case_id"])
	assert.Equal(t, "OPEN", detailData["status"])
	fe, ok := detailData["frozen_evidence"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Customer has good credit history but debt ratio is borderline.", fe["agent_reasoning"])
	neighbors, ok := fe["neighbors"].([]any)
	require.True(t, ok)
	require.Len(t, neighbors, 1)

	// ---------------------------------------------------------
	// 4. Resolve using 'approve'
	// ---------------------------------------------------------
	resolveApproveBody := map[string]string{
		"decision": "approve",
		"reason":   "Customer credit profile verified with secondary bureau",
	}
	resp, body = doRequest(ts, "POST", "/api/v1/reviews/"+case1.ID+"/resolve", reviewerToken, nil, resolveApproveBody)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var resolveResp map[string]any
	require.NoError(t, json.Unmarshal(body, &resolveResp))
	assert.Equal(t, case1.ID, resolveResp["case_id"])
	assert.Equal(t, "resolved", resolveResp["status"])
	assert.NotEmpty(t, resolveResp["audit_record_id"])

	// Verify case is now RESOLVED and final_decision matches agent proposal
	case1DB, err := st.GetCase(t.Context(), tenantID, case1.ID)
	require.NoError(t, err)
	assert.Equal(t, reviewdomain.CaseStatusResolved, case1DB.Status)
	assert.Equal(t, "APPROVE", case1DB.FinalDecision)

	// Verify double resolution is prevented (409 Conflict)
	resp, _ = doRequest(ts, "POST", "/api/v1/reviews/"+case1.ID+"/resolve", reviewerToken, nil, resolveApproveBody)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)

	// ---------------------------------------------------------
	// 5. Repeat using 'reject'
	// ---------------------------------------------------------
	createReq2 := createReq1
	createReq2.ExecutionID = "exec_rt_02"
	case2, err := revSvc.CreateCase(t.Context(), createReq2)
	require.NoError(t, err)

	resolveRejectBody := map[string]string{
		"decision": "reject",
		"reason":   "Debt-to-income exceeds regulatory threshold",
	}
	resp, _ = doRequest(ts, "POST", "/api/v1/reviews/"+case2.ID+"/resolve", reviewerToken, nil, resolveRejectBody)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	case2DB, err := st.GetCase(t.Context(), tenantID, case2.ID)
	require.NoError(t, err)
	assert.Equal(t, reviewdomain.CaseStatusResolved, case2DB.Status)
	assert.Equal(t, "REJECT", case2DB.FinalDecision)
	assert.Equal(t, reviewdomain.DecisionReject, case2DB.Decision)

	// ---------------------------------------------------------
	// 6. Repeat using 'override'
	// ---------------------------------------------------------
	createReq3 := createReq1
	createReq3.ExecutionID = "exec_rt_03"
	case3, err := revSvc.CreateCase(t.Context(), createReq3)
	require.NoError(t, err)

	resolveOverrideBody := map[string]string{
		"decision":       "override",
		"final_decision": "HOLD",
		"reason":         "Merchant watchlist match; escalated to AML team",
	}
	resp, _ = doRequest(ts, "POST", "/api/v1/reviews/"+case3.ID+"/resolve", reviewerToken, nil, resolveOverrideBody)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	case3DB, err := st.GetCase(t.Context(), tenantID, case3.ID)
	require.NoError(t, err)
	assert.Equal(t, reviewdomain.CaseStatusResolved, case3DB.Status)
	assert.Equal(t, "HOLD", case3DB.FinalDecision)
	assert.Equal(t, reviewdomain.DecisionOverride, case3DB.Decision)

	// ---------------------------------------------------------
	// 7. Verify Outbox Events Enqueued for Reliability Service
	// ---------------------------------------------------------
	pendingEvents, err := st.FetchPendingOutboxEvents(t.Context(), 10)
	require.NoError(t, err)
	require.NotEmpty(t, pendingEvents)
	var foundResolvedEvent bool
	for _, ev := range pendingEvents {
		if ev.EventType == reviewevents.EventTypeReviewResolved {
			foundResolvedEvent = true
			assert.Equal(t, reviewevents.TopicReviewResolved, ev.Topic)
			assert.NotEmpty(t, ev.Headers["signature"])
		}
	}
	assert.True(t, foundResolvedEvent, "expected review.resolved outbox event to be queued")

	// ---------------------------------------------------------
	// 8. Durable Execution Mode (Temporal Workflow Signal Resumption)
	// ---------------------------------------------------------
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	mockExec := &agentruntime.MockExecutor{
		Result: agentruntime.ExecutionResult{
			Status:  agentruntime.StatusEscalated,
			Outcome: agentruntime.OutcomeTypeEscalation,
			Escalation: &agentruntime.EscalationInfo{
				Reason: "Durable workflow requires supervisory review",
				Target: "compliance_officer",
			},
		},
	}
	rt, _ := agentruntime.New(mockExec)
	activities := temporal.NewAgentActivities(rt)

	env.RegisterWorkflow(temporal.AgentExecutionWorkflow)
	env.RegisterActivity(activities.AgentExecutionActivity)

	// Signal durable workflow upon pause
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(temporal.HumanReviewSignalName, temporal.HumanResolutionSignalPayload{
			CaseID:           "case_durable_99",
			Decision:         "override",
			FinalDecision:    "HOLD",
			ResolutionReason: "Durable human review resolution",
			ReviewerID:       "compliance_officer",
			AuditRecordID:    "aud_durable_99",
		})
	}, 10*time.Millisecond)

	env.ExecuteWorkflow(temporal.AgentExecutionWorkflow, temporal.AgentExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ModeType:       "durable",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var workflowResult temporal.AgentExecutionResult
	require.NoError(t, env.GetWorkflowResult(&workflowResult))
	assert.Equal(t, "RESOLVED", workflowResult.Status)
	assert.Equal(t, "HOLD", workflowResult.Output["final_decision"])
	assert.Equal(t, "compliance_officer", workflowResult.Output["reviewer_id"])
	assert.Equal(t, "case_durable_99", workflowResult.Output["review_case_id"])
}
