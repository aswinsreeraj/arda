package execution_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/services/execution"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func getTestJWTValidator() (*auth.TokenValidator, auth.JWTConfig) {
	cfg := auth.JWTConfig{
		Secret:   []byte("test-execution-secret-key-32-bytes!"),
		Issuer:   "arda-auth",
		Audience: "arda-api",
	}
	v, err := auth.NewTokenValidator(cfg)
	if err != nil {
		panic(err)
	}
	return v, cfg
}

func createTestServer(svc *execution.Service, validator *auth.TokenValidator) *gin.Engine {
	r := gin.New()
	handler := execution.NewHandler(svc, validator)
	handler.RegisterRoutes(r)
	return r
}

func doRequest(ts *httptest.Server, method, path, token string, headers map[string]string, body any) (*http.Response, []byte) {
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}

	req, _ := http.NewRequest(method, ts.URL+path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	return resp, respBody
}

func parseErrorResponse(t *testing.T, body []byte) (code, message string) {
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("failed to parse error response %s: %v", string(body), err)
	}
	return payload.Error.Code, payload.Error.Message
}

func TestHandler_SubmitExecution_Success(t *testing.T) {
	validator, jwtCfg := getTestJWTValidator()
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	dispatcher := &mockWorkflowDispatcher{}
	registry := &mockRegistryClient{}

	svc, err := execution.NewService(dispatcher, registry)
	if err != nil {
		t.Fatalf("unexpected error creating service: %v", err)
	}

	router := createTestServer(svc, validator)
	ts := httptest.NewServer(router)
	defer ts.Close()

	id := auth.Identity{
		UserID:   "dev-1",
		TenantID: tenantID,
		Role:     auth.RoleAgentDeveloper,
	}
	token, _ := auth.GenerateTestToken(id, jwtCfg, time.Hour)

	reqBody := map[string]any{
		"agent_version_id": versionID.String(),
		"input": map[string]any{
			"prompt": "Hello ARDA",
		},
		"execution_mode": "autonomous",
	}

	path := fmt.Sprintf("/v1/agents/%s/execute", agentID)
	resp, body := doRequest(ts, "POST", path, token, nil, reqBody)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d (body: %s)", resp.StatusCode, string(body))
	}

	var submitResp execution.SubmitExecutionResponse
	if err := json.Unmarshal(body, &submitResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if submitResp.Status != "ACCEPTED" {
		t.Errorf("expected status ACCEPTED, got %s", submitResp.Status)
	}
	if submitResp.AgentID != agentID {
		t.Errorf("expected agent ID %s, got %s", agentID, submitResp.AgentID)
	}
	if submitResp.AgentVersionID != versionID {
		t.Errorf("expected version ID %s, got %s", versionID, submitResp.AgentVersionID)
	}
	if submitResp.WorkflowID == "" {
		t.Errorf("expected non-empty workflow_id")
	}
	if submitResp.ExecutionMode != domain.ExecutionModeAutonomous {
		t.Errorf("expected autonomous mode, got %s", submitResp.ExecutionMode)
	}
}

func TestHandler_SubmitExecution_AlternativeRoute(t *testing.T) {
	validator, jwtCfg := getTestJWTValidator()
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	dispatcher := &mockWorkflowDispatcher{}
	registry := &mockRegistryClient{}
	svc, _ := execution.NewService(dispatcher, registry)

	router := createTestServer(svc, validator)
	ts := httptest.NewServer(router)
	defer ts.Close()

	id := auth.Identity{
		UserID:   "admin-1",
		TenantID: tenantID,
		Role:     auth.RoleCompanyAdmin,
	}
	token, _ := auth.GenerateTestToken(id, jwtCfg, time.Hour)

	reqBody := map[string]any{
		"agent_id":         agentID.String(),
		"agent_version_id": versionID.String(),
		"input":            map[string]any{"data": "123"},
	}

	// POST /v1/executions
	resp, body := doRequest(ts, "POST", "/v1/executions", token, nil, reqBody)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on /v1/executions, got %d (body: %s)", resp.StatusCode, string(body))
	}

	// POST /api/v1/executions
	resp2, body2 := doRequest(ts, "POST", "/api/v1/executions", token, nil, reqBody)
	if resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on /api/v1/executions, got %d (body: %s)", resp2.StatusCode, string(body2))
	}
}

