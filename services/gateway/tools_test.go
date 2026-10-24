package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/pkg/toolgateway"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	gatewayhttp "github.com/aswinsreeraj/arda/services/gateway/http"
	"github.com/aswinsreeraj/arda/services/gateway/ratelimit"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupToolsGatewayTest(t *testing.T) (*httptest.Server, *auth.TokenValidator, toolgateway.ToolRegistry, toolgateway.Store) {
	val, err := auth.NewTokenValidator(getTestConfig())
	require.NoError(t, err)

	store := toolgateway.NewMemoryStore()
	registry := toolgateway.NewRegistry(store)
	toolsHandler := gatewayhttp.NewToolsHandler(registry, store, gatewayhttp.WriteErrorResponse)

	gw := gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
		JWTValidator: val,
		RateLimiter:  ratelimit.NewLimiter(ratelimit.Config{Enabled: false}),
		ToolsHandler: toolsHandler,
	})

	ts := httptest.NewServer(gw)
	t.Cleanup(ts.Close)

	return ts, val, registry, store
}

func issueUserToken(t *testing.T, val *auth.TokenValidator, tenantID uuid.UUID, role auth.Role) string {
	tok, err := val.IssueToken(auth.Identity{
		TenantID: tenantID,
		UserID:   uuid.New().String(),
		Role:     role,
	}, 15*time.Minute)
	require.NoError(t, err)
	return tok
}

