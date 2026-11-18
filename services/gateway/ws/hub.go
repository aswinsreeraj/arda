package ws

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	reviewevents "github.com/aswinsreeraj/arda/services/review/events"
	"github.com/google/uuid"
)

const (
	// MessageTypeConnected confirms successful authentication and tenant subscription.
	MessageTypeConnected = "connected"
	// MessageTypeReviewCreated notifies clients that a new review case was escalated.
	MessageTypeReviewCreated = "review.created"
	// MessageTypeReviewResolved notifies clients that a review case was adjudicated.
	MessageTypeReviewResolved = "review.resolved"
	// MessageTypePong acknowledges a client-initiated application ping.
	MessageTypePong = "pong"

	maxSeenEvents = 512
)

// Notification is the lightweight tenant-scoped message pushed over WebSockets.
// The REST API remains the source of truth; clients use these notifications to
// invalidate cached queries and refetch authoritative state.
type Notification struct {
	Type        string    `json:"type"`
	EventID     string    `json:"event_id,omitempty"`
	TenantID    string    `json:"tenant_id,omitempty"`
	CaseID      string    `json:"case_id,omitempty"`
	ExecutionID string    `json:"execution_id,omitempty"`
	AgentID     string    `json:"agent_id,omitempty"`
	AgentName   string    `json:"agent_name,omitempty"`
	Status      string    `json:"status,omitempty"`
	Decision    string    `json:"decision,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
}

// Client represents a single authenticated, tenant-scoped WebSocket connection.
type Client struct {
	tenantID  uuid.UUID
	userID    string
	send      chan []byte
	closeOnce sync.Once
}

func newClient(tenantID uuid.UUID, userID string) *Client {
	return &Client{
		tenantID: tenantID,
		userID:   userID,
		send:     make(chan []byte, 64),
	}
}

func (c *Client) closeSend() {
	c.closeOnce.Do(func() {
		close(c.send)
	})
}

// Hub maintains active WebSocket connections strictly partitioned by tenant ID.
type Hub struct {
	mu        sync.RWMutex
	clients   map[uuid.UUID]map[*Client]struct{}
	closed    bool
	seenIDs   map[string]struct{}
	seenOrder []string
}

// NewHub constructs an initialized tenant-scoped WebSocket Hub.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[uuid.UUID]map[*Client]struct{}),
		seenIDs: make(map[string]struct{}, maxSeenEvents),
	}
}

// Register adds an authenticated client to its verified tenant's room.
func (h *Hub) Register(c *Client) bool {
	if c == nil || c.tenantID == uuid.Nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	if _, ok := h.clients[c.tenantID]; !ok {
		h.clients[c.tenantID] = make(map[*Client]struct{})
	}
	h.clients[c.tenantID][c] = struct{}{}
	return true
}

// Unregister removes a client from its tenant room and closes its outbound channel.
func (h *Hub) Unregister(c *Client) {
	if c == nil {
		return
	}
	h.mu.Lock()
	if room, ok := h.clients[c.tenantID]; ok {
		delete(room, c)
		if len(room) == 0 {
			delete(h.clients, c.tenantID)
		}
	}
	h.mu.Unlock()
	c.closeSend()
}

// ClientCount returns the number of active WebSocket connections for a tenant.
func (h *Hub) ClientCount(tenantID uuid.UUID) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients[tenantID])
}

// Broadcast fans out a notification strictly to clients belonging to tenantID.
// If msg.EventID is non-empty and was already broadcast recently, the duplicate is dropped.
func (h *Hub) Broadcast(tenantID uuid.UUID, msg Notification) {
	if tenantID == uuid.Nil {
		return
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}
	if msg.TenantID == "" {
		msg.TenantID = tenantID.String()
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	if msg.EventID != "" {
		if _, dup := h.seenIDs[msg.EventID]; dup {
			h.mu.Unlock()
			return
		}
		h.recordSeenLocked(msg.EventID)
	}
	room := h.clients[tenantID]
	targets := make([]*Client, 0, len(room))
	for c := range room {
		targets = append(targets, c)
	}
	h.mu.Unlock()

	var slow []*Client
	for _, c := range targets {
		select {
		case c.send <- payload:
		default:
			// Slow consumer: drop and disconnect to protect hub liveness.
			slow = append(slow, c)
		}
	}
	for _, c := range slow {
		h.Unregister(c)
	}
}

func (h *Hub) recordSeenLocked(eventID string) {
	if len(h.seenOrder) >= maxSeenEvents {
		evict := h.seenOrder[0]
		h.seenOrder = h.seenOrder[1:]
		delete(h.seenIDs, evict)
	}
	h.seenIDs[eventID] = struct{}{}
	h.seenOrder = append(h.seenOrder, eventID)
}

// BroadcastRawEvent parses a serialized Redpanda/outbox review lifecycle event
// and fans out a lightweight notification to the event's tenant.
func (h *Hub) BroadcastRawEvent(topic string, payload []byte) {
	if h == nil || len(payload) == 0 {
		return
	}

	switch topic {
	case reviewevents.TopicReviewCreated:
		var ev reviewevents.ReviewCreatedEvent
		if err := json.Unmarshal(payload, &ev); err != nil || ev.TenantID == uuid.Nil {
			return
		}
		ts := ev.CreatedAt
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		h.Broadcast(ev.TenantID, Notification{
			Type:        MessageTypeReviewCreated,
			EventID:     strings.TrimSpace(ev.EventID),
			TenantID:    ev.TenantID.String(),
			CaseID:      ev.CaseID,
			ExecutionID: ev.ExecutionID,
			AgentID:     ev.AgentID.String(),
			AgentName:   ev.AgentName,
			Status:      "OPEN",
			Decision:    ev.ProposedDecision,
			Timestamp:   ts,
		})

	case reviewevents.TopicReviewResolved:
		var ev reviewevents.ReviewResolvedEvent
		if err := json.Unmarshal(payload, &ev); err != nil || ev.TenantID == uuid.Nil {
			return
		}
		ts := ev.ResolvedAt
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		decision := strings.TrimSpace(ev.FinalDecision)
		if decision == "" {
			decision = strings.TrimSpace(ev.HumanDecision)
		}
		h.Broadcast(ev.TenantID, Notification{
			Type:        MessageTypeReviewResolved,
			EventID:     strings.TrimSpace(ev.EventID),
			TenantID:    ev.TenantID.String(),
			CaseID:      ev.CaseID,
			ExecutionID: ev.ExecutionID,
			AgentID:     ev.AgentID.String(),
			Status:      "RESOLVED",
			Decision:    decision,
			Timestamp:   ts,
		})
	}
}

// Close shuts down the hub and closes all active client channels.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	var all []*Client
	for _, room := range h.clients {
		for c := range room {
			all = append(all, c)
		}
	}
	h.clients = make(map[uuid.UUID]map[*Client]struct{})
	h.mu.Unlock()

	for _, c := range all {
		c.closeSend()
	}
}
