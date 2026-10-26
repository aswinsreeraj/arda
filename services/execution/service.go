package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	registrygrpc "github.com/aswinsreeraj/arda/services/registry/grpc"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// WorkflowDispatcher defines the contract for submitting execution workflows to Temporal.
type WorkflowDispatcher interface {
	StartAgentExecutionWorkflow(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error)
}

// RegistryClient defines the contract for verifying agent and version ownership within a tenant.
type RegistryClient interface {
	GetAgentVersion(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (*domain.AgentVersion, error)
}

// RegistryClientFunc adapts a function to the RegistryClient interface.
type RegistryClientFunc func(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (*domain.AgentVersion, error)

// GetAgentVersion delegates to the wrapped function.
func (f RegistryClientFunc) GetAgentVersion(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	return f(ctx, tenantID, agentID, versionID)
}

// GRPCRegistryAdapter adapts an ardav1.AgentRegistryServiceClient to the RegistryClient interface.
type GRPCRegistryAdapter struct {
	client ardav1.AgentRegistryServiceClient
}

// NewGRPCRegistryAdapter wraps a generated gRPC client.
func NewGRPCRegistryAdapter(client ardav1.AgentRegistryServiceClient) *GRPCRegistryAdapter {
	return &GRPCRegistryAdapter{client: client}
}

// GetAgentVersion queries the gRPC registry service and converts the response to a domain.AgentVersion.
func (a *GRPCRegistryAdapter) GetAgentVersion(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	if a == nil || a.client == nil {
		return nil, errors.New("grpc registry client is nil")
	}

	md := metadata.New(map[string]string{
		registrygrpc.MetadataTenantID: tenantID.String(),
	})
	outCtx := metadata.NewOutgoingContext(ctx, md)

	req := &ardav1.GetAgentVersionRequest{
		TenantId:  tenantID.String(),
		AgentId:   agentID.String(),
		VersionId: versionID.String(),
	}

	resp, err := a.client.GetAgentVersion(outCtx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.GetVersion() == nil {
		return nil, domain.NewNotFoundError("agent version not found")
	}

	protoVer := resp.GetVersion()
	parsedID, err := uuid.Parse(protoVer.GetId())
	if err != nil {
		return nil, fmt.Errorf("invalid version id from registry: %w", err)
	}
	parsedAgentID, err := uuid.Parse(protoVer.GetAgentId())
	if err != nil {
		return nil, fmt.Errorf("invalid agent id from registry: %w", err)
	}

	mode := domain.ExecutionModeAutonomous
	if protoVer.GetConfiguration() != nil {
		switch protoVer.GetConfiguration().GetExecutionMode() {
		case ardav1.ExecutionMode_EXECUTION_MODE_SUPERVISED:
			mode = domain.ExecutionModeSupervised
		case ardav1.ExecutionMode_EXECUTION_MODE_HUMAN_IN_THE_LOOP:
			mode = domain.ExecutionModeHumanInTheLoop
		case ardav1.ExecutionMode_EXECUTION_MODE_GATED:
			mode = domain.ExecutionModeGated
		}
	}

	return &domain.AgentVersion{
		ID:            parsedID,
		AgentID:       parsedAgentID,
		VersionNumber: int(protoVer.GetVersionNumber()),
		Published:     protoVer.GetPublished(),
		Configuration: domain.Configuration{
			ExecutionMode: mode,
		},
	}, nil
}

// Service coordinates agent execution requests, tenant authorization, version verification,
// and Temporal workflow submission.
type Service struct {
	dispatcher WorkflowDispatcher
	registry   RegistryClient
}

// NewService constructs an Execution Service instance.
func NewService(dispatcher WorkflowDispatcher, registry RegistryClient) (*Service, error) {
	if dispatcher == nil {
		return nil, errors.New("execution service: workflow dispatcher cannot be nil")
	}
	if registry == nil {
		return nil, errors.New("execution service: registry client cannot be nil")
	}

	return &Service{
		dispatcher: dispatcher,
		registry:   registry,
	}, nil
}

// GenerateWorkflowID constructs a collision-safe, deterministic workflow identifier.
// If executionID is supplied by the caller (idempotency key), it is incorporated deterministically.
func GenerateWorkflowID(agentID uuid.UUID, executionID string) string {
	cleaned := strings.TrimSpace(executionID)
	if cleaned != "" {
		return fmt.Sprintf("agent-exec-%s-%s", agentID, cleaned)
	}
	return fmt.Sprintf("agent-exec-%s-%s", agentID, uuid.New().String())
}

// SubmitExecution authorizes the caller within their tenant context, verifies that the target agent version
// exists and belongs to the authorized tenant, preserves exact version pinning, and dispatches the execution
// workflow to Temporal.
func (s *Service) SubmitExecution(ctx context.Context, identity auth.Identity, req SubmitExecutionRequest) (*SubmitExecutionResponse, error) {
	if identity.TenantID == uuid.Nil {
		return nil, NewUnauthenticatedError("unauthenticated caller: missing tenant identity in context")
	}

	if err := auth.Authorize(identity, auth.ActionAgentExecute); err != nil {
		return nil, NewForbiddenError(err.Error())
	}

	if err := req.Validate(); err != nil {
		var execErr *ExecutionError
		if errors.As(err, &execErr) {
			return nil, execErr
		}
		return nil, NewValidationError(err.Error())
	}

	// Verify target agent version belongs to authorized tenant
	ver, err := s.registry.GetAgentVersion(ctx, identity.TenantID, req.AgentID, req.AgentVersionID)
	if err != nil {
		var domErr *domain.DomainError
		if errors.As(err, &domErr) {
			switch domErr.Code {
			case domain.CodeNotFound:
				return nil, NewNotFoundError("agent version not found or does not belong to authorized tenant")
			case domain.CodeUnauthenticated:
				return nil, NewUnauthenticatedError("unauthenticated registry access")
			default:
				return nil, NewValidationError(domErr.Message)
			}
		}

		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.NotFound:
				return nil, NewNotFoundError("agent version not found or does not belong to authorized tenant")
			case codes.InvalidArgument:
				return nil, NewValidationError(st.Message())
			case codes.Unauthenticated:
				return nil, NewUnauthenticatedError(st.Message())
			case codes.PermissionDenied:
				return nil, NewForbiddenError(st.Message())
			}
		}

		return nil, NewUpstreamError("failed to verify agent version with registry", err)
	}

	if ver == nil {
		return nil, NewNotFoundError("agent version not found")
	}

	if ver.AgentID != req.AgentID {
		return nil, NewNotFoundError("agent version does not belong to the specified agent")
	}

	execMode := req.ExecutionMode
	if execMode == "" {
		if ver.Configuration.ExecutionMode != "" {
			execMode = ver.Configuration.ExecutionMode
		} else {
			execMode = domain.ExecutionModeAutonomous
		}
	}

	workflowID := GenerateWorkflowID(req.AgentID, req.ExecutionID)

	// Pin execution strictly to the validated version ID
	wfInput := temporal.AgentExecutionInput{
		TenantID:       identity.TenantID,
		AgentID:        req.AgentID,
		AgentVersionID: req.AgentVersionID,
		ExecutionInput: req.Input,
	}

	run, err := s.dispatcher.StartAgentExecutionWorkflow(ctx, workflowID, wfInput)
	if err != nil {
		if strings.Contains(err.Error(), "WorkflowExecutionAlreadyStarted") ||
			strings.Contains(err.Error(), "already started") {
			return nil, NewConflictError(fmt.Sprintf("execution already in progress for workflow ID: %s", workflowID), err)
		}
		return nil, NewUpstreamError("failed to dispatch execution workflow to temporal", err)
	}

	return &SubmitExecutionResponse{
		WorkflowID:     run.GetID(),
		RunID:          run.GetRunID(),
		AgentID:        req.AgentID,
		AgentVersionID: req.AgentVersionID,
		Status:         "ACCEPTED",
		ExecutionMode:  execMode,
		Mode:           "async",
	}, nil
}
