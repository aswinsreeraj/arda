package temporal_test

import (
	"context"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/grpc"
)

// MockReviewServiceClient implements ardav1.ReviewServiceClient for testing
type MockReviewServiceClient struct {
	mock.Mock
}

func (m *MockReviewServiceClient) CreateReviewCase(ctx context.Context, in *ardav1.CreateReviewCaseRequest, opts ...grpc.CallOption) (*ardav1.CreateReviewCaseResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ardav1.CreateReviewCaseResponse), args.Error(1)
}

func (m *MockReviewServiceClient) GetReviewCase(ctx context.Context, in *ardav1.GetReviewCaseRequest, opts ...grpc.CallOption) (*ardav1.GetReviewCaseResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*ardav1.GetReviewCaseResponse), args.Error(1)
}

func (m *MockReviewServiceClient) ListReviewCases(ctx context.Context, in *ardav1.ListReviewCasesRequest, opts ...grpc.CallOption) (*ardav1.ListReviewCasesResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*ardav1.ListReviewCasesResponse), args.Error(1)
}

func (m *MockReviewServiceClient) ResolveReviewCase(ctx context.Context, in *ardav1.ResolveReviewCaseRequest, opts ...grpc.CallOption) (*ardav1.ResolveReviewCaseResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*ardav1.ResolveReviewCaseResponse), args.Error(1)
}

// TestWorkflow_HumanReviewResolution_Approve verifies the full pause and resume flow when approved
func TestWorkflow_HumanReviewResolution_Approve(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	mockExec := &agentruntime.MockExecutor{
		Result: agentruntime.ExecutionResult{
			Status:  agentruntime.StatusEscalated,
			Outcome: agentruntime.OutcomeTypeEscalation,
			Escalation: &agentruntime.EscalationInfo{
				Reason: "Confidence 0.65 below policy threshold 0.85",
				Target: "reviewer",
			},
		},
	}
	rt, _ := agentruntime.New(mockExec)
	activities := temporal.NewAgentActivities(rt)

	env.RegisterWorkflow(temporal.AgentExecutionWorkflow)
	env.RegisterActivity(activities.AgentExecutionActivity)

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	input := temporal.AgentExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionInput: map[string]any{"customer_id": "cust_456"},
		ModeType:       "durable",
	}

	expectedSignal := temporal.HumanResolutionSignalPayload{
		CaseID:           "case_appr_01",
		Decision:         "approve",
		FinalDecision:    "APPROVE",
		ResolutionReason: "Confidence is acceptable given customer tenure",
		ReviewerID:       "rev_alice",
		AuditRecordID:    "aud_appr_01",
	}

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(temporal.HumanReviewSignalName, expectedSignal)
	}, 10*time.Millisecond)

	env.ExecuteWorkflow(temporal.AgentExecutionWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result temporal.AgentExecutionResult
	require.NoError(t, env.GetWorkflowResult(&result))

	assert.Equal(t, "RESOLVED", result.Status)
	assert.Equal(t, "Confidence is acceptable given customer tenure", result.Reason)
	assert.Equal(t, "APPROVE", result.Output["final_decision"])
	assert.Equal(t, "approve", result.Output["resolution_decision"])
	assert.Equal(t, "rev_alice", result.Output["reviewer_id"])
	assert.Equal(t, "case_appr_01", result.Output["review_case_id"])
	assert.Equal(t, "aud_appr_01", result.Output["audit_record_id"])
}

// TestWorkflow_HumanReviewResolution_Override verifies that reviewer override takes precedence
func TestWorkflow_HumanReviewResolution_Override(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	mockExec := &agentruntime.MockExecutor{
		Result: agentruntime.ExecutionResult{
			Status:  agentruntime.StatusEscalated,
			Outcome: agentruntime.OutcomeTypeEscalation,
			Escalation: &agentruntime.EscalationInfo{
				Reason: "High-value wire transfer",
				Target: "compliance_officer",
			},
		},
	}
	rt, _ := agentruntime.New(mockExec)
	activities := temporal.NewAgentActivities(rt)

	env.RegisterWorkflow(temporal.AgentExecutionWorkflow)
	env.RegisterActivity(activities.AgentExecutionActivity)

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	input := temporal.AgentExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionInput: map[string]any{"amount": 500000},
	}

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(temporal.HumanReviewSignalName, temporal.HumanResolutionSignalPayload{
			CaseID:           "case_ovr_02",
			Decision:         "override",
			FinalDecision:    "HOLD",
			ResolutionReason: "Merchant on watchlist; hold for fraud team review",
			ReviewerID:       "rev_bob",
			AuditRecordID:    "aud_ovr_02",
		})
	}, 10*time.Millisecond)

	env.ExecuteWorkflow(temporal.AgentExecutionWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result temporal.AgentExecutionResult
	require.NoError(t, env.GetWorkflowResult(&result))

	assert.Equal(t, "RESOLVED", result.Status)
	assert.Equal(t, "HOLD", result.Output["final_decision"])
	assert.Equal(t, "override", result.Output["resolution_decision"])
	assert.Equal(t, "Merchant on watchlist; hold for fraud team review", result.Output["resolution_reason"])
	assert.Equal(t, "rev_bob", result.Output["reviewer_id"])
}

// TestWorkflow_HumanReviewResolution_DuplicateSignals verifies duplicate signals are drained cleanly
func TestWorkflow_HumanReviewResolution_DuplicateSignals(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	mockExec := &agentruntime.MockExecutor{
		Result: agentruntime.ExecutionResult{
			Status:  agentruntime.StatusEscalated,
			Outcome: agentruntime.OutcomeTypeEscalation,
			Escalation: &agentruntime.EscalationInfo{
				Reason: "Edge case flagged",
				Target: "reviewer",
			},
		},
	}
	rt, _ := agentruntime.New(mockExec)
	activities := temporal.NewAgentActivities(rt)

	env.RegisterWorkflow(temporal.AgentExecutionWorkflow)
	env.RegisterActivity(activities.AgentExecutionActivity)

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	input := temporal.AgentExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
	}

	payload := temporal.HumanResolutionSignalPayload{
		CaseID:           "case_dup_03",
		Decision:         "reject",
		FinalDecision:    "REJECT",
		ResolutionReason: "Incorrect classification",
		ReviewerID:       "rev_carol",
		AuditRecordID:    "aud_dup_03",
	}

	// Send signal 3 times rapidly
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(temporal.HumanReviewSignalName, payload)
		env.SignalWorkflow(temporal.HumanReviewSignalName, payload)
		env.SignalWorkflow(temporal.HumanReviewSignalName, payload)
	}, 10*time.Millisecond)

	env.ExecuteWorkflow(temporal.AgentExecutionWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result temporal.AgentExecutionResult
	require.NoError(t, env.GetWorkflowResult(&result))

	assert.Equal(t, "RESOLVED", result.Status)
	assert.Equal(t, "REJECT", result.Output["final_decision"])
	assert.Equal(t, "reject", result.Output["resolution_decision"])
}
