package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	gatewayhttp "github.com/aswinsreeraj/arda/services/gateway/http"
	"github.com/aswinsreeraj/arda/services/gateway/proxy"
	"github.com/aswinsreeraj/arda/services/gateway/ratelimit"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	registryhttp "github.com/aswinsreeraj/arda/services/registry/http"
	"github.com/aswinsreeraj/arda/services/registry/service"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
)

var testSecret = []byte("super-secure-test-signing-key-12345")
var testIssuer = "arda-auth"
var testAudience = "arda-api"

func getTestConfig() auth.JWTConfig {
	return auth.JWTConfig{
		Secret:   testSecret,
		Issuer:   testIssuer,
		Audience: testAudience,
	}
}

// Mock ReliabilityService for upstream tests
type mockReliabilityService struct {
	gateFunc func(ctx context.Context, req *ardav1.GateRequest) (*ardav1.GateResponse, error)
}

func (m *mockReliabilityService) Gate(ctx context.Context, req *ardav1.GateRequest) (*ardav1.GateResponse, error) {
	if m == nil {
		return nil, proxy.ErrUnconfigured
	}
	if m.gateFunc != nil {
		return m.gateFunc(ctx, req)
	}
	return &ardav1.GateResponse{
		Confidence: 0.95,
		Outcome:    ardav1.GateOutcome_GATE_OUTCOME_PASS,
		ReasonCode: "CONFIDENT_APPROVAL",
	}, nil
}

func (m *mockReliabilityService) Close() error {
	return nil
}

// Mock RegistryHandler that records the tenant ID it received from context
type mockRegistryHandler struct {
	lastSeenTenantID uuid.UUID
	statusCode       int
}

func (m *mockRegistryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if tenantID, ok := registryhttp.TenantIDFromContext(r.Context()); ok {
		m.lastSeenTenantID = tenantID
	}
	code := m.statusCode
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tenant_id": m.lastSeenTenantID.String(),
		"status":    "handled",
	})
}

func createTestGateway(t *testing.T, regHandler http.Handler, relSvc *mockReliabilityService, rateCfg *ratelimit.Config) (http.Handler, *auth.TokenValidator) {
	val, err := auth.NewTokenValidator(getTestConfig())
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	var limiter *ratelimit.Limiter
	if rateCfg != nil {
		limiter = ratelimit.NewLimiter(*rateCfg)
	} else {
		limiter = ratelimit.NewLimiter(ratelimit.Config{Enabled: false})
	}

	var proxyRel proxy.ReliabilityService
	if relSvc != nil {
		proxyRel = relSvc
	}

	gw := gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
		JWTValidator:       val,
		RateLimiter:        limiter,
		RegistryHandler:    regHandler,
		ReliabilityService: proxyRel,
	})

	return gw, val
}

func doRequest(ts *httptest.Server, method, path, token string, extraHeaders map[string]string, body any) (*http.Response, []byte) {
	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, ts.URL+path, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		panic(err)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	_ = resp.Body.Close()
	return resp, buf.Bytes()
}

func parseErrorResponse(t *testing.T, body []byte) gatewayhttp.ErrorResponse {
	var errResp gatewayhttp.ErrorResponse
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("failed to parse error response (%s): %v", string(body), err)
	}
	return errResp
}