func TestHandler_SubmitExecution_AuthenticationAndAuthorization(t *testing.T) {
	validator, jwtCfg := getTestJWTValidator()
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	dispatcher := &mockWorkflowDispatcher{}
	registry := &mockRegistryClient{}
	svc, _ := execution.NewService(dispatcher, registry)

	router := createTestServer(svc, validator)
	ts := httptest.NewServer(router)
	defer ts.Close()

	path := fmt.Sprintf("/v1/agents/%s/execute", agentID)
	reqBody := map[string]any{"agent_version_id": versionID.String()}

	// 1. Unauthenticated (no header)
	resp, body := doRequest(ts, "POST", path, "", nil, reqBody)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", resp.StatusCode)
	}
	code, _ := parseErrorResponse(t, body)
	if code != "UNAUTHENTICATED" {
		t.Errorf("expected code UNAUTHENTICATED, got %s", code)
	}

	// 2. Invalid Token
	resp, body = doRequest(ts, "POST", path, "invalid-bearer-token", nil, reqBody)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", resp.StatusCode)
	}

	// 3. Forbidden Role (Reviewer)
	reviewerID := auth.Identity{UserID: "rev-1", TenantID: tenantID, Role: auth.RoleReviewer}
	revTok, _ := auth.GenerateTestToken(reviewerID, jwtCfg, time.Hour)
	resp, body = doRequest(ts, "POST", path, revTok, nil, reqBody)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got %d", resp.StatusCode)
	}
	code, _ = parseErrorResponse(t, body)
	if code != "FORBIDDEN" {
		t.Errorf("expected code FORBIDDEN, got %s", code)
	}
}

func TestHandler_SubmitExecution_ValidationErrors(t *testing.T) {
	validator, jwtCfg := getTestJWTValidator()
	tenantID := uuid.New()
	agentID := uuid.New()

	dispatcher := &mockWorkflowDispatcher{}
	registry := &mockRegistryClient{}
	svc, _ := execution.NewService(dispatcher, registry)

	router := createTestServer(svc, validator)
	ts := httptest.NewServer(router)
	defer ts.Close()

	id := auth.Identity{UserID: "dev-1", TenantID: tenantID, Role: auth.RoleAgentDeveloper}
	token, _ := auth.GenerateTestToken(id, jwtCfg, time.Hour)

	// 1. Malformed JSON
	resp, body := doRequest(ts, "POST", fmt.Sprintf("/v1/agents/%s/execute", agentID), token, nil, nil)
	// Passing invalid body manually
	req, _ := http.NewRequest("POST", ts.URL+fmt.Sprintf("/v1/agents/%s/execute", agentID), bytes.NewReader([]byte("{malformed-json")))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, _ = (&http.Client{}).Do(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for malformed JSON, got %d", resp.StatusCode)
	}

	// 2. Non-UUID in URL path
	resp, body = doRequest(ts, "POST", "/v1/agents/not-a-valid-uuid/execute", token, nil, map[string]any{"agent_version_id": uuid.New().String()})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for invalid UUID in path, got %d", resp.StatusCode)
	}
	code, _ := parseErrorResponse(t, body)
	if code != "VALIDATION_FAILED" {
		t.Errorf("expected VALIDATION_FAILED, got %s", code)
	}

	// 3. Path agent_id and body agent_id mismatch
	bodyAgentID := uuid.New()
	resp, body = doRequest(ts, "POST", fmt.Sprintf("/v1/agents/%s/execute", agentID), token, nil, map[string]any{
		"agent_id":         bodyAgentID.String(),
		"agent_version_id": uuid.New().String(),
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for ID mismatch, got %d", resp.StatusCode)
	}
}

