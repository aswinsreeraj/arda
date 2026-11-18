package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/services/gateway/auth"
	reviewevents "github.com/aswinsreeraj/arda/services/review/events"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testJWTSecret = "test-ws-secret-key-for-unit-tests-only-32bytes"

type stubTenantChecker struct {
	active bool
	err    error
}

func (s stubTenantChecker) IsTenantActive(_ context.Context, _ uuid.UUID) (bool, error) {
	return s.active, s.err
}

func issueTestJWT(t *testing.T, tenantID, userID uuid.UUID, role auth.Role) string {
	t.Helper()
	claims := auth.Claims{
		TenantID: tenantID.String(),
		Role:     string(role),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			Issuer:    "arda-auth",
			Audience:  jwt.ClaimStrings{"arda-api"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(testJWTSecret))
	require.NoError(t, err)
	return signed
}

func dialWS(t *testing.T, serverURL string, headers http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws/reviews"
	return websocket.DefaultDialer.Dial(wsURL, headers)
}

func TestWebSocket_FirstMessageAuthAndTenantIsolation(t *testing.T) {
	validator, err := auth.NewTokenValidator(auth.JWTConfig{
		Secret:   []byte(testJWTSecret),
		Issuer:   "arda-auth",
		Audience: "arda-api",
	})
	require.NoError(t, err)

	hub := NewHub()
	defer hub.Close()

	handler := NewHandler(HandlerConfig{
		Hub:            hub,
		JWTValidator:   validator,
		TenantChecker:  stubTenantChecker{active: true},
		AllowedOrigins: []string{"http://localhost:5174"},
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	tenantA := uuid.New()
	tenantB := uuid.New()
	tokenA := issueTestJWT(t, tenantA, uuid.New(), auth.RoleReviewer)
	tokenB := issueTestJWT(t, tenantB, uuid.New(), auth.RoleReviewer)

	// Connect Tenant A with allowed Origin and first-message JWT auth
	originHeader := http.Header{"Origin": []string{"http://localhost:5174"}}
	connA, _, err := dialWS(t, srv.URL, originHeader)
	require.NoError(t, err)
	defer connA.Close()

	require.NoError(t, connA.WriteJSON(map[string]string{"type": "auth", "token": tokenA}))
	var ackA Notification
	require.NoError(t, connA.ReadJSON(&ackA))
	assert.Equal(t, MessageTypeConnected, ackA.Type)
	assert.Equal(t, tenantA.String(), ackA.TenantID)

	// Connect Tenant B
	connB, _, err := dialWS(t, srv.URL, originHeader)
	require.NoError(t, err)
	defer connB.Close()

	require.NoError(t, connB.WriteJSON(map[string]string{"type": "auth", "token": tokenB}))
	var ackB Notification
	require.NoError(t, connB.ReadJSON(&ackB))
	assert.Equal(t, MessageTypeConnected, ackB.Type)
	assert.Equal(t, tenantB.String(), ackB.TenantID)

	assert.Equal(t, 1, hub.ClientCount(tenantA))
	assert.Equal(t, 1, hub.ClientCount(tenantB))

	// Publish a review.created event for Tenant A (twice with same EventID to verify dedup)
	createdEv := reviewevents.ReviewCreatedEvent{
		EventID:          "evt_created_1",
		EventType:        reviewevents.EventTypeReviewCreated,
		TenantID:         tenantA,
		AgentID:          uuid.New(),
		AgentName:        "Dispute Agent",
		CaseID:           "case_001",
		ExecutionID:      "exec_001",
		ProposedDecision: "REJECT",
		CreatedAt:        time.Now().UTC(),
	}
	rawCreated, err := json.Marshal(createdEv)
	require.NoError(t, err)
	hub.BroadcastRawEvent(reviewevents.TopicReviewCreated, rawCreated)
	hub.BroadcastRawEvent(reviewevents.TopicReviewCreated, rawCreated) // duplicate should be dropped

	// Publish a review.resolved event for Tenant A
	resolvedEv := reviewevents.ReviewResolvedEvent{
		EventID:       "evt_resolved_1",
		EventType:     reviewevents.EventTypeReviewResolved,
		TenantID:      tenantA,
		AgentID:       createdEv.AgentID,
		CaseID:        "case_001",
		ExecutionID:   "exec_001",
		FinalDecision: "APPROVE",
		ResolvedAt:    time.Now().UTC(),
	}
	rawResolved, err := json.Marshal(resolvedEv)
	require.NoError(t, err)
	hub.BroadcastRawEvent(reviewevents.TopicReviewResolved, rawResolved)

	// Tenant A receives review.created then review.resolved (no duplicate)
	_ = connA.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg1 Notification
	require.NoError(t, connA.ReadJSON(&msg1))
	assert.Equal(t, MessageTypeReviewCreated, msg1.Type)
	assert.Equal(t, "case_001", msg1.CaseID)
	assert.Equal(t, "OPEN", msg1.Status)

	var msg2 Notification
	require.NoError(t, connA.ReadJSON(&msg2))
	assert.Equal(t, MessageTypeReviewResolved, msg2.Type)
	assert.Equal(t, "case_001", msg2.CaseID)
	assert.Equal(t, "RESOLVED", msg2.Status)
	assert.Equal(t, "APPROVE", msg2.Decision)

	// Tenant B must receive NOTHING from Tenant A
	_ = connB.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var leaked Notification
	err = connB.ReadJSON(&leaked)
	assert.Error(t, err, "tenant B must not receive tenant A notifications")
}

func TestWebSocket_SecurityRejections(t *testing.T) {
	validator, err := auth.NewTokenValidator(auth.JWTConfig{
		Secret:   []byte(testJWTSecret),
		Issuer:   "arda-auth",
		Audience: "arda-api",
	})
	require.NoError(t, err)

	hub := NewHub()
	defer hub.Close()

	handler := NewHandler(HandlerConfig{
		Hub:            hub,
		JWTValidator:   validator,
		TenantChecker:  stubTenantChecker{active: true},
		AllowedOrigins: []string{"http://localhost:5174"},
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	t.Run("rejects credentials in URL query parameter", func(t *testing.T) {
		wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/reviews?token=should-never-be-in-url"
		_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
		require.Error(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("rejects untrusted cross-origin", func(t *testing.T) {
		_, resp, err := dialWS(t, srv.URL, http.Header{"Origin": []string{"https://evil.example.com"}})
		require.Error(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("rejects invalid JWT with close code 4001", func(t *testing.T) {
		conn, _, err := dialWS(t, srv.URL, nil)
		require.NoError(t, err)
		defer conn.Close()

		require.NoError(t, conn.WriteJSON(map[string]string{"type": "auth", "token": "not-a-valid-jwt"}))
		_, _, readErr := conn.ReadMessage()
		require.Error(t, readErr)
		closeErr, ok := readErr.(*websocket.CloseError)
		require.True(t, ok, "expected CloseError, got %T: %v", readErr, readErr)
		assert.Equal(t, CloseCodeUnauthenticated, closeErr.Code)
	})

	t.Run("rejects role without review:read permission with close code 4003", func(t *testing.T) {
		superToken := issueTestJWT(t, uuid.New(), uuid.New(), auth.RoleSuperAdmin)
		conn, _, err := dialWS(t, srv.URL, nil)
		require.NoError(t, err)
		defer conn.Close()

		require.NoError(t, conn.WriteJSON(map[string]string{"type": "auth", "token": superToken}))
		_, _, readErr := conn.ReadMessage()
		require.Error(t, readErr)
		closeErr, ok := readErr.(*websocket.CloseError)
		require.True(t, ok, "expected CloseError, got %T: %v", readErr, readErr)
		assert.Equal(t, CloseCodeForbidden, closeErr.Code)
	})

	t.Run("rejects suspended tenant with close code 4003", func(t *testing.T) {
		suspendedHandler := NewHandler(HandlerConfig{
			Hub:           hub,
			JWTValidator:  validator,
			TenantChecker: stubTenantChecker{active: false},
		})
		suspendedSrv := httptest.NewServer(suspendedHandler)
		defer suspendedSrv.Close()

		tok := issueTestJWT(t, uuid.New(), uuid.New(), auth.RoleReviewer)
		conn, _, err := dialWS(t, suspendedSrv.URL, nil)
		require.NoError(t, err)
		defer conn.Close()

		require.NoError(t, conn.WriteJSON(map[string]string{"type": "auth", "token": tok}))
		_, _, readErr := conn.ReadMessage()
		require.Error(t, readErr)
		closeErr, ok := readErr.(*websocket.CloseError)
		require.True(t, ok)
		assert.Equal(t, CloseCodeForbidden, closeErr.Code)
	})
}
