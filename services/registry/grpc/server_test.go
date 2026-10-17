package grpc_test

import (
	"context"
	"net"
	"testing"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	registrygrpc "github.com/aswinsreeraj/arda/services/registry/grpc"
	"github.com/aswinsreeraj/arda/services/registry/service"
	"github.com/aswinsreeraj/arda/services/registry/store"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
)

type memoryStore struct {
	agents        map[uuid.UUID]*domain.Agent
	versions      map[uuid.UUID]*domain.AgentVersion
	agentVersions map[uuid.UUID][]*domain.AgentVersion
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		agents:        make(map[uuid.UUID]*domain.Agent),
		versions:      make(map[uuid.UUID]*domain.AgentVersion),
		agentVersions: make(map[uuid.UUID][]*domain.AgentVersion),
	}
}

func (m *memoryStore) CreateAgent(ctx context.Context, agent *domain.Agent) error {
	m.agents[agent.ID] = agent
	return nil
}

func (m *memoryStore) CreateAgentWithInitialVersion(ctx context.Context, agent *domain.Agent, version *domain.AgentVersion) error {
	m.agents[agent.ID] = agent
	m.versions[version.ID] = version
	m.agentVersions[agent.ID] = append(m.agentVersions[agent.ID], version)
	return nil
}

func (m *memoryStore) GetAgent(ctx context.Context, tenantID, agentID uuid.UUID) (*domain.Agent, error) {
	agent, ok := m.agents[agentID]
	if !ok || agent.TenantID != tenantID {
		return nil, domain.NewNotFoundError("agent not found")
	}
	return agent, nil
}

func (m *memoryStore) ListAgents(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.Agent, int, error) {
	var matched []domain.Agent
	for _, a := range m.agents {
		if a.TenantID == tenantID {
			matched = append(matched, *a)
		}
	}
	total := len(matched)
	if offset >= total {
		return []domain.Agent{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return matched[offset:end], total, nil
}

func (m *memoryStore) CreateVersion(ctx context.Context, version *domain.AgentVersion) error {
	m.versions[version.ID] = version
	m.agentVersions[version.AgentID] = append(m.agentVersions[version.AgentID], version)
	return nil
}

func (m *memoryStore) GetVersion(ctx context.Context, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	ver, ok := m.versions[versionID]
	if !ok || ver.AgentID != agentID {
		return nil, domain.NewNotFoundError("agent version not found")
	}
	return ver, nil
}

func (m *memoryStore) GetLatestPublishedVersion(ctx context.Context, agentID uuid.UUID) (*domain.AgentVersion, error) {
	vers := m.agentVersions[agentID]
	for i := len(vers) - 1; i >= 0; i-- {
		if vers[i].Published {
			return vers[i], nil
		}
	}
	return nil, domain.NewNotFoundError("no published version found for agent")
}

func (m *memoryStore) GetLatestDraftVersion(ctx context.Context, agentID uuid.UUID) (*domain.AgentVersion, error) {
	vers := m.agentVersions[agentID]
	for i := len(vers) - 1; i >= 0; i-- {
		if !vers[i].Published {
			return vers[i], nil
		}
	}
	return nil, nil
}

func (m *memoryStore) GetNextVersionNumber(ctx context.Context, agentID uuid.UUID) (int, error) {
	return len(m.agentVersions[agentID]) + 1, nil
}

func (m *memoryStore) PublishVersion(ctx context.Context, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	ver, ok := m.versions[versionID]
	if !ok || ver.AgentID != agentID {
		return nil, domain.NewNotFoundError("version not found")
	}
	if ver.Published {
		return nil, domain.NewConflictError(domain.CodeAlreadyPublished, "version is already published")
	}
	ver.Published = true
	return ver, nil
}

func (m *memoryStore) UpdateDraftVersion(ctx context.Context, version *domain.AgentVersion) error {
	ver, ok := m.versions[version.ID]
	if !ok {
		return domain.NewNotFoundError("version not found")
	}
	if ver.Published {
		return domain.NewConflictError(domain.CodePublishedImmutable, "published version is immutable")
	}
	ver.Configuration = version.Configuration
	return nil
}

func (m *memoryStore) DeleteVersion(ctx context.Context, agentID, versionID uuid.UUID) error {
	ver, ok := m.versions[versionID]
	if !ok {
		return domain.NewNotFoundError("version not found")
	}
	if ver.Published {
		return domain.NewConflictError(domain.CodePublishedImmutable, "published version is immutable")
	}
	delete(m.versions, versionID)
	return nil
}

func setupGRPCTest(t *testing.T, st store.Store) (ardav1.AgentRegistryServiceClient, func()) {
	t.Helper()
	svc := service.NewRegistryService(st)
	server := registrygrpc.NewServer(svc)

	lis := bufconn.Listen(1024 * 1024)
	go func() {
		if err := server.Serve(lis); err != nil && err != grpc.ErrServerStopped {
			// server stopped
		}
	}()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufnet: %v", err)
	}

	client := ardav1.NewAgentRegistryServiceClient(conn)
	cleanup := func() {
		_ = conn.Close()
		server.GracefulStop()
		_ = lis.Close()
	}
	return client, cleanup
}

func authCtx(tenantID uuid.UUID) context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		registrygrpc.MetadataTenantID, tenantID.String(),
		registrygrpc.MetadataUserID, "test-user-1",
		registrygrpc.MetadataRole, "company_admin",
	))
}

