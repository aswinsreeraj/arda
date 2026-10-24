package agentruntime_test

import (
	"context"
	"errors"
	"testing"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	registrygrpc "github.com/aswinsreeraj/arda/services/registry/grpc"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

type mockRegistryGRPCClient struct {
	getAgentVersionFn func(ctx context.Context, in *ardav1.GetAgentVersionRequest, opts ...grpc.CallOption) (*ardav1.GetAgentVersionResponse, error)
}

func (m *mockRegistryGRPCClient) CreateAgent(ctx context.Context, in *ardav1.CreateAgentRequest, opts ...grpc.CallOption) (*ardav1.CreateAgentResponse, error) {
	return nil, nil
}
func (m *mockRegistryGRPCClient) GetAgent(ctx context.Context, in *ardav1.GetAgentRequest, opts ...grpc.CallOption) (*ardav1.GetAgentResponse, error) {
	return nil, nil
}
func (m *mockRegistryGRPCClient) ListAgents(ctx context.Context, in *ardav1.ListAgentsRequest, opts ...grpc.CallOption) (*ardav1.ListAgentsResponse, error) {
	return nil, nil
}
func (m *mockRegistryGRPCClient) CreateAgentVersion(ctx context.Context, in *ardav1.CreateAgentVersionRequest, opts ...grpc.CallOption) (*ardav1.CreateAgentVersionResponse, error) {
	return nil, nil
}
func (m *mockRegistryGRPCClient) GetAgentVersion(ctx context.Context, in *ardav1.GetAgentVersionRequest, opts ...grpc.CallOption) (*ardav1.GetAgentVersionResponse, error) {
	if m.getAgentVersionFn != nil {
		return m.getAgentVersionFn(ctx, in, opts...)
	}
	return nil, status.Error(codes.NotFound, "agent version not found")
}
func (m *mockRegistryGRPCClient) GetLatestPublishedVersion(ctx context.Context, in *ardav1.GetLatestPublishedVersionRequest, opts ...grpc.CallOption) (*ardav1.GetLatestPublishedVersionResponse, error) {
	return nil, nil
}
func (m *mockRegistryGRPCClient) UpdateDraftVersion(ctx context.Context, in *ardav1.UpdateDraftVersionRequest, opts ...grpc.CallOption) (*ardav1.UpdateDraftVersionResponse, error) {
	return nil, nil
}
func (m *mockRegistryGRPCClient) PublishVersion(ctx context.Context, in *ardav1.PublishVersionRequest, opts ...grpc.CallOption) (*ardav1.PublishVersionResponse, error) {
	return nil, nil
}
func (m *mockRegistryGRPCClient) DeleteDraftVersion(ctx context.Context, in *ardav1.DeleteDraftVersionRequest, opts ...grpc.CallOption) (*ardav1.DeleteDraftVersionResponse, error) {
	return nil, nil
}

func TestRegistryConfigProvider_Success(t *testing.T) {
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	var receivedTenantMD string
	guardrails, _ := structpb.NewStruct(map[string]any{"model": "gpt-4o", "temperature": 0.7})
	budgets, _ := structpb.NewStruct(map[string]any{"max_cost_cents": 100.0})

	mockClient := &mockRegistryGRPCClient{
		getAgentVersionFn: func(ctx context.Context, in *ardav1.GetAgentVersionRequest, opts ...grpc.CallOption) (*ardav1.GetAgentVersionResponse, error) {
			if md, ok := metadata.FromOutgoingContext(ctx); ok {
				vals := md.Get(registrygrpc.MetadataTenantID)
				if len(vals) > 0 {
					receivedTenantMD = vals[0]
				}
			}

			if in.GetTenantId() != tenantID.String() {
				return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
			}
			if in.GetAgentId() != agentID.String() || in.GetVersionId() != versionID.String() {
				return nil, status.Error(codes.NotFound, "version not found")
			}

			return &ardav1.GetAgentVersionResponse{
				Version: &ardav1.AgentVersion{
					Id:            versionID.String(),
					AgentId:       agentID.String(),
					VersionNumber: 1,
					Published:     true,
					Configuration: &ardav1.AgentConfiguration{
						PromptTemplate:       "Answer: {{query}}",
						Tools:                []string{"calculator"},
						ReliabilityThreshold: 0.90,
						ExecutionMode:        ardav1.ExecutionMode_EXECUTION_MODE_AUTONOMOUS,
						Guardrails:           guardrails,
						Budgets:              budgets,
					},
				},
			}, nil
		},
	}

	provider, err := agentruntime.NewRegistryConfigProvider(mockClient)
	if err != nil {
		t.Fatalf("unexpected error creating provider: %v", err)
	}

	cfg, err := provider.GetAgentConfig(context.Background(), tenantID, agentID, versionID)
	if err != nil {
		t.Fatalf("unexpected error retrieving config: %v", err)
	}

	if receivedTenantMD != tenantID.String() {
		t.Errorf("expected outgoing metadata tenant %s, got %s", tenantID, receivedTenantMD)
	}
	if cfg.PromptTemplate != "Answer: {{query}}" {
		t.Errorf("expected prompt template 'Answer: {{query}}', got %q", cfg.PromptTemplate)
	}
	if cfg.ExecutionMode != domain.ExecutionModeAutonomous {
		t.Errorf("expected autonomous mode, got %s", cfg.ExecutionMode)
	}
	if cfg.ReliabilityThreshold != 0.90 {
		t.Errorf("expected reliability threshold 0.90, got %f", cfg.ReliabilityThreshold)
	}
	if len(cfg.Tools) != 1 || cfg.Tools[0] != "calculator" {
		t.Errorf("expected tools ['calculator'], got %v", cfg.Tools)
	}
}

func TestRegistryConfigProvider_DraftVersionRejectedForExecution(t *testing.T) {
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	mockClient := &mockRegistryGRPCClient{
		getAgentVersionFn: func(ctx context.Context, in *ardav1.GetAgentVersionRequest, opts ...grpc.CallOption) (*ardav1.GetAgentVersionResponse, error) {
			return &ardav1.GetAgentVersionResponse{
				Version: &ardav1.AgentVersion{
					Id:            versionID.String(),
					AgentId:       agentID.String(),
					VersionNumber: 2,
					Published:     false, // Draft version!
					Configuration: &ardav1.AgentConfiguration{
						PromptTemplate: "draft prompt",
					},
				},
			}, nil
		},
	}

	provider, err := agentruntime.NewRegistryConfigProvider(mockClient)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = provider.GetAgentConfig(context.Background(), tenantID, agentID, versionID)
	if err == nil {
		t.Fatalf("expected error for draft version in execution, got nil")
	}
	if !errors.Is(err, agentruntime.ErrInvalidConfiguration) {
		t.Errorf("expected ErrInvalidConfiguration for draft version, got %v", err)
	}

	// Test with WithRequirePublished(false)
	permissiveProvider, _ := agentruntime.NewRegistryConfigProvider(mockClient, agentruntime.WithRequirePublished(false))
	cfg, err := permissiveProvider.GetAgentConfig(context.Background(), tenantID, agentID, versionID)
	if err != nil {
		t.Fatalf("unexpected error with permissive requirePublished: %v", err)
	}
	if cfg.PromptTemplate != "draft prompt" {
		t.Errorf("expected 'draft prompt', got %q", cfg.PromptTemplate)
	}
}

func TestRegistryConfigProvider_ErrorMappings(t *testing.T) {
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	t.Run("invalid input identifiers", func(t *testing.T) {
		provider, _ := agentruntime.NewRegistryConfigProvider(&mockRegistryGRPCClient{})

		_, err := provider.GetAgentConfig(context.Background(), uuid.Nil, agentID, versionID)
		if !errors.Is(err, agentruntime.ErrInvalidInput) {
			t.Errorf("expected ErrInvalidInput for nil tenant ID, got %v", err)
		}

		_, err = provider.GetAgentConfig(context.Background(), tenantID, uuid.Nil, versionID)
		if !errors.Is(err, agentruntime.ErrInvalidInput) {
			t.Errorf("expected ErrInvalidInput for nil agent ID, got %v", err)
		}

		_, err = provider.GetAgentConfig(context.Background(), tenantID, agentID, uuid.Nil)
		if !errors.Is(err, agentruntime.ErrInvalidInput) {
			t.Errorf("expected ErrInvalidInput for nil version ID, got %v", err)
		}
	})

	t.Run("404 NotFound mapped to ErrConfigurationNotFound", func(t *testing.T) {
		mockClient := &mockRegistryGRPCClient{
			getAgentVersionFn: func(ctx context.Context, in *ardav1.GetAgentVersionRequest, opts ...grpc.CallOption) (*ardav1.GetAgentVersionResponse, error) {
				return nil, status.Error(codes.NotFound, "version not found")
			},
		}
		provider, _ := agentruntime.NewRegistryConfigProvider(mockClient)

		_, err := provider.GetAgentConfig(context.Background(), tenantID, agentID, versionID)
		if !errors.Is(err, agentruntime.ErrConfigurationNotFound) {
			t.Errorf("expected ErrConfigurationNotFound, got %v", err)
		}
	})

	t.Run("PermissionDenied mapped to ErrPermanent", func(t *testing.T) {
		mockClient := &mockRegistryGRPCClient{
			getAgentVersionFn: func(ctx context.Context, in *ardav1.GetAgentVersionRequest, opts ...grpc.CallOption) (*ardav1.GetAgentVersionResponse, error) {
				return nil, status.Error(codes.PermissionDenied, "unauthorized tenant")
			},
		}
		provider, _ := agentruntime.NewRegistryConfigProvider(mockClient)

		_, err := provider.GetAgentConfig(context.Background(), tenantID, agentID, versionID)
		if !errors.Is(err, agentruntime.ErrPermanent) {
			t.Errorf("expected ErrPermanent, got %v", err)
		}
	})

	t.Run("ownership mismatch returns ErrInvalidConfiguration", func(t *testing.T) {
		otherAgentID := uuid.New()
		mockClient := &mockRegistryGRPCClient{
			getAgentVersionFn: func(ctx context.Context, in *ardav1.GetAgentVersionRequest, opts ...grpc.CallOption) (*ardav1.GetAgentVersionResponse, error) {
				return &ardav1.GetAgentVersionResponse{
					Version: &ardav1.AgentVersion{
						Id:        versionID.String(),
						AgentId:   otherAgentID.String(), // Mismatched!
						Published: true,
					},
				}, nil
			},
		}
		provider, _ := agentruntime.NewRegistryConfigProvider(mockClient)

		_, err := provider.GetAgentConfig(context.Background(), tenantID, agentID, versionID)
		if !errors.Is(err, agentruntime.ErrInvalidConfiguration) {
			t.Errorf("expected ErrInvalidConfiguration for mismatched agent ID, got %v", err)
		}
	})

	t.Run("nil client returns error in constructor", func(t *testing.T) {
		_, err := agentruntime.NewRegistryConfigProvider(nil)
		if err == nil {
			t.Errorf("expected error for nil client in constructor, got nil")
		}
	})
}
