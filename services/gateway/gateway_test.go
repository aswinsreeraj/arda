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
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	gatewayhttp "github.com/aswinsreeraj/arda/services/gateway/http"
	"github.com/aswinsreeraj/arda/services/gateway/proxy"
	"github.com/aswinsreeraj/arda/services/gateway/ratelimit"
	registryhttp "github.com/aswinsreeraj/arda/services/registry/http"
	"github.com/aswinsreeraj/arda/services/registry/service"
	"github.com/google/uuid"
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
