package grpc

import (
	"context"
	"net"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	registrygrpc "github.com/aswinsreeraj/arda/services/registry/grpc"
	"github.com/aswinsreeraj/arda/services/review/domain"
	"github.com/aswinsreeraj/arda/services/review/service"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server implements ardav1.ReviewServiceServer.
type Server struct {
	ardav1.UnimplementedReviewServiceServer
	svc        *service.Service
	grpcServer *grpc.Server
}

// NewServer initializes a new Server and registers it with a grpc.Server configured
// with the internal authentication interceptor.
func NewServer(svc *service.Service, opts ...grpc.ServerOption) *Server {
	unaryInterceptors := []grpc.UnaryServerInterceptor{
		registrygrpc.InternalAuthInterceptor(),
	}
	serverOpts := append([]grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unaryInterceptors...),
	}, opts...)

	gServer := grpc.NewServer(serverOpts...)
	s := &Server{
		svc:        svc,
		grpcServer: gServer,
	}
	ardav1.RegisterReviewServiceServer(gServer, s)
	return s
}

// GRPCServer returns the underlying *grpc.Server.
func (s *Server) GRPCServer() *grpc.Server {
	return s.grpcServer
}

// Serve accepts incoming connections on the listener.
func (s *Server) Serve(lis net.Listener) error {
	return s.grpcServer.Serve(lis)
}

// GracefulStop stops the gRPC server gracefully.
func (s *Server) GracefulStop() {
	s.grpcServer.GracefulStop()
}

// Stop stops the gRPC server immediately.
func (s *Server) Stop() {
	s.grpcServer.Stop()
}

func (s *Server) resolveTenant(ctx context.Context, reqTenantID string) (uuid.UUID, error) {
	trustedTenantID, err := registrygrpc.TenantIDFromContext(ctx)
	if err != nil {
		return uuid.Nil, status.Error(codes.Unauthenticated, "missing trusted tenant context in gRPC metadata")
	}
	if reqTenantID != "" && reqTenantID != trustedTenantID.String() {
		return uuid.Nil, status.Error(codes.PermissionDenied, "tenant id in request does not match trusted context")
	}
	return trustedTenantID, nil
}

// CreateReviewCase registers an escalated review case and stores frozen evidence.
func (s *Server) CreateReviewCase(ctx context.Context, req *ardav1.CreateReviewCaseRequest) (*ardav1.CreateReviewCaseResponse, error) {
	tenantID, err := s.resolveTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	agentID, err := uuid.Parse(req.GetAgentId())
	if err != nil || agentID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid agent_id")
	}

	agentVersionID, err := uuid.Parse(req.GetAgentVersionId())
	if err != nil || agentVersionID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid agent_version_id")
	}

	createReq := domain.CreateCaseRequest{
		CaseID:               req.GetCaseId(),
		TenantID:             tenantID,
		ExecutionID:          req.GetExecutionId(),
		AgentID:              agentID,
		AgentName:            req.GetAgentName(),
		AgentVersionID:       agentVersionID,
		AgentVersionNumber:   int(req.GetAgentVersionNumber()),
		ExecutionMode:        req.GetExecutionMode(),
		ProposedDecision:     req.GetProposedDecision(),
		Confidence:           req.GetConfidence(),
		ReliabilityThreshold: req.GetReliabilityThreshold(),
		EscalationReasons:    req.GetEscalationReasons(),
		FrozenEvidence:       ProtoToDomainFrozenEvidence(req.GetFrozenEvidence()),
	}

	rc, err := s.svc.CreateCase(ctx, createReq)
	if err != nil {
		return nil, MapReviewDomainError(err)
	}

	protoCase, err := DomainCaseToProto(*rc)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &ardav1.CreateReviewCaseResponse{Case: protoCase}, nil
}

// GetReviewCase retrieves a review case with complete frozen evidence.
func (s *Server) GetReviewCase(ctx context.Context, req *ardav1.GetReviewCaseRequest) (*ardav1.GetReviewCaseResponse, error) {
	tenantID, err := s.resolveTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	rc, err := s.svc.GetCase(ctx, tenantID, req.GetCaseId())
	if err != nil {
		return nil, MapReviewDomainError(err)
	}

	protoCase, err := DomainCaseToProto(*rc)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &ardav1.GetReviewCaseResponse{Case: protoCase}, nil
}

// ListReviewCases retrieves tenant-scoped review cases with filtering and pagination.
func (s *Server) ListReviewCases(ctx context.Context, req *ardav1.ListReviewCasesRequest) (*ardav1.ListReviewCasesResponse, error) {
	tenantID, err := s.resolveTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	var agentID uuid.UUID
	if req.GetAgentId() != "" {
		if parsed, err := uuid.Parse(req.GetAgentId()); err == nil {
			agentID = parsed
		}
	}

	var pageSize int32
	var pageToken string
	if req.GetPagination() != nil {
		pageSize = req.GetPagination().GetPageSize()
		pageToken = req.GetPagination().GetPageToken()
	}

	filter := domain.ListFilter{
		TenantID:         tenantID,
		Status:           domain.CaseStatus(req.GetStatus()),
		AgentID:          agentID,
		EscalationReason: req.GetEscalationReason(),
		PageSize:         pageSize,
		PageToken:        pageToken,
	}

	summaries, nextPageToken, totalCount, err := s.svc.ListCases(ctx, filter)
	if err != nil {
		return nil, MapReviewDomainError(err)
	}

	protoSummaries := make([]*ardav1.ReviewCaseSummary, 0, len(summaries))
	for _, s := range summaries {
		protoSummaries = append(protoSummaries, DomainSummaryToProto(s))
	}

	return &ardav1.ListReviewCasesResponse{
		Cases: protoSummaries,
		Pagination: &ardav1.PaginationResponse{
			NextPageToken: nextPageToken,
			TotalCount:    totalCount,
		},
	}, nil
}

// ResolveReviewCase adjudicates an open review case and records the final authoritative decision.
func (s *Server) ResolveReviewCase(ctx context.Context, req *ardav1.ResolveReviewCaseRequest) (*ardav1.ResolveReviewCaseResponse, error) {
	tenantID, err := s.resolveTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	resolveReq := domain.ResolveCaseRequest{
		TenantID:      tenantID,
		CaseID:        req.GetCaseId(),
		Decision:      domain.ResolutionDecision(req.GetDecision()),
		FinalDecision: req.GetFinalDecision(),
		Reason:        req.GetReason(),
		ReviewerID:    req.GetReviewerId(),
	}

	res, err := s.svc.ResolveCase(ctx, resolveReq)
	if err != nil {
		return nil, MapReviewDomainError(err)
	}

	return &ardav1.ResolveReviewCaseResponse{
		CaseId:        res.CaseID,
		Status:        res.Status,
		FinalDecision: res.FinalDecision,
		AuditRecordId: res.AuditRecordID,
		ResolvedAt:    timestamppb.New(res.ResolvedAt),
	}, nil
}
