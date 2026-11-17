package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	gatewayhttp "github.com/aswinsreeraj/arda/services/gateway/http"
	"github.com/aswinsreeraj/arda/services/gateway/ratelimit"
	reviewdomain "github.com/aswinsreeraj/arda/services/review/domain"
	reviewgrpc "github.com/aswinsreeraj/arda/services/review/grpc"
	reviewservice "github.com/aswinsreeraj/arda/services/review/service"
	reviewstore "github.com/aswinsreeraj/arda/services/review/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func setupTestGatewayWithReview(t *testing.T) (http.Handler, *reviewstore.MemoryStore, *reviewservice.Service, func()) {
	jwtVal, err := auth.NewTokenValidator(getTestConfig())
	require.NoError(t, err)

	st := reviewstore.NewMemoryStore()
	revSvc := reviewservice.NewService(st, reviewservice.WithProducerSecret("test-secret"))
	grpcSrv := reviewgrpc.NewServer(revSvc)

	lis := bufconn.Listen(1024 * 1024)
	go func() {
		_ = grpcSrv.Serve(lis)
	}()

	conn, err := grpc.NewClient("passthrough://bufnet-review-test",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	reviewClient := ardav1.NewReviewServiceClient(conn)

	rateCfg := ratelimit.DefaultConfig()
	rateCfg.Enabled = false
	limiter := ratelimit.NewLimiter(rateCfg)

	handler := gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
		JWTValidator: jwtVal,
		RateLimiter:  limiter,
		ReviewClient: reviewClient,
	})

	cleanup := func() {
		_ = conn.Close()
		grpcSrv.GracefulStop()
	}

	return handler, st, revSvc, cleanup
}

