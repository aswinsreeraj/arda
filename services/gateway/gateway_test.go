package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/aswinsreeraj/arda/services/gateway"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/aswinsreeraj/arda/services/gateway/credentials"
	gatewayhttp "github.com/aswinsreeraj/arda/services/gateway/http"
	"github.com/aswinsreeraj/arda/services/gateway/proxy"
	"github.com/aswinsreeraj/arda/services/gateway/ratelimit"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	registrygrpc "github.com/aswinsreeraj/arda/services/registry/grpc"
	registryhttp "github.com/aswinsreeraj/arda/services/registry/http"
	"github.com/aswinsreeraj/arda/services/registry/service"
	"github.com/aswinsreeraj/arda/services/registry/store"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func init() {
	gin.SetMode(gin.TestMode)
}

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
	regRouter := gin.New()
	regRouter.Use(registryhttp.TenantMiddleware(resolver))
	regRouter.POST("/api/v1/agents", func(c *gin.Context) {
		dummyHandler(c.Writer, c.Request)
	})

	gw, _ := createTestGateway(t, regRouter, nil, nil)
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

	t.Run("14. request cancellation propagates to downstream services", func(t *testing.T) {
		ctxCancelled := make(chan bool, 1)
		regMock := &mockGatewayRegistryService{
			getLatestPublishedVersionFn: func(ctx context.Context, tID, aID uuid.UUID) (*domain.AgentVersion, error) {
				select {
				case <-ctx.Done():
					ctxCancelled <- true
					return nil, ctx.Err()
				case <-time.After(500 * time.Millisecond):
					return publishedVer, nil
				}
			},
		}
		gw := createExecutionTestGateway(t, regMock, &mockTemporalWorkflowDispatcher{})
		ts := httptest.NewServer(gw)
		defer ts.Close()

		id := auth.Identity{
			UserID:   "dev-1",
			TenantID: tenantID,
			Role:     auth.RoleAgentDeveloper,
		}
		tok, _ := auth.GenerateTestToken(id, getTestConfig(), time.Hour)

		reqCtx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(reqCtx, "POST", ts.URL+path, bytes.NewReader([]byte("{}")))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")

		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()

		_, _ = http.DefaultClient.Do(req)

		select {
		case wasCancelled := <-ctxCancelled:
			if !wasCancelled {
				t.Fatalf("expected downstream context to be cancelled")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for context cancellation in downstream service")
		}
	})
}

func TestGatewayServer_Lifecycle(t *testing.T) {
	cfg := gateway.Config{
		Port:             "0",
		JWTSecret:        testSecret,
		JWTIssuer:        testIssuer,
		JWTAudience:      testAudience,
		RateLimitEnabled: false,
	}

	server, err := gateway.NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create gateway server: %v", err)
	}

	if server.Handler == nil {
		t.Fatalf("expected non-nil server handler")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	httpServer := &http.Server{
		Addr:    ln.Addr().String(),
		Handler: server.Handler,
	}

	go func() {
		_ = httpServer.Serve(ln)
	}()

	// Verify server responds on /healthz
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/healthz", ln.Addr().String()))
	if err != nil {
		t.Fatalf("failed to query /healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /healthz, got %d", resp.StatusCode)
	}

	// Verify graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("graceful shutdown failed: %v", err)
	}
}

// mockVaultSecretStore implements vault.SecretStore for Gateway credential tests.
type mockVaultSecretStore struct {
	secrets map[string]map[string]string
}

func newMockVaultSecretStore() *mockVaultSecretStore {
	return &mockVaultSecretStore{
		secrets: make(map[string]map[string]string),
	}
}

func (m *mockVaultSecretStore) makeKey(tenantID uuid.UUID, path string) string {
	return tenantID.String() + ":" + path
}

func (m *mockVaultSecretStore) WriteSecret(ctx context.Context, tenantID uuid.UUID, key string, data map[string]string) error {
	k := m.makeKey(tenantID, key)
	cp := make(map[string]string, len(data))
	for k, v := range data {
		cp[k] = v
	}
	m.secrets[k] = cp
	return nil
}

func (m *mockVaultSecretStore) ReadSecret(ctx context.Context, tenantID uuid.UUID, ref string) (map[string]string, error) {
	parsed, err := vault.ParseReference(ref)
	if err != nil {
		return nil, err
	}
	k := m.makeKey(tenantID, parsed.Key)
	data, ok := m.secrets[k]
	if !ok {
		return nil, vault.ErrSecretNotFound
	}
	return data, nil
}

