package gateway_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/platformconfig"
	"github.com/aswinsreeraj/arda/services/gateway"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlatformConfigAPI_RBACPersistenceAndActivity(t *testing.T) {
	secret := []byte("super-secure-platform-config-test-secret-32b")
	srv, err := gateway.NewServer(gateway.Config{
		JWTSecret:        secret,
		JWTIssuer:        "arda-auth",
		JWTAudience:      "arda-api",
		RateLimitEnabled: true,
		RateLimitRPS:     20,
		RateLimitBurst:   40,
	}, nil)
	require.NoError(t, err)
	defer srv.Close()

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	superToken := issueUserToken(t, srv.JWTValidator, uuid.New(), auth.RoleSuperAdmin)

	// Register a real active tenant + company_admin via public registration endpoint
	regBody := []byte(`{"tenant_name":"Test Org","email":"owner@testorg.example","password":"StrongPassword!123","display_name":"Test Owner"}`)
	regReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/tenants/register", bytes.NewReader(regBody))
	regReq.Header.Set("Content-Type", "application/json")
	regResp, err := http.DefaultClient.Do(regReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, regResp.StatusCode)
	var regData struct {
		AccessToken string `json:"access_token"`
		Tenant      struct {
			ID uuid.UUID `json:"id"`
		} `json:"tenant"`
	}
	require.NoError(t, json.NewDecoder(regResp.Body).Decode(&regData))
	_ = regResp.Body.Close()

	adminToken := regData.AccessToken
	if adminToken == "" {
		adminToken = issueUserToken(t, srv.JWTValidator, regData.Tenant.ID, auth.RoleCompanyAdmin)
	}

	// 1. Company admin must be forbidden from GET /api/v1/platform/config
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/platform/config", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	_ = resp.Body.Close()

	// 2. Super admin can read defaults
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/platform/config", nil)
	req.Header.Set("Authorization", "Bearer "+superToken)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var listRes struct {
		Configs  []platformconfig.Item   `json:"configs"`
		Snapshot platformconfig.Snapshot `json:"snapshot"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&listRes))
	_ = resp.Body.Close()
	assert.Len(t, listRes.Configs, 5)
	assert.Equal(t, 1200, listRes.Snapshot.RateLimitPerMinute)

	// 3. Invalid value is rejected with 400
	badBody := []byte(`{"updates":{"reliability.default_threshold":"1.75"}}`)
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/api/v1/platform/config", bytes.NewReader(badBody))
	req.Header.Set("Authorization", "Bearer "+superToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	_ = resp.Body.Close()

	// 4. Valid update persists all 5 settings and creates real audit logs
	goodBody := []byte(`{
		"configs": [
			{"key": "gateway.rate_limit.default", "value": "600"},
			{"key": "calibration.sampling_interval", "value": "3"},
			{"key": "reliability.default_threshold", "value": "0.75"},
			{"key": "retrieval.top_k", "value": "7"},
			{"key": "runtime.checkpoint.retention", "value": "14d"}
		]
	}`)
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/api/v1/platform/config", bytes.NewReader(goodBody))
	req.Header.Set("Authorization", "Bearer "+superToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var updateRes struct {
		Configs  []platformconfig.Item   `json:"configs"`
		Snapshot platformconfig.Snapshot `json:"snapshot"`
		Applied  []string                `json:"applied"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&updateRes))
	_ = resp.Body.Close()
	assert.Equal(t, 600, updateRes.Snapshot.RateLimitPerMinute)
	assert.Equal(t, 3, updateRes.Snapshot.CalibrationSamplingInterval)
	assert.InDelta(t, 0.75, updateRes.Snapshot.DefaultReliabilityThreshold, 0.001)
	assert.Equal(t, 7, updateRes.Snapshot.RetrievalTopK)
	assert.Equal(t, 14, updateRes.Snapshot.RetentionDays)

	// 5. Activity log reflects the real persisted config changes
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/platform/activity", nil)
	req.Header.Set("Authorization", "Bearer "+superToken)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var actRes struct {
		Events []platformconfig.AuditEvent `json:"events"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&actRes))
	_ = resp.Body.Close()
	assert.Len(t, actRes.Events, 5)

	// 6. Authenticated tenant user can read non-sensitive platform defaults
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/platform/defaults", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var defRes struct {
		ReliabilityDefaultThreshold float64 `json:"reliability_default_threshold"`
		CalibrationSamplingInterval int     `json:"calibration_sampling_interval"`
		RetrievalTopK               int     `json:"retrieval_top_k"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&defRes))
	_ = resp.Body.Close()
	assert.InDelta(t, 0.75, defRes.ReliabilityDefaultThreshold, 0.001)
	assert.Equal(t, 3, defRes.CalibrationSamplingInterval)
	assert.Equal(t, 7, defRes.RetrievalTopK)
}