func TestAgentRegistryGRPC_FullLifecycle(t *testing.T) {
	mem := newMemoryStore()
	client, cleanup := setupGRPCTest(t, mem)
	defer cleanup()

	tenantA := uuid.New()
	tenantB := uuid.New()
	ctxA := authCtx(tenantA)
	ctxB := authCtx(tenantB)

	// 1. Unauthenticated call (missing metadata)
	_, errUnauth := client.CreateAgent(context.Background(), &ardav1.CreateAgentRequest{Name: "No Auth"})
	if status.Code(errUnauth) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got: %v", errUnauth)
	}

	// 2. Tenant mismatch in request body vs metadata
	_, errMismatch := client.CreateAgent(ctxA, &ardav1.CreateAgentRequest{
		TenantId: tenantB.String(),
		Name:     "Spoofed Agent",
	})
	if status.Code(errMismatch) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied on tenant mismatch, got: %v", errMismatch)
	}

	// 3. Create Agent with initial config
	guardrails, _ := structpb.NewStruct(map[string]any{"max_tokens": float64(1000)})
	createResp, err := client.CreateAgent(ctxA, &ardav1.CreateAgentRequest{
		TenantId: tenantA.String(),
		Name:     "Support Agent",
		Configuration: &ardav1.AgentConfiguration{
			PromptTemplate:       "Answer questions",
			Tools:                []string{"search"},
			ReliabilityThreshold: 0.9,
			ExecutionMode:        ardav1.ExecutionMode_EXECUTION_MODE_GATED,
			Guardrails:           guardrails,
		},
	})
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}
	agent := createResp.GetAgent().GetAgent()
	agentID := agent.GetId()
	if agent.GetName() != "Support Agent" || agent.GetTenantId() != tenantA.String() {
		t.Errorf("unexpected agent: %+v", agent)
	}

	// 4. Get Agent
	getResp, err := client.GetAgent(ctxA, &ardav1.GetAgentRequest{
		TenantId: tenantA.String(),
		AgentId:  agentID,
	})
	if err != nil {
		t.Fatalf("failed to get agent: %v", err)
	}
	if getResp.GetAgent().GetAgent().GetId() != agentID {
		t.Errorf("agent id mismatch: %s vs %s", getResp.GetAgent().GetAgent().GetId(), agentID)
	}

	// 5. Tenant Isolation on GetAgent: Tenant B querying Tenant A
	_, errCross := client.GetAgent(ctxB, &ardav1.GetAgentRequest{
		TenantId: tenantB.String(),
		AgentId:  agentID,
	})
	if status.Code(errCross) != codes.NotFound {
		t.Fatalf("expected NotFound for cross-tenant query, got: %v", errCross)
	}

	// 6. List Agents
	listResp, err := client.ListAgents(ctxA, &ardav1.ListAgentsRequest{
		TenantId: tenantA.String(),
		Pagination: &ardav1.PaginationRequest{
			PageSize: 10,
		},
	})
	if err != nil {
		t.Fatalf("failed to list agents: %v", err)
	}
	if len(listResp.GetAgents()) != 1 {
		t.Errorf("expected 1 agent, got %d", len(listResp.GetAgents()))
	}

	// 7. Create Draft Version 2
	verResp, err := client.CreateAgentVersion(ctxA, &ardav1.CreateAgentVersionRequest{
		TenantId: tenantA.String(),
		AgentId:  agentID,
		Configuration: &ardav1.AgentConfiguration{
			PromptTemplate:       "v2 prompt",
			Tools:                []string{"search", "calculator"},
			ReliabilityThreshold: 0.85,
			ExecutionMode:        ardav1.ExecutionMode_EXECUTION_MODE_SUPERVISED,
		},
	})
	if err != nil {
		t.Fatalf("failed to create draft version: %v", err)
	}
	v2 := verResp.GetVersion()
	if v2.GetVersionNumber() != 2 || v2.GetPublished() {
		t.Errorf("unexpected version: %+v", v2)
	}

	// 8. Update Draft Version 2
	upResp, err := client.UpdateDraftVersion(ctxA, &ardav1.UpdateDraftVersionRequest{
		TenantId:  tenantA.String(),
		AgentId:   agentID,
		VersionId: v2.GetId(),
		Configuration: &ardav1.AgentConfiguration{
			PromptTemplate:       "updated v2 prompt",
			Tools:                []string{"search"},
			ReliabilityThreshold: 0.88,
			ExecutionMode:        ardav1.ExecutionMode_EXECUTION_MODE_AUTONOMOUS,
		},
	})
	if err != nil {
		t.Fatalf("failed to update draft version: %v", err)
	}
	if upResp.GetVersion().GetConfiguration().GetPromptTemplate() != "updated v2 prompt" {
		t.Errorf("draft version update not reflected")
	}

	// 9. Get Version 2
	getVerResp, err := client.GetAgentVersion(ctxA, &ardav1.GetAgentVersionRequest{
		TenantId:  tenantA.String(),
		AgentId:   agentID,
		VersionId: v2.GetId(),
	})
	if err != nil {
		t.Fatalf("failed to get agent version: %v", err)
	}
	if getVerResp.GetVersion().GetId() != v2.GetId() {
		t.Errorf("version id mismatch")
	}

	// 10. Publish Version 2
	pubResp, err := client.PublishVersion(ctxA, &ardav1.PublishVersionRequest{
		TenantId:  tenantA.String(),
		AgentId:   agentID,
		VersionId: v2.GetId(),
	})
	if err != nil {
		t.Fatalf("failed to publish version: %v", err)
	}
	if !pubResp.GetVersion().GetPublished() {
		t.Errorf("expected version to be published")
	}

	// 11. Immutability: Attempt update on published version 2
	_, errImmutable := client.UpdateDraftVersion(ctxA, &ardav1.UpdateDraftVersionRequest{
		TenantId:  tenantA.String(),
		AgentId:   agentID,
		VersionId: v2.GetId(),
		Configuration: &ardav1.AgentConfiguration{
			PromptTemplate: "tampered prompt",
		},
	})
	if status.Code(errImmutable) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition on mutating published version, got: %v", errImmutable)
	}

	// 12. Immutability: Attempt delete on published version 2
	_, errDelPub := client.DeleteDraftVersion(ctxA, &ardav1.DeleteDraftVersionRequest{
		TenantId:  tenantA.String(),
		AgentId:   agentID,
		VersionId: v2.GetId(),
	})
	if status.Code(errDelPub) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition on deleting published version, got: %v", errDelPub)
	}

	// 13. Get Latest Published Version
	latestResp, err := client.GetLatestPublishedVersion(ctxA, &ardav1.GetLatestPublishedVersionRequest{
		TenantId: tenantA.String(),
		AgentId:  agentID,
	})
	if err != nil {
		t.Fatalf("failed to get latest published version: %v", err)
	}
	if latestResp.GetVersion().GetId() != v2.GetId() {
		t.Errorf("latest published version mismatch")
	}

	// 14. Delete Draft Version (v1 is draft in initial agent creation)
	v1ID := createResp.GetAgent().GetDraftVersion().GetId()
	_, errDelDraft := client.DeleteDraftVersion(ctxA, &ardav1.DeleteDraftVersionRequest{
		TenantId:  tenantA.String(),
		AgentId:   agentID,
		VersionId: v1ID,
	})
	if errDelDraft != nil {
		t.Fatalf("failed to delete draft version: %v", errDelDraft)
	}
}