func (m *mockVaultSecretStore) ReadSecretValue(ctx context.Context, tenantID uuid.UUID, ref string) (string, error) {
	parsed, err := vault.ParseReference(ref)
	if err != nil {
		return "", err
	}
	data, err := m.ReadSecret(ctx, tenantID, ref)
	if err != nil {
		return "", err
	}
	val, ok := data[parsed.Field]
	if !ok {
		return "", vault.ErrSecretNotFound
	}
	return val, nil
}

func (m *mockVaultSecretStore) DeleteSecret(ctx context.Context, tenantID uuid.UUID, key string) error {
	delete(m.secrets, m.makeKey(tenantID, key))
	return nil
}

func createGRPCTestGateway(t *testing.T, regStore store.Store, vStore vault.SecretStore, credStore credentials.Store, tempDispatcher gatewayhttp.WorkflowDispatcher) (http.Handler, *auth.TokenValidator, func()) {
	val, err := auth.NewTokenValidator(getTestConfig())
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	regSvc := service.NewRegistryService(regStore)
	grpcServer := registrygrpc.NewServer(regSvc)
	lis := bufconn.Listen(1024 * 1024)

	go func() {
		_ = grpcServer.Serve(lis)
	}()

	conn, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufnet: %v", err)
	}

	regClient := ardav1.NewAgentRegistryServiceClient(conn)

	var credHandler *credentials.Handler
	if vStore != nil && credStore != nil {
		credSvc := credentials.NewService(vStore, credStore)
		credHandler = credentials.NewHandler(credSvc, gatewayhttp.WriteErrorResponse)
	}

	gw := gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
		JWTValidator:       val,
		RateLimiter:        ratelimit.NewLimiter(ratelimit.Config{Enabled: false}),
		RegistryClient:     regClient,
		CredentialHandler:  credHandler,
		TemporalClient:     tempDispatcher,
	})

	cleanup := func() {
		_ = conn.Close()
		grpcServer.GracefulStop()
	}

	return gw, val, cleanup
}

