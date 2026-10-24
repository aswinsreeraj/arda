package execution_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/services/execution"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mockWorkflowRun struct {
	workflowID string
	runID      string
}

func (m *mockWorkflowRun) GetID() string                                       { return m.workflowID }
func (m *mockWorkflowRun) GetRunID() string                                    { return m.runID }
func (m *mockWorkflowRun) Get(ctx context.Context, valuePtr interface{}) error { return nil }
func (m *mockWorkflowRun) GetWithOptions(ctx context.Context, valuePtr interface{}, options client.WorkflowRunGetOptions) error {
	return nil
}
func (m *mockWorkflowRun) GetFirstExecutionRunID() string { return m.runID }

type mockWorkflowDispatcher struct {
	startFn   func(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error)
	lastWfID  string
	lastInput temporal.AgentExecutionInput
	calls     int
}

func (m *mockWorkflowDispatcher) StartAgentExecutionWorkflow(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error) {
	m.calls++
	m.lastWfID = workflowID
	m.lastInput = input
	if m.startFn != nil {
		return m.startFn(ctx, workflowID, input)
	}
	return &mockWorkflowRun{workflowID: workflowID, runID: "run-test-123"}, nil
}

type mockRegistryClient struct {
	getVersionFn  func(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (*domain.AgentVersion, error)
	lastTenantID  uuid.UUID
	lastAgentID   uuid.UUID
	lastVersionID uuid.UUID
	calls         int
}

func (m *mockRegistryClient) GetAgentVersion(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	m.calls++
	m.lastTenantID = tenantID
	m.lastAgentID = agentID
	m.lastVersionID = versionID
	if m.getVersionFn != nil {
		return m.getVersionFn(ctx, tenantID, agentID, versionID)
	}
	return &domain.AgentVersion{
		ID:            versionID,
		AgentID:       agentID,
		VersionNumber: 1,
		Published:     true,
		Configuration: domain.Configuration{
			ExecutionMode: domain.ExecutionModeAutonomous,
		},
	}, nil
}

func defaultTestContext() (auth.Identity, uuid.UUID, uuid.UUID) {
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	identity := auth.Identity{
		UserID:   "dev-user-1",
		TenantID: tenantID,
		Role:     auth.RoleAgentDeveloper,
	}

	return identity, agentID, versionID
}

func TestService_SubmitExecution_Success(t *testing.T) {
	identity, agentID, versionID := defaultTestContext()
	dispatcher := &mockWorkflowDispatcher{}
	registry := &mockRegistryClient{}

	svc, err := execution.NewService(dispatcher, registry)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	req := execution.SubmitExecutionRequest{
		AgentID:        agentID,
		AgentVersionID: versionID,
		Input: map[string]any{
			"prompt": "Summarize customer records",
		},
		ExecutionMode: domain.ExecutionModeAutonomous,
	}

	resp, err := svc.SubmitExecution(context.Background(), identity, req)
	if err != nil {
		t.Fatalf("expected successful execution submission, got: %v", err)
	}

	if resp.Status != "ACCEPTED" {
		t.Errorf("expected status 'ACCEPTED', got %q", resp.Status)
	}
	if resp.AgentID != agentID {
		t.Errorf("expected agent ID %s, got %s", agentID, resp.AgentID)
	}
	if resp.AgentVersionID != versionID {
		t.Errorf("expected agent version ID %s, got %s", versionID, resp.AgentVersionID)
	}
	if resp.WorkflowID == "" || resp.RunID == "" {
		t.Errorf("expected non-empty workflow_id and run_id, got %q, %q", resp.WorkflowID, resp.RunID)
	}
	if resp.ExecutionMode != domain.ExecutionModeAutonomous {
		t.Errorf("expected execution mode 'autonomous', got %q", resp.ExecutionMode)
	}

	// Verify dispatcher received exact parameters
	if dispatcher.calls != 1 {
		t.Errorf("expected 1 dispatcher call, got %d", dispatcher.calls)
	}
	if dispatcher.lastInput.TenantID != identity.TenantID {
		t.Errorf("expected tenant ID %s, got %s", identity.TenantID, dispatcher.lastInput.TenantID)
	}
	if dispatcher.lastInput.AgentID != agentID {
		t.Errorf("expected agent ID %s, got %s", agentID, dispatcher.lastInput.AgentID)
	}
	if dispatcher.lastInput.AgentVersionID != versionID {
		t.Errorf("expected version ID %s, got %s", versionID, dispatcher.lastInput.AgentVersionID)
	}
	if dispatcher.lastInput.ExecutionInput["prompt"] != "Summarize customer records" {
		t.Errorf("unexpected execution input: %+v", dispatcher.lastInput.ExecutionInput)
	}
}

func TestService_SubmitExecution_ExactVersionPinning(t *testing.T) {
	identity, agentID, _ := defaultTestContext()
	v1ID := uuid.New()
	v2ID := uuid.New()

	dispatcher := &mockWorkflowDispatcher{}
	registry := &mockRegistryClient{
		getVersionFn: func(ctx context.Context, tenantID, aID, verID uuid.UUID) (*domain.AgentVersion, error) {
			if verID == v1ID {
				return &domain.AgentVersion{ID: v1ID, AgentID: aID, VersionNumber: 1}, nil
			}
			if verID == v2ID {
				return &domain.AgentVersion{ID: v2ID, AgentID: aID, VersionNumber: 2}, nil
			}
			return nil, domain.NewNotFoundError("version not found")
		},
	}

	svc, err := execution.NewService(dispatcher, registry)
	if err != nil {
		t.Fatalf("unexpected error creating service: %v", err)
	}

	// Submit with v1
	req1 := execution.SubmitExecutionRequest{
		AgentID:        agentID,
		AgentVersionID: v1ID,
	}
	resp1, err := svc.SubmitExecution(context.Background(), identity, req1)
	if err != nil {
		t.Fatalf("unexpected error submitting v1: %v", err)
	}
	if resp1.AgentVersionID != v1ID || dispatcher.lastInput.AgentVersionID != v1ID {
		t.Errorf("expected v1 pinned, got %s", dispatcher.lastInput.AgentVersionID)
	}

	// Submit with v2
	req2 := execution.SubmitExecutionRequest{
		AgentID:        agentID,
		AgentVersionID: v2ID,
	}
	resp2, err := svc.SubmitExecution(context.Background(), identity, req2)
	if err != nil {
		t.Fatalf("unexpected error submitting v2: %v", err)
	}
	if resp2.AgentVersionID != v2ID || dispatcher.lastInput.AgentVersionID != v2ID {
		t.Errorf("expected v2 pinned, got %s", dispatcher.lastInput.AgentVersionID)
	}

	// Submit unconfigured v3 should fail with NOT_FOUND
	req3 := execution.SubmitExecutionRequest{
		AgentID:        agentID,
		AgentVersionID: uuid.New(),
	}
	_, err = svc.SubmitExecution(context.Background(), identity, req3)
	if err == nil {
		t.Fatalf("expected error for unconfigured version, got nil")
	}
	var execErr *execution.ExecutionError
	if !errors.As(err, &execErr) || execErr.Code != execution.CodeNotFound {
		t.Errorf("expected NOT_FOUND error, got %v", err)
	}
}

func TestService_SubmitExecution_MissingTenantContext(t *testing.T) {
	dispatcher := &mockWorkflowDispatcher{}
	registry := &mockRegistryClient{}
	svc, _ := execution.NewService(dispatcher, registry)

	unauthIdentity := auth.Identity{
		UserID:   "user-no-tenant",
		TenantID: uuid.Nil,
		Role:     auth.RoleAgentDeveloper,
	}

	req := execution.SubmitExecutionRequest{
		AgentID:        uuid.New(),
		AgentVersionID: uuid.New(),
	}

	_, err := svc.SubmitExecution(context.Background(), unauthIdentity, req)
	if err == nil {
		t.Fatalf("expected error for missing tenant context, got nil")
	}
	var execErr *execution.ExecutionError
	if !errors.As(err, &execErr) || execErr.Code != execution.CodeUnauthenticated {
		t.Errorf("expected UNAUTHENTICATED error, got %v", err)
	}
}

func TestService_SubmitExecution_RoleAuthorization(t *testing.T) {
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	dispatcher := &mockWorkflowDispatcher{}
	registry := &mockRegistryClient{}
	svc, _ := execution.NewService(dispatcher, registry)

	req := execution.SubmitExecutionRequest{
		AgentID:        agentID,
		AgentVersionID: versionID,
	}

	deniedRoles := []auth.Role{auth.RoleSuperAdmin, auth.RoleReviewer}
	for _, role := range deniedRoles {
		t.Run("denied_"+string(role), func(t *testing.T) {
			id := auth.Identity{
				UserID:   "user-" + string(role),
				TenantID: tenantID,
				Role:     role,
			}
			_, err := svc.SubmitExecution(context.Background(), id, req)
			if err == nil {
				t.Fatalf("expected forbidden error for role %s, got nil", role)
			}
			var execErr *execution.ExecutionError
			if !errors.As(err, &execErr) || execErr.Code != execution.CodeForbidden {
				t.Errorf("expected FORBIDDEN error, got %v", err)
			}
		})
	}

	allowedRoles := []auth.Role{auth.RoleCompanyAdmin, auth.RoleAgentDeveloper}
	for _, role := range allowedRoles {
		t.Run("allowed_"+string(role), func(t *testing.T) {
			id := auth.Identity{
				UserID:   "user-" + string(role),
				TenantID: tenantID,
				Role:     role,
			}
			resp, err := svc.SubmitExecution(context.Background(), id, req)
			if err != nil {
				t.Fatalf("expected success for role %s, got %v", role, err)
			}
			if resp.Status != "ACCEPTED" {
				t.Errorf("expected status 'ACCEPTED', got %s", resp.Status)
			}
		})
	}
}

func TestService_SubmitExecution_InvalidIdentifiers(t *testing.T) {
	identity, agentID, versionID := defaultTestContext()
	dispatcher := &mockWorkflowDispatcher{}
	registry := &mockRegistryClient{}
	svc, _ := execution.NewService(dispatcher, registry)

	tests := []struct {
		name string
		req  execution.SubmitExecutionRequest
	}{
		{
			name: "nil agent_id",
			req: execution.SubmitExecutionRequest{
				AgentID:        uuid.Nil,
				AgentVersionID: versionID,
			},
		},
		{
			name: "nil agent_version_id",
			req: execution.SubmitExecutionRequest{
				AgentID:        agentID,
				AgentVersionID: uuid.Nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.SubmitExecution(context.Background(), identity, tt.req)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			var execErr *execution.ExecutionError
			if !errors.As(err, &execErr) || execErr.Code != execution.CodeValidationFailed {
				t.Errorf("expected VALIDATION_FAILED, got %v", err)
			}
		})
	}
}

func TestService_SubmitExecution_VersionOwnershipAndNotFound(t *testing.T) {
	identity, agentID, versionID := defaultTestContext()
	dispatcher := &mockWorkflowDispatcher{}

	t.Run("registry returns domain NotFound", func(t *testing.T) {
		registry := &mockRegistryClient{
			getVersionFn: func(ctx context.Context, tenantID, aID, verID uuid.UUID) (*domain.AgentVersion, error) {
				return nil, domain.NewNotFoundError("version does not exist")
			},
		}
		svc, _ := execution.NewService(dispatcher, registry)
		_, err := svc.SubmitExecution(context.Background(), identity, execution.SubmitExecutionRequest{
			AgentID:        agentID,
			AgentVersionID: versionID,
		})
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		var execErr *execution.ExecutionError
		if !errors.As(err, &execErr) || execErr.Code != execution.CodeNotFound {
			t.Errorf("expected NOT_FOUND, got %v", err)
		}
	})

	t.Run("registry returns grpc NotFound", func(t *testing.T) {
		registry := &mockRegistryClient{
			getVersionFn: func(ctx context.Context, tenantID, aID, verID uuid.UUID) (*domain.AgentVersion, error) {
				return nil, status.Error(codes.NotFound, "agent version not found")
			},
		}
		svc, _ := execution.NewService(dispatcher, registry)
		_, err := svc.SubmitExecution(context.Background(), identity, execution.SubmitExecutionRequest{
			AgentID:        agentID,
			AgentVersionID: versionID,
		})
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		var execErr *execution.ExecutionError
		if !errors.As(err, &execErr) || execErr.Code != execution.CodeNotFound {
			t.Errorf("expected NOT_FOUND, got %v", err)
		}
	})

	t.Run("version agent ID does not match request agent ID", func(t *testing.T) {
		differentAgentID := uuid.New()
		registry := &mockRegistryClient{
			getVersionFn: func(ctx context.Context, tenantID, aID, verID uuid.UUID) (*domain.AgentVersion, error) {
				return &domain.AgentVersion{
					ID:      verID,
					AgentID: differentAgentID, // Mismatch!
				}, nil
			},
		}
		svc, _ := execution.NewService(dispatcher, registry)
		_, err := svc.SubmitExecution(context.Background(), identity, execution.SubmitExecutionRequest{
			AgentID:        agentID,
			AgentVersionID: versionID,
		})
		if err == nil {
			t.Fatalf("expected error for agent mismatch, got nil")
		}
		var execErr *execution.ExecutionError
		if !errors.As(err, &execErr) || execErr.Code != execution.CodeNotFound {
			t.Errorf("expected NOT_FOUND, got %v", err)
		}
	})
}

func TestService_SubmitExecution_ExecutionModeValidation(t *testing.T) {
	identity, agentID, versionID := defaultTestContext()
	dispatcher := &mockWorkflowDispatcher{}
	registry := &mockRegistryClient{}
	svc, _ := execution.NewService(dispatcher, registry)

	t.Run("unsupported execution mode is rejected", func(t *testing.T) {
		req := execution.SubmitExecutionRequest{
			AgentID:        agentID,
			AgentVersionID: versionID,
			ExecutionMode:  "unsupported_mode",
		}
		_, err := svc.SubmitExecution(context.Background(), identity, req)
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		var execErr *execution.ExecutionError
		if !errors.As(err, &execErr) || execErr.Code != execution.CodeValidationFailed {
			t.Errorf("expected VALIDATION_FAILED, got %v", err)
		}
	})

	supportedModes := []domain.ExecutionMode{
		domain.ExecutionModeAutonomous,
		domain.ExecutionModeSupervised,
		domain.ExecutionModeHumanInTheLoop,
		domain.ExecutionModeGated,
	}

	for _, mode := range supportedModes {
		t.Run("supported_"+string(mode), func(t *testing.T) {
			req := execution.SubmitExecutionRequest{
				AgentID:        agentID,
				AgentVersionID: versionID,
				ExecutionMode:  mode,
			}
			resp, err := svc.SubmitExecution(context.Background(), identity, req)
			if err != nil {
				t.Fatalf("expected success for supported mode %s, got %v", mode, err)
			}
			if resp.ExecutionMode != mode {
				t.Errorf("expected %s, got %s", mode, resp.ExecutionMode)
			}
		})
	}
}

func TestService_SubmitExecution_WorkflowIDGenerationAndCollision(t *testing.T) {
	identity, agentID, versionID := defaultTestContext()
	registry := &mockRegistryClient{}

	t.Run("custom execution ID generates deterministic workflow ID", func(t *testing.T) {
		dispatcher := &mockWorkflowDispatcher{}
		svc, _ := execution.NewService(dispatcher, registry)

		req := execution.SubmitExecutionRequest{
			AgentID:        agentID,
			AgentVersionID: versionID,
			ExecutionID:    "custom-exec-456",
		}

		resp, err := svc.SubmitExecution(context.Background(), identity, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expectedWfID := fmt.Sprintf("agent-exec-%s-custom-exec-456", agentID)
		if resp.WorkflowID != expectedWfID || dispatcher.lastWfID != expectedWfID {
			t.Errorf("expected workflow ID %q, got %q", expectedWfID, resp.WorkflowID)
		}
	})

	t.Run("duplicate execution submission returns CONFLICT", func(t *testing.T) {
		dispatcher := &mockWorkflowDispatcher{
			startFn: func(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error) {
				return nil, errors.New("WorkflowExecutionAlreadyStarted: execution is already running")
			},
		}
		svc, _ := execution.NewService(dispatcher, registry)

		req := execution.SubmitExecutionRequest{
			AgentID:        agentID,
			AgentVersionID: versionID,
			ExecutionID:    "duplicate-id",
		}

		_, err := svc.SubmitExecution(context.Background(), identity, req)
		if err == nil {
			t.Fatalf("expected duplicate conflict error, got nil")
		}
		var execErr *execution.ExecutionError
		if !errors.As(err, &execErr) || execErr.Code != execution.CodeConflict {
			t.Errorf("expected CONFLICT error code, got %v", err)
		}
		if execErr.HTTPStatus != 409 {
			t.Errorf("expected HTTP 409, got %d", execErr.HTTPStatus)
		}
	})

	t.Run("temporal client error maps to UPSTREAM_ERROR", func(t *testing.T) {
		dispatcher := &mockWorkflowDispatcher{
			startFn: func(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error) {
				return nil, errors.New("temporal server connection refused")
			},
		}
		svc, _ := execution.NewService(dispatcher, registry)

		req := execution.SubmitExecutionRequest{
			AgentID:        agentID,
			AgentVersionID: versionID,
		}

		_, err := svc.SubmitExecution(context.Background(), identity, req)
		if err == nil {
			t.Fatalf("expected upstream error, got nil")
		}
		var execErr *execution.ExecutionError
		if !errors.As(err, &execErr) || execErr.Code != execution.CodeUpstreamError {
			t.Errorf("expected UPSTREAM_ERROR, got %v", err)
		}
		if execErr.HTTPStatus != 502 {
			t.Errorf("expected HTTP 502, got %d", execErr.HTTPStatus)
		}
	})
}

func TestNewService_NilDependencies(t *testing.T) {
	dispatcher := &mockWorkflowDispatcher{}
	registry := &mockRegistryClient{}

	if _, err := execution.NewService(nil, registry); err == nil {
		t.Errorf("expected error for nil dispatcher, got nil")
	}
	if _, err := execution.NewService(dispatcher, nil); err == nil {
		t.Errorf("expected error for nil registry, got nil")
	}
}

// Mock gRPC Client for testing GRPCRegistryAdapter
type mockAgentRegistryGRPCClient struct {
	ardav1.AgentRegistryServiceClient
	getVerFn func(ctx context.Context, in *ardav1.GetAgentVersionRequest, opts ...grpc.CallOption) (*ardav1.GetAgentVersionResponse, error)
}

func (m *mockAgentRegistryGRPCClient) GetAgentVersion(ctx context.Context, in *ardav1.GetAgentVersionRequest, opts ...grpc.CallOption) (*ardav1.GetAgentVersionResponse, error) {
	if m.getVerFn != nil {
		return m.getVerFn(ctx, in, opts...)
	}
	return &ardav1.GetAgentVersionResponse{
		Version: &ardav1.AgentVersion{
			Id:            in.GetVersionId(),
			AgentId:       in.GetAgentId(),
			VersionNumber: 1,
			Published:     true,
			Configuration: &ardav1.AgentConfiguration{
				ExecutionMode: ardav1.ExecutionMode_EXECUTION_MODE_AUTONOMOUS,
			},
		},
	}, nil
}

func TestGRPCRegistryAdapter(t *testing.T) {
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	grpcClient := &mockAgentRegistryGRPCClient{}
	adapter := execution.NewGRPCRegistryAdapter(grpcClient)

	ver, err := adapter.GetAgentVersion(context.Background(), tenantID, agentID, versionID)
	if err != nil {
		t.Fatalf("unexpected error from adapter: %v", err)
	}
	if ver.ID != versionID || ver.AgentID != agentID {
		t.Errorf("unexpected version returned: %+v", ver)
	}
	if ver.Configuration.ExecutionMode != domain.ExecutionModeAutonomous {
		t.Errorf("expected autonomous mode, got %s", ver.Configuration.ExecutionMode)
	}
}