func TestHandler_SubmitExecution_StatusMapping(t *testing.T) {
	validator, jwtCfg := getTestJWTValidator()
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	id := auth.Identity{UserID: "dev-1", TenantID: tenantID, Role: auth.RoleAgentDeveloper}
	token, _ := auth.GenerateTestToken(id, jwtCfg, time.Hour)
	path := fmt.Sprintf("/v1/agents/%s/execute", agentID)

	// 1. Not Found
	t.Run("404 Not Found mapping", func(t *testing.T) {
		registry := &mockRegistryClient{
			getVersionFn: func(ctx context.Context, tenantID, aID, verID uuid.UUID) (*domain.AgentVersion, error) {
				return nil, domain.NewNotFoundError("version not found")
			},
		}
		svc, _ := execution.NewService(&mockWorkflowDispatcher{}, registry)
		ts := httptest.NewServer(createTestServer(svc, validator))
		defer ts.Close()

		resp, body := doRequest(ts, "POST", path, token, nil, map[string]any{"agent_version_id": versionID.String()})
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found, got %d", resp.StatusCode)
		}
		code, _ := parseErrorResponse(t, body)
		if code != "NOT_FOUND" {
			t.Errorf("expected NOT_FOUND, got %s", code)
		}
	})

	// 2. Conflict (duplicate submission)
	t.Run("409 Conflict mapping", func(t *testing.T) {
		dispatcher := &mockWorkflowDispatcher{
			startFn: func(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error) {
				return nil, errors.New("WorkflowExecutionAlreadyStarted")
			},
		}
		svc, _ := execution.NewService(dispatcher, &mockRegistryClient{})
		ts := httptest.NewServer(createTestServer(svc, validator))
		defer ts.Close()

		resp, body := doRequest(ts, "POST", path, token, nil, map[string]any{"agent_version_id": versionID.String()})
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("expected 409 Conflict, got %d", resp.StatusCode)
		}
		code, _ := parseErrorResponse(t, body)
		if code != "CONFLICT" {
			t.Errorf("expected CONFLICT, got %s", code)
		}
	})

	// 3. Upstream Error
	t.Run("502 Upstream Error mapping", func(t *testing.T) {
		dispatcher := &mockWorkflowDispatcher{
			startFn: func(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error) {
				return nil, errors.New("network connection reset")
			},
		}
		svc, _ := execution.NewService(dispatcher, &mockRegistryClient{})
		ts := httptest.NewServer(createTestServer(svc, validator))
		defer ts.Close()

		resp, body := doRequest(ts, "POST", path, token, nil, map[string]any{"agent_version_id": versionID.String()})
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("expected 502 Bad Gateway, got %d", resp.StatusCode)
		}
		code, _ := parseErrorResponse(t, body)
		if code != "UPSTREAM_ERROR" {
			t.Errorf("expected UPSTREAM_ERROR, got %s", code)
		}
	})
}

func TestHandler_SubmitExecution_SpoofedTenantHeaderIgnored(t *testing.T) {
	validator, jwtCfg := getTestJWTValidator()
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	dispatcher := &mockWorkflowDispatcher{}
	registry := &mockRegistryClient{}
	svc, _ := execution.NewService(dispatcher, registry)

	router := createTestServer(svc, validator)
	ts := httptest.NewServer(router)
	defer ts.Close()

	id := auth.Identity{UserID: "dev-1", TenantID: tenantID, Role: auth.RoleAgentDeveloper}
	token, _ := auth.GenerateTestToken(id, jwtCfg, time.Hour)

	spoofedTenant := uuid.New()
	headers := map[string]string{
		"X-Tenant-ID": spoofedTenant.String(),
	}

	reqBody := map[string]any{"agent_version_id": versionID.String()}
	resp, _ := doRequest(ts, "POST", fmt.Sprintf("/v1/agents/%s/execute", agentID), token, headers, reqBody)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", resp.StatusCode)
	}

	if registry.lastTenantID != tenantID {
		t.Fatalf("SECURITY VIOLATION: registry received %s instead of trusted JWT tenant %s", registry.lastTenantID, tenantID)
	}
	if registry.lastTenantID == spoofedTenant {
		t.Fatalf("CRITICAL SECURITY FLAW: spoofed X-Tenant-ID was used!")
	}
	if dispatcher.lastInput.TenantID != tenantID {
		t.Fatalf("SECURITY VIOLATION: dispatcher received %s instead of trusted JWT tenant %s", dispatcher.lastInput.TenantID, tenantID)
	}
}

func TestHandler_HealthCheck(t *testing.T) {
	validator, _ := getTestJWTValidator()
	svc, _ := execution.NewService(&mockWorkflowDispatcher{}, &mockRegistryClient{})
	router := createTestServer(svc, validator)
	ts := httptest.NewServer(router)
	defer ts.Close()

	resp, body := doRequest(ts, "GET", "/healthz", "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /healthz, got %d", resp.StatusCode)
	}

	var res map[string]string
	if err := json.Unmarshal(body, &res); err != nil || res["status"] != "ok" {
		t.Fatalf("expected status 'ok', got %v", res)
	}
}
