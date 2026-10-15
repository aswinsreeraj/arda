package registry_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/database"
	"github.com/aswinsreeraj/arda/services/registry"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	registryhttp "github.com/aswinsreeraj/arda/services/registry/http"
	"github.com/aswinsreeraj/arda/services/registry/service"
	"github.com/google/uuid"
)

func getTestDB(t *testing.T) *database.DB {
	cfg := database.LoadConfigFromEnv()
	db, err := database.Connect(cfg)
	if err != nil {
		t.Skipf("skipping integration test: database connection failed: %v", err)
	}
	return db
}

func createTestTenant(t *testing.T, db *database.DB, name string) uuid.UUID {
	id := uuid.New()
	query := `INSERT INTO tenants (id, name, status) VALUES (?, ?, 'active')`
	if err := db.Exec(query, id, name).Error; err != nil {
		t.Fatalf("failed to create test tenant: %v", err)
	}
	return id
}

func TestAgentRegistry_Integration(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	tenantA := createTestTenant(t, db, fmt.Sprintf("Tenant A %s", uuid.New().String()[:8]))
	tenantB := createTestTenant(t, db, fmt.Sprintf("Tenant B %s", uuid.New().String()[:8]))

	server := registry.NewServer(db.DB, nil)
	ts := httptest.NewServer(server.Handler)
	defer ts.Close()

	client := ts.Client()

	// Helper for HTTP requests
	doRequest := func(method, path string, tenantID *uuid.UUID, body any) (*http.Response, []byte) {
		var reqBody []byte
		if body != nil {
			var err error
			reqBody, err = json.Marshal(body)
			if err != nil {
				t.Fatalf("failed to marshal request body: %v", err)
			}
		}
		req, err := http.NewRequest(method, ts.URL+path, bytes.NewReader(reqBody))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if tenantID != nil {
			req.Header.Set(registryhttp.TenantHeader, tenantID.String())
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		return resp, buf.Bytes()
	}

	t.Run("unauthenticated request rejected", func(t *testing.T) {
		resp, _ := doRequest("POST", "/api/v1/agents", nil, map[string]string{"name": "No Auth"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected status 401 without tenant header, got %d", resp.StatusCode)
		}
	})

	var createdAgentID uuid.UUID
	var draftV1ID uuid.UUID

	t.Run("tenant A creates agent with initial draft version", func(t *testing.T) {
		createPayload := service.CreateAgentRequest{
			Name:           "Tenant A Customer Support",
			PromptTemplate: "You are a customer support agent.",
			Tools:          []string{"kb_search", "create_ticket"},
			ExecutionMode:  domain.ExecutionModeSupervised,
		}
		resp, body := doRequest("POST", "/api/v1/agents", &tenantA, createPayload)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected status 201, got %d; body: %s", resp.StatusCode, string(body))
		}

		var detail domain.AgentDetail
		if err := json.Unmarshal(body, &detail); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if detail.Agent.Name != "Tenant A Customer Support" {
			t.Errorf("agent name mismatch: %s", detail.Agent.Name)
		}
		if detail.DraftVersion == nil || detail.DraftVersion.VersionNumber != 1 {
			t.Fatalf("expected initial draft version 1, got %+v", detail.DraftVersion)
		}
		if detail.DraftVersion.Published {
			t.Errorf("expected initial version to be draft, got published=true")
		}

		createdAgentID = detail.Agent.ID
		draftV1ID = detail.DraftVersion.ID
	})

	t.Run("tenant isolation: tenant B cannot access tenant A's agent", func(t *testing.T) {
		// Attempt retrieval as Tenant B
		resp, body := doRequest("GET", fmt.Sprintf("/api/v1/agents/%s", createdAgentID), &tenantB, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected status 404 for tenant B lookup, got %d; body: %s", resp.StatusCode, string(body))
		}

		// Attempt to create version as Tenant B
		cfg := domain.Configuration{
			PromptTemplate:       "Malicious prompt",
			Tools:                []string{"dump_data"},
			ReliabilityThreshold: 0.5,
			ExecutionMode:        domain.ExecutionModeAutonomous,
		}
		resp, _ = doRequest("POST", fmt.Sprintf("/api/v1/agents/%s/versions", createdAgentID), &tenantB, cfg)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected status 404 for tenant B version creation, got %d", resp.StatusCode)
		}

		// Attempt to publish as Tenant B
		resp, _ = doRequest("POST", fmt.Sprintf("/api/v1/agents/%s/versions/%s/publish", createdAgentID, draftV1ID), &tenantB, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected status 404 for tenant B publish attempt, got %d", resp.StatusCode)
		}
	})

	t.Run("tenant A can update draft version", func(t *testing.T) {
		updatedCfg := domain.Configuration{
			PromptTemplate:       "Updated support prompt",
			Tools:                []string{"kb_search", "ticket_update"},
			ReliabilityThreshold: 0.9,
			ExecutionMode:        domain.ExecutionModeSupervised,
		}
		resp, body := doRequest("PUT", fmt.Sprintf("/api/v1/agents/%s/versions/%s", createdAgentID, draftV1ID), &tenantA, updatedCfg)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d; body: %s", resp.StatusCode, string(body))
		}

		var ver domain.AgentVersion
		if err := json.Unmarshal(body, &ver); err != nil {
			t.Fatalf("failed to decode version: %v", err)
		}
		if ver.Configuration.PromptTemplate != "Updated support prompt" {
			t.Errorf("draft prompt template not updated")
		}
	})

	t.Run("tenant A publishes version 1", func(t *testing.T) {
		resp, body := doRequest("POST", fmt.Sprintf("/api/v1/agents/%s/versions/%s/publish", createdAgentID, draftV1ID), &tenantA, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200 on publish, got %d; body: %s", resp.StatusCode, string(body))
		}

		var pubVer domain.AgentVersion
		if err := json.Unmarshal(body, &pubVer); err != nil {
			t.Fatalf("failed to decode published version: %v", err)
		}
		if !pubVer.Published || pubVer.PublishedAt == nil {
			t.Errorf("version not marked as published or published_at is nil")
		}

		// Republish attempt must fail with 409 Conflict
		respConflict, _ := doRequest("POST", fmt.Sprintf("/api/v1/agents/%s/versions/%s/publish", createdAgentID, draftV1ID), &tenantA, nil)
		if respConflict.StatusCode != http.StatusConflict {
			t.Errorf("expected status 409 on republishing, got %d", respConflict.StatusCode)
		}
	})

	t.Run("published version is immutable: rejects modification and deletion", func(t *testing.T) {
		tamperedCfg := domain.Configuration{
			PromptTemplate:       "Tampered prompt",
			Tools:                []string{"tamper"},
			ReliabilityThreshold: 0.5,
			ExecutionMode:        domain.ExecutionModeAutonomous,
		}
		// Attempt update on published version
		respPut, _ := doRequest("PUT", fmt.Sprintf("/api/v1/agents/%s/versions/%s", createdAgentID, draftV1ID), &tenantA, tamperedCfg)
		if respPut.StatusCode != http.StatusConflict {
			t.Errorf("expected status 409 when updating published version, got %d", respPut.StatusCode)
		}

		// Attempt delete on published version
		respDel, _ := doRequest("DELETE", fmt.Sprintf("/api/v1/agents/%s/versions/%s", createdAgentID, draftV1ID), &tenantA, nil)
		if respDel.StatusCode != http.StatusConflict {
			t.Errorf("expected status 409 when deleting published version, got %d", respDel.StatusCode)
		}
	})

	t.Run("resolve latest published version returns v1", func(t *testing.T) {
		resp, body := doRequest("GET", fmt.Sprintf("/api/v1/agents/%s/versions/latest", createdAgentID), &tenantA, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d; body: %s", resp.StatusCode, string(body))
		}

		var latest domain.AgentVersion
		if err := json.Unmarshal(body, &latest); err != nil {
			t.Fatalf("failed to decode latest version: %v", err)
		}
		if latest.ID != draftV1ID || latest.VersionNumber != 1 {
			t.Errorf("expected latest version to be v1, got version %d", latest.VersionNumber)
		}
	})

	t.Run("create and publish v2 keeps v1 immutable and historical", func(t *testing.T) {
		cfg2 := domain.Configuration{
			PromptTemplate:       "V2 prompt with advanced tools",
			Tools:                []string{"kb_search", "escalate"},
			ReliabilityThreshold: 0.95,
			ExecutionMode:        domain.ExecutionModeAutonomous,
		}
		// Create draft v2
		resp2, body2 := doRequest("POST", fmt.Sprintf("/api/v1/agents/%s/versions", createdAgentID), &tenantA, cfg2)
		if resp2.StatusCode != http.StatusCreated {
			t.Fatalf("expected status 201 for v2 creation, got %d; body: %s", resp2.StatusCode, string(body2))
		}

		var v2 domain.AgentVersion
		if err := json.Unmarshal(body2, &v2); err != nil {
			t.Fatalf("failed to decode v2: %v", err)
		}
		if v2.VersionNumber != 2 {
			t.Errorf("expected version number 2, got %d", v2.VersionNumber)
		}

		// Publish v2
		respPub2, _ := doRequest("POST", fmt.Sprintf("/api/v1/agents/%s/versions/%s/publish", createdAgentID, v2.ID), &tenantA, nil)
		if respPub2.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200 publishing v2, got %d", respPub2.StatusCode)
		}

		// Resolve latest published version: must now be v2
		respLatest, bodyLatest := doRequest("GET", fmt.Sprintf("/api/v1/agents/%s/versions/latest", createdAgentID), &tenantA, nil)
		if respLatest.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", respLatest.StatusCode)
		}
		var latestVer domain.AgentVersion
		if err := json.Unmarshal(bodyLatest, &latestVer); err != nil {
			t.Fatalf("failed to decode latest: %v", err)
		}
		if latestVer.VersionNumber != 2 {
			t.Errorf("expected latest version to be 2, got %d", latestVer.VersionNumber)
		}

		// Verify v1 in database is still published and not overwritten
		var v1Published bool
		err := db.Raw("SELECT published FROM agent_versions WHERE id = ?", draftV1ID).Scan(&v1Published).Error
		if err != nil {
			t.Fatalf("failed to query v1: %v", err)
		}
		if !v1Published {
			t.Errorf("v1 must remain published after v2 is published")
		}
	})

	t.Run("tenant-scoped version lookup GET /api/v1/agents/{id}/versions/{versionId}", func(t *testing.T) {
		// 1. Tenant A can retrieve its own version v1 (published)
		resp, body := doRequest("GET", fmt.Sprintf("/api/v1/agents/%s/versions/%s", createdAgentID, draftV1ID), &tenantA, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d; body: %s", resp.StatusCode, string(body))
		}
		var ver1 domain.AgentVersion
		if err := json.Unmarshal(body, &ver1); err != nil {
			t.Fatalf("failed to decode version: %v", err)
		}
		if ver1.ID != draftV1ID || ver1.VersionNumber != 1 || !ver1.Published {
			t.Errorf("unexpected version returned: %+v", ver1)
		}

		// 2. Missing tenant header is rejected with 401
		respNoAuth, _ := doRequest("GET", fmt.Sprintf("/api/v1/agents/%s/versions/%s", createdAgentID, draftV1ID), nil, nil)
		if respNoAuth.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected status 401 for missing tenant, got %d", respNoAuth.StatusCode)
		}

		// 3. Invalid agent UUID or version UUID is rejected with 400
		respBadAgent, _ := doRequest("GET", fmt.Sprintf("/api/v1/agents/not-a-uuid/versions/%s", draftV1ID), &tenantA, nil)
		if respBadAgent.StatusCode != http.StatusBadRequest {
			t.Errorf("expected status 400 for bad agent uuid, got %d", respBadAgent.StatusCode)
		}
		respBadVer, _ := doRequest("GET", fmt.Sprintf("/api/v1/agents/%s/versions/not-a-uuid", createdAgentID), &tenantA, nil)
		if respBadVer.StatusCode != http.StatusBadRequest {
			t.Errorf("expected status 400 for bad version uuid, got %d", respBadVer.StatusCode)
		}

		// 4. Nonexistent version returns 404 Not Found
		respNonexistent, _ := doRequest("GET", fmt.Sprintf("/api/v1/agents/%s/versions/%s", createdAgentID, uuid.New()), &tenantA, nil)
		if respNonexistent.StatusCode != http.StatusNotFound {
			t.Errorf("expected status 404 for nonexistent version, got %d", respNonexistent.StatusCode)
		}

		// 5. Tenant B cannot retrieve Tenant A's agent version; returns 404 Not Found
		respCrossTenant, _ := doRequest("GET", fmt.Sprintf("/api/v1/agents/%s/versions/%s", createdAgentID, draftV1ID), &tenantB, nil)
		if respCrossTenant.StatusCode != http.StatusNotFound {
			t.Errorf("expected status 404 for cross-tenant version lookup, got %d", respCrossTenant.StatusCode)
		}

		// 6. Version belonging to a different agent cannot be retrieved through another agent's URL
		agent2Resp, agent2Body := doRequest("POST", "/api/v1/agents", &tenantA, service.CreateAgentRequest{
			Name: "Tenant A Secondary Agent",
		})
		if agent2Resp.StatusCode != http.StatusCreated {
			t.Fatalf("failed to create second agent: %v", string(agent2Body))
		}
		var agent2Detail domain.AgentDetail
		if err := json.Unmarshal(agent2Body, &agent2Detail); err != nil {
			t.Fatalf("failed to decode agent 2: %v", err)
		}

		respCrossAgent, _ := doRequest("GET", fmt.Sprintf("/api/v1/agents/%s/versions/%s", agent2Detail.Agent.ID, draftV1ID), &tenantA, nil)
		if respCrossAgent.StatusCode != http.StatusNotFound {
			t.Errorf("expected status 404 for version belonging to different agent, got %d", respCrossAgent.StatusCode)
		}
	})
}