func TestGateway_AgentRegistryOverGRPC_Lifecycle(t *testing.T) {
	regStore := store.NewMemoryStore()
	gw, val, cleanup := createGRPCTestGateway(t, regStore, nil, nil, nil)
	defer cleanup()

	ts := httptest.NewServer(gw)
	defer ts.Close()

	tenantID := uuid.New()
	devToken, _ := auth.GenerateTestToken(auth.Identity{
		UserID:   "dev-1",
		TenantID: tenantID,
		Role:     auth.RoleAgentDeveloper,
	}, getTestConfig(), time.Hour)

	revToken, _ := auth.GenerateTestToken(auth.Identity{
		UserID:   "rev-1",
		TenantID: tenantID,
		Role:     auth.RoleReviewer,
	}, getTestConfig(), time.Hour)

	_ = val

	// 1. Create Agent as Developer
	createAgentBody := map[string]any{
		"name": "financial-agent",
		"configuration": map[string]any{
			"prompt_template":       "You are a finance assistant.",
			"tools":                 []string{"calculator", "forex"},
			"reliability_threshold": 0.90,
			"execution_mode":        "supervised",
		},
	}
	resp, body := doRequest(ts, "POST", "/api/v1/agents", devToken, nil, createAgentBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created on agent creation, got %d: %s", resp.StatusCode, string(body))
	}

	var agentDetail domain.AgentDetail
	if err := json.Unmarshal(body, &agentDetail); err != nil {
		t.Fatalf("failed to unmarshal agent detail: %v", err)
	}
	agentID := agentDetail.Agent.ID
	if agentID == uuid.Nil {
		t.Fatalf("expected valid agent ID")
	}
	if agentDetail.DraftVersion == nil {
		t.Fatalf("expected initial draft version to be created with agent")
	}
	versionID := agentDetail.DraftVersion.ID

	// 2. Query Agent by ID
	resp, body = doRequest(ts, "GET", fmt.Sprintf("/api/v1/agents/%s", agentID), devToken, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on get agent, got %d: %s", resp.StatusCode, string(body))
	}

	// 3. List Agents
	resp, body = doRequest(ts, "GET", "/api/v1/agents?page_size=10", devToken, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on list agents, got %d: %s", resp.StatusCode, string(body))
	}
	var listResp struct {
		Agents     []domain.AgentDetail `json:"agents"`
		TotalCount int                  `json:"total_count"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		t.Fatalf("failed to unmarshal list agents response: %v", err)
	}
	if len(listResp.Agents) != 1 {
		t.Fatalf("expected 1 agent in list, got %d", len(listResp.Agents))
	}

	// 4. Update Draft Version
	updateBody := map[string]any{
		"prompt_template":       "Updated prompt for finance.",
		"tools":                 []string{"calculator", "forex", "charts"},
		"reliability_threshold": 0.95,
		"execution_mode":        "supervised",
	}
	resp, body = doRequest(ts, "PUT", fmt.Sprintf("/api/v1/agents/%s/versions/%s", agentID, versionID), devToken, nil, updateBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on update draft, got %d: %s", resp.StatusCode, string(body))
	}

	// 5. Developer attempts to Publish Version -> 403 Forbidden (RBAC)
	resp, _ = doRequest(ts, "POST", fmt.Sprintf("/api/v1/agents/%s/versions/%s/publish", agentID, versionID), devToken, nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when developer tries to publish, got %d", resp.StatusCode)
	}

	// 6. Reviewer Publishes Version -> 200 OK
	resp, body = doRequest(ts, "POST", fmt.Sprintf("/api/v1/agents/%s/versions/%s/publish", agentID, versionID), revToken, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK when reviewer publishes, got %d: %s", resp.StatusCode, string(body))
	}

	// 7. Immutability: Developer attempts to update published version -> 409 Conflict
	resp, _ = doRequest(ts, "PUT", fmt.Sprintf("/api/v1/agents/%s/versions/%s", agentID, versionID), devToken, nil, updateBody)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict when updating published version, got %d", resp.StatusCode)
	}

	// 8a. RBAC: Developer attempts to delete version -> 403 Forbidden
	resp, _ = doRequest(ts, "DELETE", fmt.Sprintf("/api/v1/agents/%s/versions/%s", agentID, versionID), devToken, nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when developer deletes version, got %d", resp.StatusCode)
	}

	// 8b. Immutability: Admin attempts to delete published version -> 409 Conflict
	adminToken, _ := auth.GenerateTestToken(auth.Identity{
		UserID:   "admin-1",
		TenantID: tenantID,
		Role:     auth.RoleCompanyAdmin,
	}, getTestConfig(), time.Hour)

	resp, _ = doRequest(ts, "DELETE", fmt.Sprintf("/api/v1/agents/%s/versions/%s", agentID, versionID), adminToken, nil, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict when deleting published version, got %d", resp.StatusCode)
	}

	// 9. Version Resolution Routes & Aliases
	// Canonical: /versions/latest
	resp, body = doRequest(ts, "GET", fmt.Sprintf("/api/v1/agents/%s/versions/latest", agentID), devToken, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on canonical /versions/latest, got %d: %s", resp.StatusCode, string(body))
	}
	var canonicalVer domain.AgentVersion
	_ = json.Unmarshal(body, &canonicalVer)
	if canonicalVer.ID != versionID {
		t.Fatalf("expected version ID %s from /versions/latest, got %s", versionID, canonicalVer.ID)
	}

	// Direct Alias 1: /versions/published
	resp, body = doRequest(ts, "GET", fmt.Sprintf("/api/v1/agents/%s/versions/published", agentID), devToken, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on alias /versions/published, got %d: %s", resp.StatusCode, string(body))
	}

	// Direct Alias 2: /versions/published/latest
	resp, body = doRequest(ts, "GET", fmt.Sprintf("/api/v1/agents/%s/versions/published/latest", agentID), devToken, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on alias /versions/published/latest, got %d: %s", resp.StatusCode, string(body))
	}

	// Lookup specific version ID
	resp, body = doRequest(ts, "GET", fmt.Sprintf("/api/v1/agents/%s/versions/%s", agentID, versionID), devToken, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on get version by ID, got %d: %s", resp.StatusCode, string(body))
	}

	// Invalid version ID format returns 400 Bad Request
	resp, _ = doRequest(ts, "GET", fmt.Sprintf("/api/v1/agents/%s/versions/not-a-valid-uuid", agentID), devToken, nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request on invalid version UUID, got %d", resp.StatusCode)
	}

	// 10. Cross-Tenant Denial: Tenant B cannot access Tenant A's agent
	tenantB := uuid.New()
	tenantBToken, _ := auth.GenerateTestToken(auth.Identity{
		UserID:   "dev-b",
		TenantID: tenantB,
		Role:     auth.RoleAgentDeveloper,
	}, getTestConfig(), time.Hour)

	resp, _ = doRequest(ts, "GET", fmt.Sprintf("/api/v1/agents/%s", agentID), tenantBToken, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found for cross-tenant agent access, got %d", resp.StatusCode)
	}
}

func TestGateway_CredentialManagement_Lifecycle(t *testing.T) {
	vStore := newMockVaultSecretStore()
	credStore := credentials.NewMemoryStore()
	regStore := store.NewMemoryStore()
	gw, _, cleanup := createGRPCTestGateway(t, regStore, vStore, credStore, nil)
	defer cleanup()

	ts := httptest.NewServer(gw)
	defer ts.Close()

	tenantID := uuid.New()
	devToken, _ := auth.GenerateTestToken(auth.Identity{
		UserID:   "dev-1",
		TenantID: tenantID,
		Role:     auth.RoleAgentDeveloper,
	}, getTestConfig(), time.Hour)

	revToken, _ := auth.GenerateTestToken(auth.Identity{
		UserID:   "rev-1",
		TenantID: tenantID,
		Role:     auth.RoleReviewer,
	}, getTestConfig(), time.Hour)

	// 1. Save Mock LLM Credential
	llmReq := map[string]any{
		"category":     "llm",
		"provider":     "openai",
		"secret_value": "mock-llm-key-not-real",
	}
	resp, body := doRequest(ts, "POST", "/api/v1/credentials", devToken, nil, llmReq)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created on save LLM credential, got %d: %s", resp.StatusCode, string(body))
	}

	// Verify secret_value is NEVER returned in response
	if strings.Contains(string(body), "mock-llm-key-not-real") {
		t.Fatalf("SECURITY VIOLATION: raw secret value was leaked in API response body!")
	}

	var llmCred credentials.Credential
	if err := json.Unmarshal(body, &llmCred); err != nil {
		t.Fatalf("failed to unmarshal credential: %v", err)
	}
	if llmCred.ID == uuid.Nil {
		t.Fatalf("expected non-nil credential ID")
	}
	if llmCred.Category != "llm" || llmCred.Provider != "openai" {
		t.Fatalf("unexpected category/provider: %s/%s", llmCred.Category, llmCred.Provider)
	}

	// 2. Save Mock MCP Credential
	mcpReq := map[string]any{
		"category":     "mcp",
		"provider":     "database-mcp",
		"secret_value": "mock-mcp-token-not-real",
	}
	resp, body = doRequest(ts, "POST", "/api/v1/credentials", devToken, nil, mcpReq)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created on save MCP credential, got %d: %s", resp.StatusCode, string(body))
	}
	if strings.Contains(string(body), "mock-mcp-token-not-real") {
		t.Fatalf("SECURITY VIOLATION: raw secret value was leaked in API response body!")
	}

	var mcpCred credentials.Credential
	_ = json.Unmarshal(body, &mcpCred)

	// 3. List Credentials
	resp, body = doRequest(ts, "GET", "/api/v1/credentials", devToken, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on list credentials, got %d: %s", resp.StatusCode, string(body))
	}
	var listResp struct {
		Credentials []credentials.Credential `json:"credentials"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		t.Fatalf("failed to unmarshal list credentials response: %v", err)
	}
	if len(listResp.Credentials) != 2 {
		t.Fatalf("expected 2 credentials in list, got %d", len(listResp.Credentials))
	}

	// 4. Get Credential by ID
	resp, body = doRequest(ts, "GET", fmt.Sprintf("/api/v1/credentials/%s", llmCred.ID), devToken, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on get credential by ID, got %d: %s", resp.StatusCode, string(body))
	}
	if strings.Contains(string(body), "mock-llm-key-not-real") {
		t.Fatalf("SECURITY VIOLATION: raw secret leaked in get credential response!")
	}

	// 5. Verify Credential
	resp, body = doRequest(ts, "POST", fmt.Sprintf("/api/v1/credentials/%s/verify", llmCred.ID), devToken, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on verify credential, got %d: %s", resp.StatusCode, string(body))
	}
	var verifyResp credentials.VerifyCredentialResponse
	if err := json.Unmarshal(body, &verifyResp); err != nil {
		t.Fatalf("failed to unmarshal verify response: %v", err)
	}
	if !verifyResp.Verified {
		t.Fatalf("expected credential to be verified, got false")
	}

	// 6. Reviewer is not authorized to manage credentials (RBAC check)
	resp, _ = doRequest(ts, "POST", "/api/v1/credentials", revToken, nil, llmReq)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when reviewer manages credentials, got %d", resp.StatusCode)
	}

	// 7. Cross-Tenant Denial: Tenant B cannot access or delete Tenant A's credentials
	tenantB := uuid.New()
	tenantBToken, _ := auth.GenerateTestToken(auth.Identity{
		UserID:   "dev-b",
		TenantID: tenantB,
		Role:     auth.RoleAgentDeveloper,
	}, getTestConfig(), time.Hour)

	resp, _ = doRequest(ts, "GET", fmt.Sprintf("/api/v1/credentials/%s", llmCred.ID), tenantBToken, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found when Tenant B gets Tenant A's credential, got %d", resp.StatusCode)
	}

	resp, _ = doRequest(ts, "DELETE", fmt.Sprintf("/api/v1/credentials/%s", llmCred.ID), tenantBToken, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found when Tenant B deletes Tenant A's credential, got %d", resp.StatusCode)
	}

	// 8. Delete Credential as Tenant A
	resp, _ = doRequest(ts, "DELETE", fmt.Sprintf("/api/v1/credentials/%s", llmCred.ID), devToken, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on delete credential, got %d", resp.StatusCode)
	}

	// Subsequent lookup should return 404
	resp, _ = doRequest(ts, "GET", fmt.Sprintf("/api/v1/credentials/%s", llmCred.ID), devToken, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found after deleting credential, got %d", resp.StatusCode)
	}
}

