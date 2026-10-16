package grpc

import (
	"context"
	"net"
	"strconv"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/services/registry/service"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements ardav1.AgentRegistryServiceServer backed by RegistryService.
type Server struct {
	ardav1.UnimplementedAgentRegistryServiceServer
	svc        *service.RegistryService
	grpcServer *grpc.Server
}

// NewServer initializes a new Server and registers it with a grpc.Server configured
// with the internal authentication interceptor.
func NewServer(svc *service.RegistryService, opts ...grpc.ServerOption) *Server {
	unaryInterceptors := []grpc.UnaryServerInterceptor{
		InternalAuthInterceptor(),
	}
	serverOpts := append([]grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unaryInterceptors...),
	}, opts...)

	gServer := grpc.NewServer(serverOpts...)
	s := &Server{
		svc:        svc,
		grpcServer: gServer,
	}
	ardav1.RegisterAgentRegistryServiceServer(gServer, s)
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
	trustedTenantID, err := TenantIDFromContext(ctx)
	if err != nil {
		return uuid.Nil, status.Error(codes.Unauthenticated, "missing trusted tenant context")
	}
	if reqTenantID != "" && reqTenantID != trustedTenantID.String() {
		return uuid.Nil, status.Error(codes.PermissionDenied, "tenant id in request does not match trusted context")
	}
	return trustedTenantID, nil
}

// CreateAgent creates a new agent with an initial draft version.
func (s *Server) CreateAgent(ctx context.Context, req *ardav1.CreateAgentRequest) (*ardav1.CreateAgentResponse, error) {
	tenantID, err := s.resolveTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	cfg := ProtoToDomainConfig(req.GetConfiguration())
	createReq := service.CreateAgentRequest{
		Name:          req.GetName(),
		Configuration: &cfg,
	}

	detail, err := s.svc.CreateAgent(ctx, tenantID, createReq)
	if err != nil {
		return nil, MapDomainError(err)
	}

	protoDetail, err := DetailToProto(*detail)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &ardav1.CreateAgentResponse{Agent: protoDetail}, nil
}

// GetAgent retrieves an agent by ID.
func (s *Server) GetAgent(ctx context.Context, req *ardav1.GetAgentRequest) (*ardav1.GetAgentResponse, error) {
	tenantID, err := s.resolveTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	agentID, err := uuid.Parse(req.GetAgentId())
	if err != nil || agentID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid agent id: must be a valid uuid")
	}

	detail, err := s.svc.GetAgent(ctx, tenantID, agentID)
	if err != nil {
		return nil, MapDomainError(err)
	}

	protoDetail, err := DetailToProto(*detail)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &ardav1.GetAgentResponse{Agent: protoDetail}, nil
}

// ListAgents retrieves paginated agents for the tenant.
func (s *Server) ListAgents(ctx context.Context, req *ardav1.ListAgentsRequest) (*ardav1.ListAgentsResponse, error) {
	tenantID, err := s.resolveTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	limit := 20
	offset := 0
	if p := req.GetPagination(); p != nil {
		if p.GetPageSize() > 0 {
			limit = int(p.GetPageSize())
			if limit > 100 {
				limit = 100
			}
		}
		if token := p.GetPageToken(); token != "" {
			if parsedOffset, err := strconv.Atoi(token); err == nil && parsedOffset >= 0 {
				offset = parsedOffset
			}
		}
	}

	details, total, err := s.svc.ListAgents(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, MapDomainError(err)
	}

	var protoAgents []*ardav1.AgentDetail
	for _, d := range details {
		pDetail, err := DetailToProto(d)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		protoAgents = append(protoAgents, pDetail)
	}

	var nextToken string
	if offset+len(details) < total {
		nextToken = strconv.Itoa(offset + len(details))
	}

	return &ardav1.ListAgentsResponse{
		Agents: protoAgents,
		Pagination: &ardav1.PaginationResponse{
			NextPageToken: nextToken,
			TotalCount:    int32(total),
		},
	}, nil
}

// CreateAgentVersion creates a new draft version for an agent.
func (s *Server) CreateAgentVersion(ctx context.Context, req *ardav1.CreateAgentVersionRequest) (*ardav1.CreateAgentVersionResponse, error) {
	tenantID, err := s.resolveTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	agentID, err := uuid.Parse(req.GetAgentId())
	if err != nil || agentID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid agent id: must be a valid uuid")
	}

	cfg := ProtoToDomainConfig(req.GetConfiguration())
	ver, err := s.svc.CreateDraftVersion(ctx, tenantID, agentID, cfg)
	if err != nil {
		return nil, MapDomainError(err)
	}

	protoVer, err := VersionToProto(*ver)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &ardav1.CreateAgentVersionResponse{Version: protoVer}, nil
}

