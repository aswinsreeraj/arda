package grpc

import (
	"context"
	"net"
	"testing"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	registrygrpc "github.com/aswinsreeraj/arda/services/registry/grpc"
	"github.com/aswinsreeraj/arda/services/review/service"
	"github.com/aswinsreeraj/arda/services/review/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func setupTestGRPCServer(t *testing.T) (ardav1.ReviewServiceClient, func()) {
	st := store.NewMemoryStore()
	svc := service.NewService(st)
	grpcSrv := NewServer(svc)

	lis := bufconn.Listen(1024 * 1024)
	go func() {
		_ = grpcSrv.Serve(lis)
	}()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	client := ardav1.NewReviewServiceClient(conn)
	cleanup := func() {
		_ = conn.Close()
		grpcSrv.GracefulStop()
	}
	return client, cleanup
}

func contextWithAuth(ctx context.Context, tenantID uuid.UUID, userID string, role string) context.Context {
	md := metadata.New(map[string]string{
		registrygrpc.MetadataTenantID: tenantID.String(),
		registrygrpc.MetadataUserID:   userID,
		registrygrpc.MetadataRole:     role,
	})
	return metadata.NewOutgoingContext(ctx, md)
}

func TestReviewGRPCServer_E2E(t *testing.T) {
	client, cleanup := setupTestGRPCServer(t)
	defer cleanup()

	ctx := context.Background()
	tenantID := uuid.New()
	authCtx := contextWithAuth(ctx, tenantID, "user_1", "reviewer")

	agentID := uuid.New()
	agentVersionID := uuid.New()

	// 1. CreateReviewCase
	createResp, err := client.CreateReviewCase(authCtx, &ardav1.CreateReviewCaseRequest{
		TenantId:             tenantID.String(),
		ExecutionId:          "exec-grpc-1",
		AgentId:              agentID.String(),
		AgentName:            "ReviewerBot",
		AgentVersionId:       agentVersionID.String(),
		AgentVersionNumber:   1,
		ExecutionMode:        "realtime",
		ProposedDecision:     "APPROVE",
		Confidence:           0.77,
		ReliabilityThreshold: 0.85,
		EscalationReasons:    []string{"requires_review"},
	})
	require.NoError(t, err)
	require.NotNil(t, createResp.GetCase())
	caseID := createResp.GetCase().GetId()
	assert.NotEmpty(t, caseID)
	assert.Equal(t, "OPEN", createResp.GetCase().GetStatus())

	// 2. GetReviewCase
	getResp, err := client.GetReviewCase(authCtx, &ardav1.GetReviewCaseRequest{
		TenantId: tenantID.String(),
		CaseId:   caseID,
	})
	require.NoError(t, err)
	assert.Equal(t, caseID, getResp.GetCase().GetId())
	assert.Equal(t, "APPROVE", getResp.GetCase().GetProposedDecision())

	// 3. ListReviewCases
	listResp, err := client.ListReviewCases(authCtx, &ardav1.ListReviewCasesRequest{
		TenantId: tenantID.String(),
		Status:   "OPEN",
	})
	require.NoError(t, err)
	assert.Len(t, listResp.GetCases(), 1)
	assert.Equal(t, caseID, listResp.GetCases()[0].GetCaseId())

	// 4. ResolveReviewCase
	resolveResp, err := client.ResolveReviewCase(authCtx, &ardav1.ResolveReviewCaseRequest{
		TenantId:      tenantID.String(),
		CaseId:        caseID,
		Decision:      "override",
		FinalDecision: "HOLD",
		Reason:        "Adjudication confirmed HOLD.",
		ReviewerId:    "user_1",
	})
	require.NoError(t, err)
	assert.Equal(t, caseID, resolveResp.GetCaseId())
	assert.Equal(t, "resolved", resolveResp.GetStatus())
	assert.Equal(t, "HOLD", resolveResp.GetFinalDecision())
	assert.NotEmpty(t, resolveResp.GetAuditRecordId())

	// 5. Verify Case is now RESOLVED via GetReviewCase
	getAfterResp, err := client.GetReviewCase(authCtx, &ardav1.GetReviewCaseRequest{
		TenantId: tenantID.String(),
		CaseId:   caseID,
	})
	require.NoError(t, err)
	assert.Equal(t, "RESOLVED", getAfterResp.GetCase().GetStatus())
	assert.Equal(t, "HOLD", getAfterResp.GetCase().GetFinalDecision())
}

func TestReviewGRPCServer_TenantSecurity(t *testing.T) {
	client, cleanup := setupTestGRPCServer(t)
	defer cleanup()

	ctx := context.Background()
	tenantA := uuid.New()
	tenantB := uuid.New()

	ctxA := contextWithAuth(ctx, tenantA, "user_A", "reviewer")
	ctxB := contextWithAuth(ctx, tenantB, "user_B", "reviewer")

	// Create case under Tenant A
	createResp, err := client.CreateReviewCase(ctxA, &ardav1.CreateReviewCaseRequest{
		TenantId:         tenantA.String(),
		ExecutionId:      "exec-sec-1",
		AgentId:          uuid.New().String(),
		AgentName:        "AgentA",
		AgentVersionId:   uuid.New().String(),
		ProposedDecision: "APPROVE",
	})
	require.NoError(t, err)
	caseID := createResp.GetCase().GetId()

	// 1. Tenant B attempts to get Tenant A's case -> NotFound
	_, err = client.GetReviewCase(ctxB, &ardav1.GetReviewCaseRequest{
		TenantId: tenantB.String(),
		CaseId:   caseID,
	})
	assert.Error(t, err)
	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())

	// 2. Caller spoofing: tenant ID in body (Tenant A) does not match trusted context (Tenant B) -> PermissionDenied
	_, err = client.GetReviewCase(ctxB, &ardav1.GetReviewCaseRequest{
		TenantId: tenantA.String(),
		CaseId:   caseID,
	})
	assert.Error(t, err)
	st, ok = status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.PermissionDenied, st.Code())

	// 3. Request without internal auth metadata -> Unauthenticated
	_, err = client.GetReviewCase(ctx, &ardav1.GetReviewCaseRequest{
		TenantId: tenantA.String(),
		CaseId:   caseID,
	})
	assert.Error(t, err)
	st, ok = status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}