func TestGateway_AgentExecution_WithGRPCRegistry(t *testing.T) {
	regStore := store.NewMemoryStore()
	tenantID := uuid.New()
	agentID := uuid.New()
	verID := uuid.New()
	now := time.Now().UTC()

	// Seed store with an agent and published version
	_ = regStore.CreateAgent(context.Background(), &domain.Agent{
		ID:        agentID,
		TenantID:  tenantID,
		Name:      "test-agent",
		CreatedAt: now,
		UpdatedAt: now,
	})
	_ = regStore.CreateVersion(context.Background(), &domain.AgentVersion{
		ID:            verID,
		AgentID:       agentID,
		VersionNumber: 1,
		Configuration: domain.Configuration{
			PromptTemplate: "Run execution",
			Tools:          []string{},
		},
		Published:   true,
		PublishedAt: &now,
		CreatedAt:   now,
		UpdatedAt:   now,
	})

	mockTemp := &mockTemporalWorkflowDispatcher{
		startFn: func(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error) {
			if input.TenantID != tenantID {
				t.Fatalf("expected tenantID %s, got %s", tenantID, input.TenantID)
			}
			if input.AgentID != agentID {
				t.Fatalf("expected agentID %s, got %s", agentID, input.AgentID)
			}
			if input.AgentVersionID != verID {
				t.Fatalf("expected agentVersionID %s, got %s", verID, input.AgentVersionID)
			}
			return &mockWorkflowRun{
				workflowID: workflowID,
				runID:      "test-run-123",
			}, nil
		},
	}

	val, err := auth.NewTokenValidator(getTestConfig())
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	regSvc := service.NewRegistryService(regStore)
	grpcServer := registrygrpc.NewServer(regSvc)
	lis := bufconn.Listen(1024 * 1024)

	go func() {
		_ = grpcServer.Serve(lis)
	}()

	conn, _ := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	defer conn.Close()
	defer grpcServer.GracefulStop()

	regClient := ardav1.NewAgentRegistryServiceClient(conn)

	gw := gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
		JWTValidator:   val,
		RateLimiter:    ratelimit.NewLimiter(ratelimit.Config{Enabled: false}),
		RegistryClient: regClient,
		TemporalClient: mockTemp,
	})

	ts := httptest.NewServer(gw)
	defer ts.Close()

	devToken, _ := auth.GenerateTestToken(auth.Identity{
		UserID:   "dev-1",
		TenantID: tenantID,
		Role:     auth.RoleAgentDeveloper,
	}, getTestConfig(), time.Hour)

	execReq := map[string]any{
		"input": map[string]any{
			"task": "analyze_market",
		},
	}

	resp, body := doRequest(ts, "POST", fmt.Sprintf("/api/v1/agents/%s/execute", agentID), devToken, nil, execReq)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on execute, got %d: %s", resp.StatusCode, string(body))
	}

	var execResp gatewayhttp.ExecuteAgentResponse
	if err := json.Unmarshal(body, &execResp); err != nil {
		t.Fatalf("failed to unmarshal execute response: %v", err)
	}
	if execResp.Status != "ACCEPTED" {
		t.Fatalf("expected status ACCEPTED, got %s", execResp.Status)
	}
	if execResp.AgentVersionID != verID {
		t.Fatalf("expected agentVersionID %s, got %s", verID, execResp.AgentVersionID)
	}
}
