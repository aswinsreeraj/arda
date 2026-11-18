package ws

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	authHandshakeTimeout = 5 * time.Second
	writeWait            = 10 * time.Second
	pongWait             = 60 * time.Second
	pingPeriod           = 30 * time.Second
	maxMessageBytes      = 8 * 1024

	// Application-level WebSocket close codes (4000-4999 reserved for private use).
	CloseCodeUnauthenticated = 4001
	CloseCodeForbidden       = 4003
)

// HandlerConfig configures the tenant-scoped Review WebSocket HTTP upgrade handler.
type HandlerConfig struct {
	Hub            *Hub
	JWTValidator   *auth.TokenValidator
	TenantChecker  auth.TenantChecker
	AllowedOrigins []string
}

// Handler upgrades authenticated browser connections to tenant-scoped WebSocket streams.
type Handler struct {
	hub            *Hub
	jwtValidator   *auth.TokenValidator
	tenantChecker  auth.TenantChecker
	allowedOrigins map[string]struct{}
	upgrader       websocket.Upgrader
}

type clientMessage struct {
	Type  string `json:"type"`
	Token string `json:"token,omitempty"`
}

// DefaultAllowedOrigins builds the trusted origin allow-list from environment configuration
// and local development origins. Wildcard origins are never permitted.
func DefaultAllowedOrigins() []string {
	origins := []string{
		"http://localhost:5174",
		"http://localhost:5173",
		"http://127.0.0.1:5174",
		"http://127.0.0.1:5173",
	}
	if appURL := strings.TrimSpace(os.Getenv("APP_URL")); appURL != "" {
		origins = append(origins, strings.TrimRight(appURL, "/"))
	}
	if raw := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			if cleaned := strings.TrimRight(strings.TrimSpace(part), "/"); cleaned != "" && cleaned != "*" {
				origins = append(origins, cleaned)
			}
		}
	}
	return origins
}

// NewHandler creates a new tenant-scoped WebSocket upgrade handler.
func NewHandler(cfg HandlerConfig) *Handler {
	originsList := cfg.AllowedOrigins
	if len(originsList) == 0 {
		originsList = DefaultAllowedOrigins()
	}

	allowed := make(map[string]struct{}, len(originsList))
	for _, o := range originsList {
		cleaned := strings.ToLower(strings.TrimRight(strings.TrimSpace(o), "/"))
		if cleaned != "" && cleaned != "*" {
			allowed[cleaned] = struct{}{}
		}
	}

	h := &Handler{
		hub:            cfg.Hub,
		jwtValidator:   cfg.JWTValidator,
		tenantChecker:  cfg.TenantChecker,
		allowedOrigins: allowed,
	}
	h.upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		CheckOrigin:     h.checkOrigin,
	}
	return h
}

func (h *Handler) checkOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// Non-browser clients (e.g. CLI tools / integration tests) do not send Origin.
		return true
	}

	normalized := strings.ToLower(strings.TrimRight(origin, "/"))
	if _, ok := h.allowedOrigins[normalized]; ok {
		return true
	}

	// Permit exact same-host origin (e.g. behind a reverse proxy or Vite dev proxy).
	parsed, err := url.Parse(origin)
	if err == nil && parsed.Host != "" && strings.EqualFold(parsed.Host, r.Host) {
		return true
	}

	return false
}

// ServeHTTP upgrades the connection, authenticates the caller via Bearer header or
// first-message JWT handshake (never URL query parameters), verifies RBAC and active
// tenant status, and streams tenant-scoped review notifications.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.hub == nil || h.jwtValidator == nil {
		http.Error(w, `{"error":{"code":"SERVICE_UNAVAILABLE","message":"websocket service unavailable"}}`, http.StatusServiceUnavailable)
		return
	}

	// Reject credentials passed in URL parameters (fail-closed security rule).
	if r.URL.Query().Get("token") != "" || r.URL.Query().Get("access_token") != "" {
		http.Error(w, `{"error":{"code":"BAD_REQUEST","message":"credentials must not be passed in URL parameters"}}`, http.StatusBadRequest)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	conn.SetReadLimit(maxMessageBytes)

	tokenString := extractBearerHeader(r.Header.Get("Authorization"))
	if tokenString == "" {
		// Browser clients cannot set custom Authorization headers on WebSocket upgrade,
		// so authenticate via the first JSON message within a short deadline.
		_ = conn.SetReadDeadline(time.Now().Add(authHandshakeTimeout))
		msgType, raw, err := conn.ReadMessage()
		if err != nil || msgType != websocket.TextMessage {
			closeWithReason(conn, CloseCodeUnauthenticated, "authentication required")
			return
		}
		var authMsg clientMessage
		if err := json.Unmarshal(raw, &authMsg); err != nil || authMsg.Type != "auth" || strings.TrimSpace(authMsg.Token) == "" {
			closeWithReason(conn, CloseCodeUnauthenticated, "invalid authentication message")
			return
		}
		tokenString = strings.TrimSpace(authMsg.Token)
	}

	identity, err := h.jwtValidator.ValidateToken(tokenString)
	if err != nil || identity.TenantID == uuid.Nil {
		closeWithReason(conn, CloseCodeUnauthenticated, "invalid or expired token")
		return
	}

	// Enforce RBAC: caller must be allowed to read review cases.
	if err := auth.Authorize(identity, auth.ActionReviewRead); err != nil {
		closeWithReason(conn, CloseCodeForbidden, "insufficient permissions for review stream")
		return
	}

	// Enforce active tenant status (matching REST API middleware).
	if h.tenantChecker != nil && identity.Role != auth.RoleSuperAdmin {
		checkCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		active, checkErr := h.tenantChecker.IsTenantActive(checkCtx, identity.TenantID)
		cancel()
		if checkErr != nil || !active {
			closeWithReason(conn, CloseCodeForbidden, "organization is inactive or suspended")
			return
		}
	}

	client := newClient(identity.TenantID, identity.UserID)
	if !h.hub.Register(client) {
		closeWithReason(conn, websocket.CloseGoingAway, "server shutting down")
		return
	}

	// Send connected acknowledgement containing the verified tenant scope.
	ack, _ := json.Marshal(Notification{
		Type:      MessageTypeConnected,
		TenantID:  identity.TenantID.String(),
		Timestamp: time.Now().UTC(),
	})
	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := conn.WriteMessage(websocket.TextMessage, ack); err != nil {
		h.hub.Unregister(client)
		_ = conn.Close()
		return
	}

	go h.writePump(conn, client)
	h.readPump(conn, client)
}

func (h *Handler) readPump(conn *websocket.Conn, client *Client) {
	defer func() {
		h.hub.Unregister(client)
		_ = conn.Close()
	}()

	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		msgType, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
				log.Printf("[WS] client disconnected unexpectedly (tenant_id=%s): %v", client.tenantID, err)
			}
			return
		}
		if msgType != websocket.TextMessage {
			continue
		}

		// Optional lightweight client-initiated application ping.
		var msg clientMessage
		if json.Unmarshal(raw, &msg) == nil && msg.Type == "ping" {
			pong, _ := json.Marshal(Notification{
				Type:      MessageTypePong,
				TenantID:  client.tenantID.String(),
				Timestamp: time.Now().UTC(),
			})
			select {
			case client.send <- pong:
			default:
			}
		}
	}
}

func (h *Handler) writePump(conn *websocket.Conn, client *Client) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = conn.Close()
	}()

	for {
		select {
		case payload, ok := <-client.send:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, ""))
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func extractBearerHeader(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func closeWithReason(conn *websocket.Conn, code int, reason string) {
	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason))
	_ = conn.Close()
}