func TestGateway_ReviewQueueListing(t *testing.T) {
	gw, _, revSvc, cleanup := setupTestGatewayWithReview(t)
	defer cleanup()

	ts := httptest.NewServer(gw)
	defer ts.Close()

	ctx := context.Background()
	tenantA := uuid.New()
	tenantB := uuid.New()
	agent1 := uuid.New()

	// Seed Tenant A case
	caseA, err := revSvc.CreateCase(ctx, domainCreateRequest(tenantA, "exec-A1", agent1, "Agent1", "APPROVE", 0.75, []string{"fraud_risk"}))
	require.NoError(t, err)

	// Seed Tenant B case
	_, err = revSvc.CreateCase(ctx, domainCreateRequest(tenantB, "exec-B1", agent1, "Agent1", "REJECT", 0.40, []string{"unsupported_region"}))
	require.NoError(t, err)

	tokenA, err := auth.GenerateTestToken(auth.Identity{
		UserID:   "reviewer_alice",
		TenantID: tenantA,
		Role:     auth.RoleReviewer,
	}, getTestConfig(), time.Hour)
	require.NoError(t, err)

	// 1. List cases for Tenant A
	resp, body := doRequest(ts, "GET", "/api/v1/reviews", tokenA, nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var listData struct {
		Cases []struct {
			CaseID            string   `json:"case_id"`
			ExecutionID       string   `json:"execution_id"`
			ProposedDecision  string   `json:"proposed_decision"`
			Status            string   `json:"status"`
			Confidence        float64  `json:"confidence"`
			EscalationReasons []string `json:"escalation_reasons"`
		} `json:"cases"`
		TotalCount int32 `json:"total_count"`
	}
	err = json.Unmarshal(body, &listData)
	require.NoError(t, err)
	assert.Equal(t, int32(1), listData.TotalCount)
	require.Len(t, listData.Cases, 1)
	assert.Equal(t, caseA.ID, listData.Cases[0].CaseID)
	assert.Equal(t, "exec-A1", listData.Cases[0].ExecutionID)
	assert.Equal(t, "APPROVE", listData.Cases[0].ProposedDecision)
	assert.Equal(t, 0.75, listData.Cases[0].Confidence)
}

func TestGateway_ReviewCaseDetailsAndFrozenEvidence(t *testing.T) {
	gw, _, revSvc, cleanup := setupTestGatewayWithReview(t)
	defer cleanup()

	ts := httptest.NewServer(gw)
	defer ts.Close()

	ctx := context.Background()
	tenantA := uuid.New()
	tenantB := uuid.New()

	caseA, err := revSvc.CreateCase(ctx, domainCreateRequest(tenantA, "exec-det-1", uuid.New(), "AgentDet", "APPROVE", 0.65, []string{"low_score"}))
	require.NoError(t, err)

	tokenA, err := auth.GenerateTestToken(auth.Identity{
		UserID:   "reviewer_a",
		TenantID: tenantA,
		Role:     auth.RoleReviewer,
	}, getTestConfig(), time.Hour)
	require.NoError(t, err)

	tokenB, err := auth.GenerateTestToken(auth.Identity{
		UserID:   "reviewer_b",
		TenantID: tenantB,
		Role:     auth.RoleReviewer,
	}, getTestConfig(), time.Hour)
	require.NoError(t, err)

	// 1. Tenant A reviewer gets case details -> 200 OK with frozen evidence
	resp, body := doRequest(ts, "GET", fmt.Sprintf("/api/v1/reviews/%s", caseA.ID), tokenA, nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var caseDetail map[string]any
	err = json.Unmarshal(body, &caseDetail)
	require.NoError(t, err)
	assert.Equal(t, caseA.ID, caseDetail["case_id"])
	assert.Equal(t, "OPEN", caseDetail["status"])
	assert.Equal(t, "APPROVE", caseDetail["proposed_decision"])

	frozen, ok := caseDetail["frozen_evidence"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Customer requested expedited refund.", frozen["agent_reasoning"])

	// 2. Tenant B reviewer attempts access to Tenant A's case -> 404 Not Found
	respB, _ := doRequest(ts, "GET", fmt.Sprintf("/api/v1/reviews/%s", caseA.ID), tokenB, nil, nil)
	assert.Equal(t, http.StatusNotFound, respB.StatusCode)

	// 3. Non-existent case ID -> 404 Not Found
	respNone, _ := doRequest(ts, "GET", "/api/v1/reviews/case_nonexistent_999", tokenA, nil, nil)
	assert.Equal(t, http.StatusNotFound, respNone.StatusCode)
}

func TestGateway_ReviewCaseResolutionFlows(t *testing.T) {
	gw, _, revSvc, cleanup := setupTestGatewayWithReview(t)
	defer cleanup()

	ts := httptest.NewServer(gw)
	defer ts.Close()

	ctx := context.Background()
	tenantID := uuid.New()

	tokenReviewer, err := auth.GenerateTestToken(auth.Identity{
		UserID:   "reviewer_bob",
		TenantID: tenantID,
		Role:     auth.RoleReviewer,
	}, getTestConfig(), time.Hour)
	require.NoError(t, err)

	tokenAdmin, err := auth.GenerateTestToken(auth.Identity{
		UserID:   "admin_carol",
		TenantID: tenantID,
		Role:     auth.RoleCompanyAdmin,
	}, getTestConfig(), time.Hour)
	require.NoError(t, err)

	// Flow 1: Approve
	case1, err := revSvc.CreateCase(ctx, domainCreateRequest(tenantID, "exec-flow-1", uuid.New(), "Agent1", "APPROVE", 0.60, []string{"threshold_unmet"}))
	require.NoError(t, err)

	resolveBody := map[string]any{
		"decision": "approve",
		"reason":   "Auditor approves transaction under policy rule 4.2.",
	}
	resp, body := doRequest(ts, "POST", fmt.Sprintf("/api/v1/reviews/%s/resolve", case1.ID), tokenReviewer, nil, resolveBody)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var resData map[string]any
	err = json.Unmarshal(body, &resData)
	require.NoError(t, err)
	assert.Equal(t, case1.ID, resData["case_id"])
	assert.Equal(t, "resolved", resData["status"])
	assert.NotEmpty(t, resData["audit_record_id"])

	// Attempting double resolution returns 409 Conflict
	respDouble, bodyDouble := doRequest(ts, "POST", fmt.Sprintf("/api/v1/reviews/%s/resolve", case1.ID), tokenReviewer, nil, resolveBody)
	assert.Equal(t, http.StatusConflict, respDouble.StatusCode)
	errResp := parseErrorResponse(t, bodyDouble)
	assert.Equal(t, "CASE_ALREADY_RESOLVED", errResp.Error.Code)

	// Flow 2: Override by Company Admin
	case2, err := revSvc.CreateCase(ctx, domainCreateRequest(tenantID, "exec-flow-2", uuid.New(), "Agent1", "APPROVE", 0.60, []string{"threshold_unmet"}))
	require.NoError(t, err)

	overrideBody := map[string]any{
		"decision":       "override",
		"final_decision": "HOLD",
		"reason":         "Merchant on watch list; customer unreachable.",
	}
	respOverride, bodyOverride := doRequest(ts, "POST", fmt.Sprintf("/api/v1/reviews/%s/resolve", case2.ID), tokenAdmin, nil, overrideBody)
	assert.Equal(t, http.StatusOK, respOverride.StatusCode)

	var overrideData map[string]any
	err = json.Unmarshal(bodyOverride, &overrideData)
	require.NoError(t, err)
	assert.Equal(t, case2.ID, overrideData["case_id"])
	assert.Equal(t, "resolved", overrideData["status"])

	// Validation: Override missing final_decision returns 400 Bad Request
	case3, err := revSvc.CreateCase(ctx, domainCreateRequest(tenantID, "exec-flow-3", uuid.New(), "Agent1", "APPROVE", 0.60, []string{"threshold_unmet"}))
	require.NoError(t, err)

	badOverrideBody := map[string]any{
		"decision": "override",
		"reason":   "Missing final decision value",
	}
	respBad, _ := doRequest(ts, "POST", fmt.Sprintf("/api/v1/reviews/%s/resolve", case3.ID), tokenReviewer, nil, badOverrideBody)
	assert.Equal(t, http.StatusBadRequest, respBad.StatusCode)

	// Validation: Missing reason returns 400 Bad Request
	missingReasonBody := map[string]any{
		"decision": "approve",
		"reason":   "",
	}
	respNoReason, _ := doRequest(ts, "POST", fmt.Sprintf("/api/v1/reviews/%s/resolve", case3.ID), tokenReviewer, nil, missingReasonBody)
	assert.Equal(t, http.StatusBadRequest, respNoReason.StatusCode)
}

func TestGateway_ReviewRBACRestrictions(t *testing.T) {
	gw, _, revSvc, cleanup := setupTestGatewayWithReview(t)
	defer cleanup()

	ts := httptest.NewServer(gw)
	defer ts.Close()

	ctx := context.Background()
	tenantID := uuid.New()

	c, err := revSvc.CreateCase(ctx, domainCreateRequest(tenantID, "exec-rbac-1", uuid.New(), "Agent1", "APPROVE", 0.70, []string{"review"}))
	require.NoError(t, err)

	tokenDev, err := auth.GenerateTestToken(auth.Identity{
		UserID:   "dev_dan",
		TenantID: tenantID,
		Role:     auth.RoleAgentDeveloper,
	}, getTestConfig(), time.Hour)
	require.NoError(t, err)

	tokenSuperAdmin, err := auth.GenerateTestToken(auth.Identity{
		UserID:   "super_admin",
		TenantID: tenantID,
		Role:     auth.RoleSuperAdmin,
	}, getTestConfig(), time.Hour)
	require.NoError(t, err)

	resolveBody := map[string]any{
		"decision": "approve",
		"reason":   "Attempt unauthorized resolve",
	}

	// 1. Agent Developer cannot resolve cases -> 403 Forbidden
	respDev, _ := doRequest(ts, "POST", fmt.Sprintf("/api/v1/reviews/%s/resolve", c.ID), tokenDev, nil, resolveBody)
	assert.Equal(t, http.StatusForbidden, respDev.StatusCode)

	// 2. Super Admin cannot resolve review cases -> 403 Forbidden
	respSuper, _ := doRequest(ts, "POST", fmt.Sprintf("/api/v1/reviews/%s/resolve", c.ID), tokenSuperAdmin, nil, resolveBody)
	assert.Equal(t, http.StatusForbidden, respSuper.StatusCode)

	// 3. Unauthenticated request -> 401 Unauthorized
	respUnauth, _ := doRequest(ts, "POST", fmt.Sprintf("/api/v1/reviews/%s/resolve", c.ID), "", nil, resolveBody)
	assert.Equal(t, http.StatusUnauthorized, respUnauth.StatusCode)
}

func domainCreateRequest(tenantID uuid.UUID, execID string, agentID uuid.UUID, name string, proposed string, conf float64, reasons []string) reviewdomain.CreateCaseRequest {
	return reviewdomain.CreateCaseRequest{
		TenantID:             tenantID,
		ExecutionID:          execID,
		AgentID:              agentID,
		AgentName:            name,
		AgentVersionID:       uuid.New(),
		AgentVersionNumber:   1,
		ExecutionMode:        "realtime",
		ProposedDecision:     proposed,
		Confidence:           conf,
		ReliabilityThreshold: 0.85,
		EscalationReasons:    reasons,
		FrozenEvidence: reviewdomain.FrozenEvidence{
			AgentReasoning:   "Customer requested expedited refund.",
			ProposedDecision: proposed,
			Confidence:       conf,
		},
	}
}