// 1. Missing and malformed bearer tokens
func TestGateway_MissingAndMalformedTokens(t *testing.T) {
	mockReg := &mockRegistryHandler{}
	gw, _ := createTestGateway(t, mockReg, nil, nil)
	ts := httptest.NewServer(gw)
	defer ts.Close()

	t.Run("missing authorization header", func(t *testing.T) {
		resp, body := doRequest(ts, "GET", "/api/v1/agents/123", "", nil, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "UNAUTHENTICATED" {
			t.Errorf("expected UNAUTHENTICATED, got %s", errResp.Error.Code)
		}
	})

	t.Run("basic auth header instead of bearer", func(t *testing.T) {
		resp, _ := doRequest(ts, "GET", "/api/v1/agents/123", "", map[string]string{
			"Authorization": "Basic dXNlcjpwYXNz",
		}, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("empty bearer token", func(t *testing.T) {
		resp, _ := doRequest(ts, "GET", "/api/v1/agents/123", "", map[string]string{
			"Authorization": "Bearer ",
		}, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("malformed token string", func(t *testing.T) {
		resp, _ := doRequest(ts, "GET", "/api/v1/agents/123", "not.a.valid.jwt", nil, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})
}

// 2. Invalid signatures and expired tokens
func TestGateway_InvalidSignatureAndExpiredToken(t *testing.T) {
	mockReg := &mockRegistryHandler{}
	gw, _ := createTestGateway(t, mockReg, nil, nil)
	ts := httptest.NewServer(gw)
	defer ts.Close()

	tenantID := uuid.New()
	id := auth.Identity{
		UserID:   "user-1",
		TenantID: tenantID,
		Role:     auth.RoleCompanyAdmin,
	}

	t.Run("invalid signature rejected", func(t *testing.T) {
		wrongSecretCfg := auth.JWTConfig{
			Secret:   []byte("wrong-signing-secret-key-12345"),
			Issuer:   testIssuer,
			Audience: testAudience,
		}
		tokenWithWrongKey, err := auth.GenerateTestToken(id, wrongSecretCfg, time.Hour)
		if err != nil {
			t.Fatal(err)
		}

		resp, body := doRequest(ts, "GET", "/api/v1/agents/123", tokenWithWrongKey, nil, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 for wrong signature, got %d; body: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("expired token rejected", func(t *testing.T) {
		expiredToken, err := auth.GenerateTestToken(id, getTestConfig(), -10*time.Minute)
		if err != nil {
			t.Fatal(err)
		}

		resp, body := doRequest(ts, "GET", "/api/v1/agents/123", expiredToken, nil, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 for expired token, got %d; body: %s", resp.StatusCode, string(body))
		}
	})
}

// 3. Invalid issuer and audience
func TestGateway_InvalidIssuerAndAudience(t *testing.T) {
	mockReg := &mockRegistryHandler{}
	gw, _ := createTestGateway(t, mockReg, nil, nil)
	ts := httptest.NewServer(gw)
	defer ts.Close()

	id := auth.Identity{
		UserID:   "user-1",
		TenantID: uuid.New(),
		Role:     auth.RoleCompanyAdmin,
	}

	t.Run("wrong issuer rejected", func(t *testing.T) {
		wrongIssCfg := getTestConfig()
		wrongIssCfg.Issuer = "fraudulent-issuer"
		tok, _ := auth.GenerateTestToken(id, wrongIssCfg, time.Hour)

		resp, _ := doRequest(ts, "GET", "/api/v1/agents/123", tok, nil, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 for wrong issuer, got %d", resp.StatusCode)
		}
	})

	t.Run("wrong audience rejected", func(t *testing.T) {
		wrongAudCfg := getTestConfig()
		wrongAudCfg.Audience = "external-api"
		tok, _ := auth.GenerateTestToken(id, wrongAudCfg, time.Hour)

		resp, _ := doRequest(ts, "GET", "/api/v1/agents/123", tok, nil, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 for wrong audience, got %d", resp.StatusCode)
		}
	})
}

// 4. Valid claims producing correct trusted identity
func TestGateway_ValidClaimsTrustedIdentity(t *testing.T) {
	mockReg := &mockRegistryHandler{}
	gw, _ := createTestGateway(t, mockReg, nil, nil)
	ts := httptest.NewServer(gw)
	defer ts.Close()

	tenantID := uuid.New()
	id := auth.Identity{
		UserID:   "alice-developer",
		TenantID: tenantID,
		Role:     auth.RoleCompanyAdmin,
	}

	tok, err := auth.GenerateTestToken(id, getTestConfig(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	resp, body := doRequest(ts, "GET", "/api/v1/agents/123", tok, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", resp.StatusCode, string(body))
	}

	if mockReg.lastSeenTenantID != tenantID {
		t.Errorf("expected downstream to receive tenant %s, got %s", tenantID, mockReg.lastSeenTenantID)
	}
}

// 5. Unknown roles and forbidden actions
func TestGateway_AuthorizationRoles(t *testing.T) {
	mockReg := &mockRegistryHandler{}
	gw, _ := createTestGateway(t, mockReg, nil, nil)
	ts := httptest.NewServer(gw)
	defer ts.Close()

	tenantID := uuid.New()

	t.Run("unknown role rejected at auth", func(t *testing.T) {
		id := auth.Identity{
			UserID:   "bob",
			TenantID: tenantID,
			Role:     "hacker_role",
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)
		resp, _ := doRequest(ts, "POST", "/api/v1/agents", tok, nil, map[string]string{"name": "test"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 for unknown role, got %d", resp.StatusCode)
		}
	})

	t.Run("agent_developer cannot publish version (requires reviewer/admin)", func(t *testing.T) {
		id := auth.Identity{
			UserID:   "dev-1",
			TenantID: tenantID,
			Role:     auth.RoleAgentDeveloper,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		resp, body := doRequest(ts, "POST", fmt.Sprintf("/api/v1/agents/%s/versions/%s/publish", uuid.New(), uuid.New()), tok, nil, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for developer publishing, got %d", resp.StatusCode)
		}
		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "FORBIDDEN" {
			t.Errorf("expected FORBIDDEN code, got %s", errResp.Error.Code)
		}
	})

	t.Run("reviewer cannot create agent", func(t *testing.T) {
		id := auth.Identity{
			UserID:   "rev-1",
			TenantID: tenantID,
			Role:     auth.RoleReviewer,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		resp, body := doRequest(ts, "POST", "/api/v1/agents", tok, nil, service.CreateAgentRequest{Name: "Forbidden Agent"})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for reviewer creating agent, got %d", resp.StatusCode)
		}
		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "FORBIDDEN" {
			t.Errorf("expected FORBIDDEN code, got %s", errResp.Error.Code)
		}
	})

	t.Run("reviewer can publish version", func(t *testing.T) {
		id := auth.Identity{
			UserID:   "rev-2",
			TenantID: tenantID,
			Role:     auth.RoleReviewer,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		resp, _ := doRequest(ts, "POST", fmt.Sprintf("/api/v1/agents/%s/versions/%s/publish", uuid.New(), uuid.New()), tok, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK for reviewer publishing, got %d", resp.StatusCode)
		}
	})

	t.Run("company_admin can publish version", func(t *testing.T) {
		id := auth.Identity{
			UserID:   "admin-1",
			TenantID: tenantID,
			Role:     auth.RoleCompanyAdmin,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		resp, _ := doRequest(ts, "POST", fmt.Sprintf("/api/v1/agents/%s/versions/%s/publish", uuid.New(), uuid.New()), tok, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK for admin publishing, got %d", resp.StatusCode)
		}
	})
}

// 6. Attempted tenant spoofing through client headers or request bodies
func TestGateway_TenantSpoofingProtection(t *testing.T) {
	mockReg := &mockRegistryHandler{}
	gw, _ := createTestGateway(t, mockReg, nil, nil)
	ts := httptest.NewServer(gw)
	defer ts.Close()

	legitTenantID := uuid.New()
	spoofedTenantID := uuid.New()

	id := auth.Identity{
		UserID:   "attacker",
		TenantID: legitTenantID,
		Role:     auth.RoleCompanyAdmin,
	}
	tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

	// Attacker sends X-Tenant-ID header with spoofed tenant ID and request body with spoofed tenant ID
	body := map[string]any{
		"name":      "Injected Agent",
		"tenant_id": spoofedTenantID.String(),
	}
	extraHeaders := map[string]string{
		"X-Tenant-ID": spoofedTenantID.String(),
	}

	resp, _ := doRequest(ts, "POST", "/api/v1/agents", tok, extraHeaders, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Downstream registry MUST receive legitTenantID, NOT spoofedTenantID
	if mockReg.lastSeenTenantID != legitTenantID {
		t.Errorf("spoofing succeeded! Downstream saw %s, expected %s", mockReg.lastSeenTenantID, legitTenantID)
	}
	if mockReg.lastSeenTenantID == spoofedTenantID {
		t.Errorf("CRITICAL SECURITY FLAW: Gateway propagated client spoofed tenant ID!")
	}
}

// 7. Rate-limit enforcement
func TestGateway_RateLimiting(t *testing.T) {
	mockReg := &mockRegistryHandler{}
	rateCfg := &ratelimit.Config{
		Enabled: true,
		RPS:     1.0,
		Burst:   2,
	}
	gw, _ := createTestGateway(t, mockReg, nil, rateCfg)
	ts := httptest.NewServer(gw)
	defer ts.Close()

	id := auth.Identity{
		UserID:   "speedy",
		TenantID: uuid.New(),
		Role:     auth.RoleCompanyAdmin,
	}
	tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

	// 1st request -> Allowed
	resp1, _ := doRequest(ts, "GET", "/api/v1/agents/1", tok, nil, nil)
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("request 1 failed with status %d", resp1.StatusCode)
	}

	// 2nd request -> Allowed (burst = 2)
	resp2, _ := doRequest(ts, "GET", "/api/v1/agents/1", tok, nil, nil)
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("request 2 failed with status %d", resp2.StatusCode)
	}

	// 3rd rapid request -> Must be rate limited
	resp3, body3 := doRequest(ts, "GET", "/api/v1/agents/1", tok, nil, nil)
	if resp3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got %d", resp3.StatusCode)
	}

	if retryAfter := resp3.Header.Get("Retry-After"); retryAfter == "" {
		t.Errorf("expected Retry-After header on 429 response")
	}

	errResp := parseErrorResponse(t, body3)
	if errResp.Error.Code != "RATE_LIMIT_EXCEEDED" {
		t.Errorf("expected RATE_LIMIT_EXCEEDED code, got %s", errResp.Error.Code)
	}
}

// 8. Upstream failures and consistent error responses
func TestGateway_UpstreamFailures(t *testing.T) {
	tenantID := uuid.New()
	id := auth.Identity{
		UserID:   "evaluator",
		TenantID: tenantID,
		Role:     auth.RoleAgentDeveloper,
	}
	tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

	gatePayload := map[string]any{
		"agent_id":              uuid.New().String(),
		"case_description":      "Evaluate high-risk refund request",
		"proposed_decision":     "APPROVE_REFUND",
		"reliability_threshold": 0.85,
	}

	t.Run("upstream unconfigured returns 503", func(t *testing.T) {
		gw, _ := createTestGateway(t, nil, nil, nil)
		ts := httptest.NewServer(gw)
		defer ts.Close()

		resp, body := doRequest(ts, "POST", "/api/v1/reliability/gate", tok, nil, gatePayload)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 Service Unavailable, got %d; body: %s", resp.StatusCode, string(body))
		}
		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "UPSTREAM_UNAVAILABLE" {
			t.Errorf("expected UPSTREAM_UNAVAILABLE, got %s", errResp.Error.Code)
		}
	})

	t.Run("upstream gRPC failure returns 502", func(t *testing.T) {
		mockRel := &mockReliabilityService{
			gateFunc: func(ctx context.Context, req *ardav1.GateRequest) (*ardav1.GateResponse, error) {
				return nil, errors.New("upstream gRPC connection reset by peer")
			},
		}
		gw, _ := createTestGateway(t, nil, mockRel, nil)
		ts := httptest.NewServer(gw)
		defer ts.Close()

		resp, body := doRequest(ts, "POST", "/api/v1/reliability/gate", tok, nil, gatePayload)
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("expected 502 Bad Gateway, got %d; body: %s", resp.StatusCode, string(body))
		}
		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "UPSTREAM_ERROR" {
			t.Errorf("expected UPSTREAM_ERROR, got %s", errResp.Error.Code)
		}
	})

	t.Run("upstream success returns 200 with gate evaluation", func(t *testing.T) {
		mockRel := &mockReliabilityService{
			gateFunc: func(ctx context.Context, req *ardav1.GateRequest) (*ardav1.GateResponse, error) {
				// Verify tenant ID was forwarded from JWT claims
				if req.TenantId != tenantID.String() {
					return nil, fmt.Errorf("wrong tenant_id: %s", req.TenantId)
				}
				return &ardav1.GateResponse{
					Confidence: 0.92,
					Outcome:    ardav1.GateOutcome_GATE_OUTCOME_PASS,
					ReasonCode: "EVIDENCE_CONFIRMED",
					Neighbors: []*ardav1.ReferenceNeighbor{
						{ReferenceExampleId: "ex-1", Label: "REFUND_OK", Similarity: 0.96},
					},
				}, nil
			},
		}
		gw, _ := createTestGateway(t, nil, mockRel, nil)
		ts := httptest.NewServer(gw)
		defer ts.Close()

		resp, body := doRequest(ts, "POST", "/api/v1/reliability/gate", tok, nil, gatePayload)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d; body: %s", resp.StatusCode, string(body))
		}

		var gateResp struct {
			Confidence float64 `json:"confidence"`
			Outcome    string  `json:"outcome"`
			ReasonCode string  `json:"reason_code"`
		}
		if err := json.Unmarshal(body, &gateResp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if gateResp.Outcome != "GATE_OUTCOME_PASS" || gateResp.Confidence != 0.92 {
			t.Errorf("unexpected gate evaluation: %+v", gateResp)
		}
	})
}

// 9. Gateway-to-Registry in-process tenant context propagation
func TestGateway_RegistryContextTenantResolver(t *testing.T) {
	legitTenantID := uuid.New()
	spoofedTenantID := uuid.New()

	// Handler wrapped with ContextTenantResolver (as mounted by Gateway)
	var capturedTenantID uuid.UUID
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := registryhttp.TenantIDFromContext(r.Context()); ok {
			capturedTenantID = id
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	})

	resolver := registryhttp.ContextTenantResolver{}
	regHandler := registryhttp.TenantMiddleware(resolver, dummyHandler)

	gw, _ := createTestGateway(t, regHandler, nil, nil)
	ts := httptest.NewServer(gw)
	defer ts.Close()

	id := auth.Identity{
		UserID:   "developer-1",
		TenantID: legitTenantID,
		Role:     auth.RoleAgentDeveloper,
	}
	tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

	t.Run("request succeeds without X-Tenant-ID header", func(t *testing.T) {
		capturedTenantID = uuid.Nil
		resp, body := doRequest(ts, "POST", "/api/v1/agents", tok, nil, map[string]any{"name": "Agent A"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d (body: %s)", resp.StatusCode, string(body))
		}
		if capturedTenantID != legitTenantID {
			t.Fatalf("expected downstream to receive %s, got %s", legitTenantID, capturedTenantID)
		}
	})

	t.Run("forged X-Tenant-ID header cannot override trusted context tenant", func(t *testing.T) {
		capturedTenantID = uuid.Nil
		headers := map[string]string{
			"X-Tenant-ID": spoofedTenantID.String(),
		}
		resp, body := doRequest(ts, "POST", "/api/v1/agents", tok, headers, map[string]any{"name": "Agent A"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d (body: %s)", resp.StatusCode, string(body))
		}
		if capturedTenantID != legitTenantID {
			t.Fatalf("spoofing leak: downstream received %s instead of %s", capturedTenantID, legitTenantID)
		}
		if capturedTenantID == spoofedTenantID {
			t.Fatalf("CRITICAL SECURITY FLAW: downstream accepted spoofed header!")
		}
	})
}

// 10. Gateway tenant-scoped agent version lookup endpoint GET /api/v1/agents/{id}/versions/{versionId}
func TestGateway_GetAgentVersionEndpoint(t *testing.T) {
	mockReg := &mockRegistryHandler{}
	gw, _ := createTestGateway(t, mockReg, nil, nil)
	ts := httptest.NewServer(gw)
	defer ts.Close()

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()
	path := fmt.Sprintf("/api/v1/agents/%s/versions/%s", agentID, versionID)

	t.Run("unauthenticated request returns 401 UNAUTHENTICATED", func(t *testing.T) {
		resp, body := doRequest(ts, "GET", path, "", nil, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized, got %d", resp.StatusCode)
		}
		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "UNAUTHENTICATED" {
			t.Errorf("expected UNAUTHENTICATED error code, got %s", errResp.Error.Code)
		}
	})

	roles := []struct {
		name string
		role auth.Role
	}{
		{"super_admin", auth.RoleSuperAdmin},
		{"company_admin", auth.RoleCompanyAdmin},
		{"agent_developer", auth.RoleAgentDeveloper},
		{"reviewer", auth.RoleReviewer},
	}

	for _, tc := range roles {
		t.Run(fmt.Sprintf("role %s is authorized to read agent version", tc.name), func(t *testing.T) {
			mockReg.lastSeenTenantID = uuid.Nil
			id := auth.Identity{
				UserID:   "user-" + tc.name,
				TenantID: tenantID,
				Role:     tc.role,
			}
			tok, err := auth.GenerateTestToken(id, getTestConfig(), time.Hour)
			if err != nil {
				t.Fatalf("failed to generate token: %v", err)
			}

			resp, body := doRequest(ts, "GET", path, tok, nil, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 OK for %s, got %d (body: %s)", tc.name, resp.StatusCode, string(body))
			}

			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("failed to parse json response: %v", err)
			}
			if payload["status"] != "handled" || payload["tenant_id"] != tenantID.String() {
				t.Errorf("expected response handled with tenant %s, got %+v", tenantID, payload)
			}
			if mockReg.lastSeenTenantID != tenantID {
				t.Errorf("expected downstream to receive tenant %s, got %s", tenantID, mockReg.lastSeenTenantID)
			}
		})
	}

	t.Run("spoofed X-Tenant-ID header is ignored and trusted JWT tenant is preserved", func(t *testing.T) {
		mockReg.lastSeenTenantID = uuid.Nil
		spoofedID := uuid.New()
		id := auth.Identity{
			UserID:   "dev-1",
			TenantID: tenantID,
			Role:     auth.RoleAgentDeveloper,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		headers := map[string]string{
			"X-Tenant-ID": spoofedID.String(),
		}
		resp, body := doRequest(ts, "GET", path, tok, headers, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d (body: %s)", resp.StatusCode, string(body))
		}
		if mockReg.lastSeenTenantID != tenantID {
			t.Fatalf("expected downstream to receive trusted tenant %s, got %s", tenantID, mockReg.lastSeenTenantID)
		}
		if mockReg.lastSeenTenantID == spoofedID {
			t.Fatalf("CRITICAL SECURITY FLAW: downstream accepted spoofed X-Tenant-ID!")
		}
	})
}

// Mock WorkflowRun implementation for unit tests
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

// Mock WorkflowDispatcher for unit tests
type mockTemporalWorkflowDispatcher struct {
	startFn   func(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error)
	lastWfID  string
	lastInput temporal.AgentExecutionInput
}

func (m *mockTemporalWorkflowDispatcher) StartAgentExecutionWorkflow(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error) {
	m.lastWfID = workflowID
	m.lastInput = input
	if m.startFn != nil {
		return m.startFn(ctx, workflowID, input)
	}
	return &mockWorkflowRun{workflowID: workflowID, runID: "run-test-123"}, nil
}

// Mock RegistryService for unit tests
type mockGatewayRegistryService struct {
	getLatestPublishedVersionFn func(ctx context.Context, tenantID, agentID uuid.UUID) (*domain.AgentVersion, error)
	lastSeenTenantID            uuid.UUID
	lastSeenAgentID             uuid.UUID
}

func (m *mockGatewayRegistryService) GetLatestPublishedVersion(ctx context.Context, tenantID, agentID uuid.UUID) (*domain.AgentVersion, error) {
	m.lastSeenTenantID = tenantID
	m.lastSeenAgentID = agentID
	if m.getLatestPublishedVersionFn != nil {
		return m.getLatestPublishedVersionFn(ctx, tenantID, agentID)
	}
	return nil, domain.NewNotFoundError("agent not found")
}

func createExecutionTestGateway(t *testing.T, regSvc gatewayhttp.RegistryService, tempClient gatewayhttp.WorkflowDispatcher) http.Handler {
	val, err := auth.NewTokenValidator(getTestConfig())
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}
	limiter := ratelimit.NewLimiter(ratelimit.Config{Enabled: false})
	return gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
		JWTValidator:    val,
		RateLimiter:     limiter,
		RegistryService: regSvc,
		TemporalClient:  tempClient,
	})
}

// 11. Gateway tenant-scoped agent execution endpoint POST /api/v1/agents/{id}/execute
func TestGateway_AgentExecutionEndpoint(t *testing.T) {
	tenantID := uuid.New()
	agentID := uuid.New()
	publishedVersionID := uuid.New()
	path := fmt.Sprintf("/api/v1/agents/%s/execute", agentID)

	publishedVer := &domain.AgentVersion{
		ID:            publishedVersionID,
		AgentID:       agentID,
		VersionNumber: 1,
		Published:     true,
	}

	t.Run("1. unauthenticated execution is rejected with 401 UNAUTHENTICATED", func(t *testing.T) {
		gw := createExecutionTestGateway(t, &mockGatewayRegistryService{}, &mockTemporalWorkflowDispatcher{})
		ts := httptest.NewServer(gw)
		defer ts.Close()

		resp, body := doRequest(ts, "POST", path, "", nil, map[string]any{"input": map[string]any{"query": "hi"}})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized, got %d", resp.StatusCode)
		}
		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "UNAUTHENTICATED" {
			t.Errorf("expected UNAUTHENTICATED error code, got %s", errResp.Error.Code)
		}
	})

	t.Run("2. role without agent:execute (reviewer) is denied with 403 FORBIDDEN", func(t *testing.T) {
		gw := createExecutionTestGateway(t, &mockGatewayRegistryService{}, &mockTemporalWorkflowDispatcher{})
		ts := httptest.NewServer(gw)
		defer ts.Close()

		id := auth.Identity{
			UserID:   "rev-1",
			TenantID: tenantID,
			Role:     auth.RoleReviewer,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		resp, body := doRequest(ts, "POST", path, tok, nil, map[string]any{"input": map[string]any{"query": "hi"}})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for reviewer executing agent, got %d", resp.StatusCode)
		}
		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "FORBIDDEN" {
			t.Errorf("expected FORBIDDEN error code, got %s", errResp.Error.Code)
		}
	})

	t.Run("3. authorized roles (super_admin, company_admin, agent_developer) are permitted", func(t *testing.T) {
		allowedRoles := []struct {
			name string
			role auth.Role
		}{
			{"super_admin", auth.RoleSuperAdmin},
			{"company_admin", auth.RoleCompanyAdmin},
			{"agent_developer", auth.RoleAgentDeveloper},
		}

		for _, tc := range allowedRoles {
			t.Run(tc.name, func(t *testing.T) {
				regMock := &mockGatewayRegistryService{
					getLatestPublishedVersionFn: func(ctx context.Context, tID, aID uuid.UUID) (*domain.AgentVersion, error) {
						return publishedVer, nil
					},
				}
				tempMock := &mockTemporalWorkflowDispatcher{}
				gw := createExecutionTestGateway(t, regMock, tempMock)
				ts := httptest.NewServer(gw)
				defer ts.Close()

				id := auth.Identity{
					UserID:   "user-" + tc.name,
					TenantID: tenantID,
					Role:     tc.role,
				}
				tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

				resp, body := doRequest(ts, "POST", path, tok, nil, map[string]any{"input": map[string]any{"query": "run"}})
				if resp.StatusCode != http.StatusAccepted {
					t.Fatalf("expected 202 Accepted for %s, got %d (body: %s)", tc.name, resp.StatusCode, string(body))
				}
			})
		}
	})

	t.Run("4. authorized request resolves agent using JWT tenant", func(t *testing.T) {
		regMock := &mockGatewayRegistryService{
			getLatestPublishedVersionFn: func(ctx context.Context, tID, aID uuid.UUID) (*domain.AgentVersion, error) {
				return publishedVer, nil
			},
		}
		tempMock := &mockTemporalWorkflowDispatcher{}
		gw := createExecutionTestGateway(t, regMock, tempMock)
		ts := httptest.NewServer(gw)
		defer ts.Close()

		id := auth.Identity{
			UserID:   "dev-1",
			TenantID: tenantID,
			Role:     auth.RoleAgentDeveloper,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		resp, body := doRequest(ts, "POST", path, tok, nil, nil)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted, got %d (body: %s)", resp.StatusCode, string(body))
		}

		if regMock.lastSeenTenantID != tenantID {
			t.Errorf("expected registry to receive tenant %s, got %s", tenantID, regMock.lastSeenTenantID)
		}
		if regMock.lastSeenAgentID != agentID {
			t.Errorf("expected registry to receive agent %s, got %s", agentID, regMock.lastSeenAgentID)
		}
	})

	t.Run("5. forged X-Tenant-ID header cannot override JWT tenant", func(t *testing.T) {
		regMock := &mockGatewayRegistryService{
			getLatestPublishedVersionFn: func(ctx context.Context, tID, aID uuid.UUID) (*domain.AgentVersion, error) {
				return publishedVer, nil
			},
		}
		tempMock := &mockTemporalWorkflowDispatcher{}
		gw := createExecutionTestGateway(t, regMock, tempMock)
		ts := httptest.NewServer(gw)
		defer ts.Close()

		spoofedTenantID := uuid.New()
		id := auth.Identity{
			UserID:   "dev-1",
			TenantID: tenantID,
			Role:     auth.RoleAgentDeveloper,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		headers := map[string]string{
			"X-Tenant-ID": spoofedTenantID.String(),
		}
		resp, body := doRequest(ts, "POST", path, tok, headers, nil)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted, got %d (body: %s)", resp.StatusCode, string(body))
		}

		if regMock.lastSeenTenantID != tenantID {
			t.Fatalf("spoofing leak: registry received %s instead of trusted %s", regMock.lastSeenTenantID, tenantID)
		}
		if regMock.lastSeenTenantID == spoofedTenantID {
			t.Fatalf("CRITICAL SECURITY FLAW: registry received spoofed tenant header!")
		}
	})

	t.Run("6. missing agent returns 404 NOT_FOUND", func(t *testing.T) {
		regMock := &mockGatewayRegistryService{
			getLatestPublishedVersionFn: func(ctx context.Context, tID, aID uuid.UUID) (*domain.AgentVersion, error) {
				return nil, domain.NewNotFoundError("agent not found")
			},
		}
		tempMock := &mockTemporalWorkflowDispatcher{}
		gw := createExecutionTestGateway(t, regMock, tempMock)
		ts := httptest.NewServer(gw)
		defer ts.Close()

		id := auth.Identity{
			UserID:   "dev-1",
			TenantID: tenantID,
			Role:     auth.RoleAgentDeveloper,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		resp, body := doRequest(ts, "POST", path, tok, nil, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found for missing agent, got %d", resp.StatusCode)
		}
		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "NOT_FOUND" {
			t.Errorf("expected NOT_FOUND code, got %s", errResp.Error.Code)
		}
	})

	t.Run("7. agent without published version is rejected with 404 NOT_FOUND", func(t *testing.T) {
		regMock := &mockGatewayRegistryService{
			getLatestPublishedVersionFn: func(ctx context.Context, tID, aID uuid.UUID) (*domain.AgentVersion, error) {
				return nil, domain.NewNotFoundError("no published version found for agent")
			},
		}
		tempMock := &mockTemporalWorkflowDispatcher{}
		gw := createExecutionTestGateway(t, regMock, tempMock)
		ts := httptest.NewServer(gw)
		defer ts.Close()

		id := auth.Identity{
			UserID:   "dev-1",
			TenantID: tenantID,
			Role:     auth.RoleAgentDeveloper,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		resp, body := doRequest(ts, "POST", path, tok, nil, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found for agent with no published versions, got %d", resp.StatusCode)
		}
		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "NOT_FOUND" {
			t.Errorf("expected NOT_FOUND code, got %s", errResp.Error.Code)
		}
	})

	t.Run("8. workflow receives exact pinned version ID and execution input", func(t *testing.T) {
		regMock := &mockGatewayRegistryService{
			getLatestPublishedVersionFn: func(ctx context.Context, tID, aID uuid.UUID) (*domain.AgentVersion, error) {
				return publishedVer, nil
			},
		}
		tempMock := &mockTemporalWorkflowDispatcher{}
		gw := createExecutionTestGateway(t, regMock, tempMock)
		ts := httptest.NewServer(gw)
		defer ts.Close()

		id := auth.Identity{
			UserID:   "dev-1",
			TenantID: tenantID,
			Role:     auth.RoleAgentDeveloper,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		execInput := map[string]any{
			"input": map[string]any{
				"prompt":    "analyze portfolio risk",
				"threshold": 0.95,
			},
		}
		resp, body := doRequest(ts, "POST", path, tok, nil, execInput)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted, got %d (body: %s)", resp.StatusCode, string(body))
		}

		if tempMock.lastInput.TenantID != tenantID {
			t.Errorf("expected workflow tenant %s, got %s", tenantID, tempMock.lastInput.TenantID)
		}
		if tempMock.lastInput.AgentID != agentID {
			t.Errorf("expected workflow agent %s, got %s", agentID, tempMock.lastInput.AgentID)
		}
		if tempMock.lastInput.AgentVersionID != publishedVersionID {
			t.Errorf("expected workflow pinned version %s, got %s", publishedVersionID, tempMock.lastInput.AgentVersionID)
		}
		if tempMock.lastInput.ExecutionInput["prompt"] != "analyze portfolio risk" {
			t.Errorf("expected workflow execution input to contain prompt, got %+v", tempMock.lastInput.ExecutionInput)
		}
	})

	t.Run("9. temporal dispatch failures produce 502 UPSTREAM_ERROR, not false success", func(t *testing.T) {
		regMock := &mockGatewayRegistryService{
			getLatestPublishedVersionFn: func(ctx context.Context, tID, aID uuid.UUID) (*domain.AgentVersion, error) {
				return publishedVer, nil
			},
		}
		tempMock := &mockTemporalWorkflowDispatcher{
			startFn: func(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error) {
				return nil, errors.New("temporal server connection refused")
			},
		}
		gw := createExecutionTestGateway(t, regMock, tempMock)
		ts := httptest.NewServer(gw)
		defer ts.Close()

		id := auth.Identity{
			UserID:   "dev-1",
			TenantID: tenantID,
			Role:     auth.RoleAgentDeveloper,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		resp, body := doRequest(ts, "POST", path, tok, nil, nil)
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("expected 502 Bad Gateway for dispatch failure, got %d (body: %s)", resp.StatusCode, string(body))
		}
		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "UPSTREAM_ERROR" {
			t.Errorf("expected UPSTREAM_ERROR code, got %s", errResp.Error.Code)
		}
	})

	t.Run("10. successful dispatch returns actual workflow and run IDs", func(t *testing.T) {
		expectedWfID := "agent-exec-custom-12345"
		expectedRunID := "run-actual-98765"
		regMock := &mockGatewayRegistryService{
			getLatestPublishedVersionFn: func(ctx context.Context, tID, aID uuid.UUID) (*domain.AgentVersion, error) {
				return publishedVer, nil
			},
		}
		tempMock := &mockTemporalWorkflowDispatcher{
			startFn: func(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error) {
				return &mockWorkflowRun{workflowID: expectedWfID, runID: expectedRunID}, nil
			},
		}
		gw := createExecutionTestGateway(t, regMock, tempMock)
		ts := httptest.NewServer(gw)
		defer ts.Close()

		id := auth.Identity{
			UserID:   "dev-1",
			TenantID: tenantID,
			Role:     auth.RoleAgentDeveloper,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		resp, body := doRequest(ts, "POST", path, tok, nil, nil)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted, got %d (body: %s)", resp.StatusCode, string(body))
		}

		var execResp gatewayhttp.ExecuteAgentResponse
		if err := json.Unmarshal(body, &execResp); err != nil {
			t.Fatalf("failed to decode execution response: %v", err)
		}

		if execResp.WorkflowID != expectedWfID {
			t.Errorf("expected workflow ID %s, got %s", expectedWfID, execResp.WorkflowID)
		}
		if execResp.RunID != expectedRunID {
			t.Errorf("expected run ID %s, got %s", expectedRunID, execResp.RunID)
		}
		if execResp.AgentID != agentID {
			t.Errorf("expected agent ID %s, got %s", agentID, execResp.AgentID)
		}
		if execResp.AgentVersionID != publishedVersionID {
			t.Errorf("expected version ID %s, got %s", publishedVersionID, execResp.AgentVersionID)
		}
		if execResp.Status != "ACCEPTED" {
			t.Errorf("expected status ACCEPTED, got %s", execResp.Status)
		}
	})

	t.Run("11. invalid agent UUID returns 400 VALIDATION_FAILED", func(t *testing.T) {
		gw := createExecutionTestGateway(t, &mockGatewayRegistryService{}, &mockTemporalWorkflowDispatcher{})
		ts := httptest.NewServer(gw)
		defer ts.Close()

		id := auth.Identity{
			UserID:   "dev-1",
			TenantID: tenantID,
			Role:     auth.RoleAgentDeveloper,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		resp, body := doRequest(ts, "POST", "/api/v1/agents/not-a-valid-uuid/execute", tok, nil, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d", resp.StatusCode)
		}
		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "VALIDATION_FAILED" {
			t.Errorf("expected VALIDATION_FAILED code, got %s", errResp.Error.Code)
		}
	})

	t.Run("12. invalid JSON body returns 400 VALIDATION_FAILED", func(t *testing.T) {
		gw := createExecutionTestGateway(t, &mockGatewayRegistryService{}, &mockTemporalWorkflowDispatcher{})
		ts := httptest.NewServer(gw)
		defer ts.Close()

		id := auth.Identity{
			UserID:   "dev-1",
			TenantID: tenantID,
			Role:     auth.RoleAgentDeveloper,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		req, _ := http.NewRequest("POST", ts.URL+path, bytes.NewReader([]byte("{invalid-json-body")))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for bad JSON, got %d", resp.StatusCode)
		}
	})

	t.Run("13. temporal client unconfigured returns 503 UPSTREAM_UNAVAILABLE", func(t *testing.T) {
		regMock := &mockGatewayRegistryService{
			getLatestPublishedVersionFn: func(ctx context.Context, tID, aID uuid.UUID) (*domain.AgentVersion, error) {
				return publishedVer, nil
			},
		}
		// Pass nil TemporalClient
		gw := createExecutionTestGateway(t, regMock, nil)
		ts := httptest.NewServer(gw)
		defer ts.Close()

		id := auth.Identity{
			UserID:   "dev-1",
			TenantID: tenantID,
			Role:     auth.RoleAgentDeveloper,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		resp, body := doRequest(ts, "POST", path, tok, nil, nil)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 Service Unavailable, got %d (body: %s)", resp.StatusCode, string(body))
		}
		errResp := parseErrorResponse(t, body)
		if errResp.Error.Code != "UPSTREAM_UNAVAILABLE" {
			t.Errorf("expected UPSTREAM_UNAVAILABLE code, got %s", errResp.Error.Code)
		}
	})
}
