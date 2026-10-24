package gateway_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/services/execution"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	gatewayhttp "github.com/aswinsreeraj/arda/services/gateway/http"
	"github.com/aswinsreeraj/arda/services/gateway/proxy"
	"github.com/aswinsreeraj/arda/services/gateway/ratelimit"
	"github.com/google/uuid"
)

func TestGateway_ExecutionServiceIntegration(t *testing.T) {
	tenantID := uuid.New()
	agentID := uuid.New()
	pinnedVersionID := uuid.New()

	val, err := auth.NewTokenValidator(getTestConfig())
	if err != nil {
		t.Fatalf("failed to initialize token validator: %v", err)
	}

	devToken, _ := auth.GenerateTestToken(auth.Identity{
		UserID:   "dev-1",
		TenantID: tenantID,
		Role:     auth.RoleAgentDeveloper,
	}, getTestConfig(), time.Hour)

	t.Run("1. successful forwarding to Execution Service with exact pinned version", func(t *testing.T) {
		var receivedReq execution.SubmitExecutionRequest
		var receivedAuthHeader string
		var receivedTenantHeader string

		mockExecServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedAuthHeader = r.Header.Get("Authorization")
			receivedTenantHeader = r.Header.Get("X-Tenant-ID")

			_ = json.NewDecoder(r.Body).Decode(&receivedReq)

			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(execution.SubmitExecutionResponse{
				WorkflowID:     "agent-exec-" + agentID.String() + "-idemp-1",
				RunID:          "run-12345",
				AgentID:        agentID,
				AgentVersionID: pinnedVersionID,
				Status:         "ACCEPTED",
				Mode:           "async",
			})
		}))
		defer mockExecServer.Close()

		execClient, err := proxy.NewExecutionClient(mockExecServer.URL)
		if err != nil {
			t.Fatalf("failed to create exec client: %v", err)
		}
		defer execClient.Close()

		gw := gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
			JWTValidator:    val,
			RateLimiter:     ratelimit.NewLimiter(ratelimit.Config{Enabled: false}),
			ExecutionClient: execClient,
		})
		gwServer := httptest.NewServer(gw)
		defer gwServer.Close()

		payload := map[string]any{
			"agent_version_id": pinnedVersionID.String(),
			"execution_id":     "idemp-1",
			"input": map[string]any{
				"query": "market trend analysis",
			},
		}

		headers := map[string]string{
			"X-Tenant-ID": "spoofed-tenant-uuid",
		}
		resp, body := doRequest(gwServer, "POST", fmt.Sprintf("/api/v1/agents/%s/execute", agentID), devToken, headers, payload)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted, got %d: %s", resp.StatusCode, string(body))
		}

		var execResp gatewayhttp.ExecuteAgentResponse
		if err := json.Unmarshal(body, &execResp); err != nil {
			t.Fatalf("failed to parse execute response: %v", err)
		}

		if execResp.Status != "ACCEPTED" {
			t.Errorf("expected status ACCEPTED, got %s", execResp.Status)
		}
		if execResp.AgentVersionID != pinnedVersionID {
			t.Errorf("expected pinned version ID %s, got %s", pinnedVersionID, execResp.AgentVersionID)
		}
		if execResp.WorkflowID != "agent-exec-"+agentID.String()+"-idemp-1" {
			t.Errorf("expected workflow ID with execution ID, got %s", execResp.WorkflowID)
		}

		// Verify forwarded parameters
		if receivedReq.AgentID != agentID {
			t.Errorf("expected downstream agent ID %s, got %s", agentID, receivedReq.AgentID)
		}
		if receivedReq.AgentVersionID != pinnedVersionID {
			t.Errorf("expected downstream pinned version ID %s, got %s", pinnedVersionID, receivedReq.AgentVersionID)
		}
		if receivedReq.ExecutionID != "idemp-1" {
			t.Errorf("expected downstream execution ID 'idemp-1', got %q", receivedReq.ExecutionID)
		}
		if receivedAuthHeader != "Bearer "+devToken {
			t.Errorf("expected Authorization header forwarded, got %q", receivedAuthHeader)
		}
		if receivedTenantHeader != "" {
			t.Errorf("expected spoofed X-Tenant-ID to be stripped, got %q", receivedTenantHeader)
		}
	})

	t.Run("2. synchronous mode is rejected with 400 VALIDATION_FAILED", func(t *testing.T) {
		mockExecServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}))
		defer mockExecServer.Close()

		execClient, _ := proxy.NewExecutionClient(mockExecServer.URL)
		defer execClient.Close()

		gw := gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
			JWTValidator:    val,
			RateLimiter:     ratelimit.NewLimiter(ratelimit.Config{Enabled: false}),
			ExecutionClient: execClient,
		})
		gwServer := httptest.NewServer(gw)
		defer gwServer.Close()

		payload := map[string]any{
			"agent_version_id": pinnedVersionID.String(),
			"mode":             "sync",
			"input":            map[string]any{"query": "calculate"},
		}

		resp, body := doRequest(gwServer, "POST", fmt.Sprintf("/api/v1/agents/%s/execute", agentID), devToken, nil, payload)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for mode: sync, got %d (body: %s)", resp.StatusCode, string(body))
		}

		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "VALIDATION_FAILED" {
			t.Errorf("expected VALIDATION_FAILED code, got %s", errResp.Error.Code)
		}
	})

	t.Run("3. downstream duplicate conflict maps to 409 CONFLICT", func(t *testing.T) {
		mockExecServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":    "CONFLICT",
					"message": "execution already in progress for workflow ID: agent-exec-123-dup",
				},
			})
		}))
		defer mockExecServer.Close()

		execClient, _ := proxy.NewExecutionClient(mockExecServer.URL)
		defer execClient.Close()

		gw := gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
			JWTValidator:    val,
			RateLimiter:     ratelimit.NewLimiter(ratelimit.Config{Enabled: false}),
			ExecutionClient: execClient,
		})
		gwServer := httptest.NewServer(gw)
		defer gwServer.Close()

		payload := map[string]any{
			"agent_version_id": pinnedVersionID.String(),
			"execution_id":     "dup",
		}

		resp, body := doRequest(gwServer, "POST", fmt.Sprintf("/api/v1/agents/%s/execute", agentID), devToken, nil, payload)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("expected 409 Conflict, got %d: %s", resp.StatusCode, string(body))
		}

		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "CONFLICT" {
			t.Errorf("expected CONFLICT code, got %s", errResp.Error.Code)
		}
	})

	t.Run("4. downstream timeout and error mapping to 502 UPSTREAM_ERROR", func(t *testing.T) {
		mockExecServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":    "UPSTREAM_ERROR",
					"message": "temporal connection timeout",
				},
			})
		}))
		defer mockExecServer.Close()

		execClient, _ := proxy.NewExecutionClient(mockExecServer.URL)
		defer execClient.Close()

		gw := gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
			JWTValidator:    val,
			RateLimiter:     ratelimit.NewLimiter(ratelimit.Config{Enabled: false}),
			ExecutionClient: execClient,
		})
		gwServer := httptest.NewServer(gw)
		defer gwServer.Close()

		payload := map[string]any{
			"agent_version_id": pinnedVersionID.String(),
		}

		resp, body := doRequest(gwServer, "POST", fmt.Sprintf("/api/v1/agents/%s/execute", agentID), devToken, nil, payload)
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("expected 502 Bad Gateway, got %d: %s", resp.StatusCode, string(body))
		}

		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "UPSTREAM_ERROR" {
			t.Errorf("expected UPSTREAM_ERROR code, got %s", errResp.Error.Code)
		}
	})
}