// GetAgentVersion retrieves a specific version of an agent.
func (s *Server) GetAgentVersion(ctx context.Context, req *ardav1.GetAgentVersionRequest) (*ardav1.GetAgentVersionResponse, error) {
	tenantID, err := s.resolveTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	agentID, err := uuid.Parse(req.GetAgentId())
	if err != nil || agentID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid agent id: must be a valid uuid")
	}

	versionID, err := uuid.Parse(req.GetVersionId())
	if err != nil || versionID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid version id: must be a valid uuid")
	}

	ver, err := s.svc.GetVersion(ctx, tenantID, agentID, versionID)
	if err != nil {
		return nil, MapDomainError(err)
	}

	protoVer, err := VersionToProto(*ver)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &ardav1.GetAgentVersionResponse{Version: protoVer}, nil
}

// GetLatestPublishedVersion resolves the latest published version of an agent.
func (s *Server) GetLatestPublishedVersion(ctx context.Context, req *ardav1.GetLatestPublishedVersionRequest) (*ardav1.GetLatestPublishedVersionResponse, error) {
	tenantID, err := s.resolveTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	agentID, err := uuid.Parse(req.GetAgentId())
	if err != nil || agentID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid agent id: must be a valid uuid")
	}

	ver, err := s.svc.GetLatestPublishedVersion(ctx, tenantID, agentID)
	if err != nil {
		return nil, MapDomainError(err)
	}

	protoVer, err := VersionToProto(*ver)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &ardav1.GetLatestPublishedVersionResponse{Version: protoVer}, nil
}

// UpdateDraftVersion updates an existing draft version's configuration.
func (s *Server) UpdateDraftVersion(ctx context.Context, req *ardav1.UpdateDraftVersionRequest) (*ardav1.UpdateDraftVersionResponse, error) {
	tenantID, err := s.resolveTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	agentID, err := uuid.Parse(req.GetAgentId())
	if err != nil || agentID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid agent id: must be a valid uuid")
	}

	versionID, err := uuid.Parse(req.GetVersionId())
	if err != nil || versionID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid version id: must be a valid uuid")
	}

	cfg := ProtoToDomainConfig(req.GetConfiguration())
	ver, err := s.svc.UpdateDraftVersion(ctx, tenantID, agentID, versionID, cfg)
	if err != nil {
		return nil, MapDomainError(err)
	}

	protoVer, err := VersionToProto(*ver)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &ardav1.UpdateDraftVersionResponse{Version: protoVer}, nil
}

// PublishVersion publishes a draft version, making it immutable.
func (s *Server) PublishVersion(ctx context.Context, req *ardav1.PublishVersionRequest) (*ardav1.PublishVersionResponse, error) {
	tenantID, err := s.resolveTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	agentID, err := uuid.Parse(req.GetAgentId())
	if err != nil || agentID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid agent id: must be a valid uuid")
	}

	versionID, err := uuid.Parse(req.GetVersionId())
	if err != nil || versionID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid version id: must be a valid uuid")
	}

	ver, err := s.svc.PublishVersion(ctx, tenantID, agentID, versionID)
	if err != nil {
		return nil, MapDomainError(err)
	}

	protoVer, err := VersionToProto(*ver)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &ardav1.PublishVersionResponse{Version: protoVer}, nil
}

// DeleteDraftVersion deletes an unpublished draft version.
func (s *Server) DeleteDraftVersion(ctx context.Context, req *ardav1.DeleteDraftVersionRequest) (*ardav1.DeleteDraftVersionResponse, error) {
	tenantID, err := s.resolveTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	agentID, err := uuid.Parse(req.GetAgentId())
	if err != nil || agentID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid agent id: must be a valid uuid")
	}

	versionID, err := uuid.Parse(req.GetVersionId())
	if err != nil || versionID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid version id: must be a valid uuid")
	}

	if err := s.svc.DeleteDraftVersion(ctx, tenantID, agentID, versionID); err != nil {
		return nil, MapDomainError(err)
	}

	return &ardav1.DeleteDraftVersionResponse{}, nil
}