func TestToolsAPI_CRUDAndVersionBump(t *testing.T) {
	ts, val, _, _ := setupToolsGatewayTest(t)
	tenantID := uuid.New()
	adminToken := issueUserToken(t, val, tenantID, auth.RoleCompanyAdmin)

	// 1. Create tool
	createBody := map[string]any{
		"name":           "web_search",
		"description":    "Search the public web",
		"classification": "read",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []any{"query"},
		},
		"handler_id": "builtin:kb_search",
	}

	resp, respBytes := doRequest(ts, "POST", "/api/v1/tools", adminToken, nil, createBody)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var created toolgateway.ToolDefinition
	require.NoError(t, json.Unmarshal(respBytes, &created))
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, "web_search", created.Name)
	assert.Equal(t, 1, created.Version)
	assert.True(t, created.Enabled)
	assert.Equal(t, tenantID, created.TenantID)

	// 2. List tools
	resp, respBytes = doRequest(ts, "GET", "/api/v1/tools", adminToken, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var tools []toolgateway.ToolDefinition
	require.NoError(t, json.Unmarshal(respBytes, &tools))
	require.Len(t, tools, 1)
	assert.Equal(t, created.ID, tools[0].ID)

	// 3. Get tool by ID
	resp, respBytes = doRequest(ts, "GET", "/api/v1/tools/"+created.ID, adminToken, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var fetched toolgateway.ToolDefinition
	require.NoError(t, json.Unmarshal(respBytes, &fetched))
	assert.Equal(t, created.ID, fetched.ID)

	// 4. Update tool (bumps version)
	newDesc := "Updated web search description"
	updateBody := map[string]any{
		"description": newDesc,
	}
	resp, respBytes = doRequest(ts, "PUT", "/api/v1/tools/"+created.ID, adminToken, nil, updateBody)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var updated toolgateway.ToolDefinition
	require.NoError(t, json.Unmarshal(respBytes, &updated))
	assert.Equal(t, newDesc, updated.Description)
	assert.Equal(t, 2, updated.Version) // Version bumped from 1 to 2!

	// 5. Disable tool
	disableBody := map[string]any{
		"enabled": false,
	}
	resp, respBytes = doRequest(ts, "PATCH", "/api/v1/tools/"+created.ID+"/enable", adminToken, nil, disableBody)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var disabled toolgateway.ToolDefinition
	require.NoError(t, json.Unmarshal(respBytes, &disabled))
	assert.False(t, disabled.Enabled)

	// 6. Delete tool
	resp, _ = doRequest(ts, "DELETE", "/api/v1/tools/"+created.ID, adminToken, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Verify not found after delete
	resp, _ = doRequest(ts, "GET", "/api/v1/tools/"+created.ID, adminToken, nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestToolsAPI_TenantIsolation(t *testing.T) {
	ts, val, _, _ := setupToolsGatewayTest(t)
	tenantA := uuid.New()
	tenantB := uuid.New()

	tokenA := issueUserToken(t, val, tenantA, auth.RoleCompanyAdmin)
	tokenB := issueUserToken(t, val, tenantB, auth.RoleCompanyAdmin)

	// Tenant A creates tool
	createBody := map[string]any{
		"name":           "tenant_a_tool",
		"classification": "read",
		"input_schema":   map[string]any{"type": "object"},
		"handler_id":     "builtin:echo",
	}
	resp, respBytes := doRequest(ts, "POST", "/api/v1/tools", tokenA, nil, createBody)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var toolA toolgateway.ToolDefinition
	require.NoError(t, json.Unmarshal(respBytes, &toolA))

	// Tenant B cannot GET Tenant A's tool
	resp, _ = doRequest(ts, "GET", "/api/v1/tools/"+toolA.ID, tokenB, nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// Tenant B cannot DELETE Tenant A's tool
	resp, _ = doRequest(ts, "DELETE", "/api/v1/tools/"+toolA.ID, tokenB, nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// Tenant B's list is empty
	resp, respBytes = doRequest(ts, "GET", "/api/v1/tools", tokenB, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var toolsB []toolgateway.ToolDefinition
	require.NoError(t, json.Unmarshal(respBytes, &toolsB))
	assert.Empty(t, toolsB)
}

func TestToolsAPI_RBACMatrix(t *testing.T) {
	ts, val, _, _ := setupToolsGatewayTest(t)
	tenantID := uuid.New()

	adminToken := issueUserToken(t, val, tenantID, auth.RoleCompanyAdmin)
	devToken := issueUserToken(t, val, tenantID, auth.RoleAgentDeveloper)
	reviewerToken := issueUserToken(t, val, tenantID, auth.RoleReviewer)
	superAdminToken := issueUserToken(t, val, uuid.New(), auth.RoleSuperAdmin)

	validTool := map[string]any{
		"name":           "rbac_tool",
		"classification": "read",
		"input_schema":   map[string]any{"type": "object"},
		"handler_id":     "builtin:echo",
	}

	// 1. SuperAdmin (no tenant tool access) -> 403 Forbidden
	resp, _ := doRequest(ts, "POST", "/api/v1/tools", superAdminToken, nil, validTool)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// 2. Reviewer (read only) -> POST forbidden, GET allowed
	resp, _ = doRequest(ts, "POST", "/api/v1/tools", reviewerToken, nil, validTool)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	resp, _ = doRequest(ts, "GET", "/api/v1/tools", reviewerToken, nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 3. AgentDeveloper -> POST allowed, GET allowed, PUT allowed, DELETE forbidden
	resp, respBytes := doRequest(ts, "POST", "/api/v1/tools", devToken, nil, validTool)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var devCreated toolgateway.ToolDefinition
	require.NoError(t, json.Unmarshal(respBytes, &devCreated))

	desc := "updated by dev"
	resp, _ = doRequest(ts, "PUT", "/api/v1/tools/"+devCreated.ID, devToken, nil, map[string]any{"description": desc})
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Delete by dev is forbidden
	resp, _ = doRequest(ts, "DELETE", "/api/v1/tools/"+devCreated.ID, devToken, nil, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// 4. CompanyAdmin -> DELETE allowed
	resp, _ = doRequest(ts, "DELETE", "/api/v1/tools/"+devCreated.ID, adminToken, nil, nil)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestToolsAPI_AuditEvents(t *testing.T) {
	ts, val, _, store := setupToolsGatewayTest(t)
	tenantID := uuid.New()
	adminToken := issueUserToken(t, val, tenantID, auth.RoleCompanyAdmin)

	// Seed audit events in store
	_ = store.SaveAuditEvent(context.Background(), &toolgateway.AuditEvent{
		EventID:      uuid.New().String(),
		TenantID:     tenantID,
		ExecutionID:  "exec-audit-api",
		ToolName:     "echo_tool",
		InvocationID: "inv-audit-1",
		Allowed:      true,
		DurationMs:   25,
		Timestamp:    time.Now().UTC(),
	})

	resp, respBytes := doRequest(ts, "GET", "/api/v1/tools/audit?limit=10", adminToken, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var events []toolgateway.AuditEvent
	require.NoError(t, json.Unmarshal(respBytes, &events))
	require.Len(t, events, 1)
	assert.Equal(t, "exec-audit-api", events[0].ExecutionID)
	assert.Equal(t, "echo_tool", events[0].ToolName)
	assert.True(t, events[0].Allowed)
}
