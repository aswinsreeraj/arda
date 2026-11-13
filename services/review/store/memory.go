package store

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aswinsreeraj/arda/services/review/domain"
	"github.com/google/uuid"
)

// MemoryStore provides an in-memory implementation of Store for unit testing.
type MemoryStore struct {
	mu     sync.RWMutex
	cases  map[string]*domain.ReviewCase
	outbox map[string]*OutboxEventModel
}

// NewMemoryStore creates an initialized MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		cases:  make(map[string]*domain.ReviewCase),
		outbox: make(map[string]*OutboxEventModel),
	}
}

// CreateCase stores a review case in memory.
func (m *MemoryStore) CreateCase(ctx context.Context, rc *domain.ReviewCase, outbox *OutboxEventModel) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	if rc.CreatedAt.IsZero() {
		rc.CreatedAt = now
	}
	if rc.UpdatedAt.IsZero() {
		rc.UpdatedAt = now
	}
	if rc.Status == "" {
		rc.Status = domain.CaseStatusOpen
	}
	if rc.ID == "" {
		rc.ID = fmt.Sprintf("case_%s", uuid.New().String()[:12])
	}

	copied := *rc
	m.cases[rc.ID] = &copied

	if outbox != nil {
		if outbox.CreatedAt.IsZero() {
			outbox.CreatedAt = now
		}
		outboxCopy := *outbox
		m.outbox[outbox.ID] = &outboxCopy
	}

	return nil
}

// GetCase retrieves a case matching the tenant and case ID.
func (m *MemoryStore) GetCase(ctx context.Context, tenantID uuid.UUID, caseID string) (*domain.ReviewCase, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	c, ok := m.cases[caseID]
	if !ok || c.TenantID != tenantID {
		return nil, domain.NewNotFoundError("review case not found")
	}

	copied := *c
	return &copied, nil
}

// ListCases filters and paginates cases in memory.
func (m *MemoryStore) ListCases(ctx context.Context, filter domain.ListFilter) ([]domain.ReviewCaseSummary, string, int32, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := filter.Status
	if status == "" {
		status = domain.CaseStatusOpen
	}

	var matches []*domain.ReviewCase
	for _, c := range m.cases {
		if c.TenantID != filter.TenantID {
			continue
		}
		if c.Status != status {
			continue
		}
		if filter.AgentID != uuid.Nil && c.AgentID != filter.AgentID {
			continue
		}
		if filter.EscalationReason != "" {
			found := false
			for _, r := range c.EscalationReasons {
				if strings.Contains(strings.ToLower(r), strings.ToLower(filter.EscalationReason)) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		matches = append(matches, c)
	}

	// Sort newest first
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].CreatedAt.After(matches[j].CreatedAt)
	})

	totalCount := int32(len(matches))
	limit := int(filter.PageSize)
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	offset := 0
	if filter.PageToken != "" {
		if decoded, err := base64.StdEncoding.DecodeString(filter.PageToken); err == nil {
			if parsed, err := strconv.Atoi(string(decoded)); err == nil && parsed >= 0 {
				offset = parsed
			}
		}
	}

	if offset >= len(matches) {
		return []domain.ReviewCaseSummary{}, "", totalCount, nil
	}

	end := offset + limit
	if end > len(matches) {
		end = len(matches)
	}

	summaries := make([]domain.ReviewCaseSummary, 0, end-offset)
	for _, c := range matches[offset:end] {
		summaries = append(summaries, domain.ReviewCaseSummary{
			CaseID:            c.ID,
			ExecutionID:       c.ExecutionID,
			AgentID:           c.AgentID,
			AgentName:         c.AgentName,
			ProposedDecision:  c.ProposedDecision,
			Confidence:        c.Confidence,
			EscalationReasons: c.EscalationReasons,
			Status:            c.Status,
			CreatedAt:         c.CreatedAt,
		})
	}

	var nextPageToken string
	if end < len(matches) {
		nextPageToken = base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}

	return summaries, nextPageToken, totalCount, nil
}

// ResolveCase transitions an OPEN case to RESOLVED in memory with locking.
func (m *MemoryStore) ResolveCase(
	ctx context.Context,
	req domain.ResolveCaseRequest,
	auditRecordID string,
	finalDecision string,
	outbox *OutboxEventModel,
) (*domain.ReviewCase, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	c, ok := m.cases[req.CaseID]
	if !ok || c.TenantID != req.TenantID {
		return nil, domain.NewNotFoundError("review case not found")
	}

	if c.Status != domain.CaseStatusOpen {
		return nil, domain.NewConflictError("review case is already resolved", domain.ErrCaseAlreadyResolved)
	}

	now := time.Now().UTC()
	c.Status = domain.CaseStatusResolved
	c.Decision = req.Decision
	c.FinalDecision = finalDecision
	c.ResolutionReason = req.Reason
	c.ReviewerID = req.ReviewerID
	c.AuditRecordID = auditRecordID
	c.ResolvedAt = &now
	c.UpdatedAt = now

	if outbox != nil {
		if outbox.CreatedAt.IsZero() {
			outbox.CreatedAt = now
		}
		outboxCopy := *outbox
		m.outbox[outbox.ID] = &outboxCopy
	}

	copied := *c
	return &copied, nil
}

// FetchPendingOutboxEvents returns all PENDING outbox events.
func (m *MemoryStore) FetchPendingOutboxEvents(ctx context.Context, limit int) ([]OutboxEventModel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var pending []OutboxEventModel
	for _, o := range m.outbox {
		if o.Status == "PENDING" {
			pending = append(pending, *o)
		}
	}

	sort.Slice(pending, func(i, j int) bool {
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})

	if limit > 0 && len(pending) > limit {
		pending = pending[:limit]
	}

	return pending, nil
}

// MarkOutboxEventPublished marks an event PUBLISHED.
func (m *MemoryStore) MarkOutboxEventPublished(ctx context.Context, eventID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	o, ok := m.outbox[eventID]
	if !ok {
		return fmt.Errorf("outbox event not found: %s", eventID)
	}
	now := time.Now().UTC()
	o.Status = "PUBLISHED"
	o.PublishedAt = &now
	return nil
}

// MarkOutboxEventFailed records failure.
func (m *MemoryStore) MarkOutboxEventFailed(ctx context.Context, eventID string, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	o, ok := m.outbox[eventID]
	if !ok {
		return fmt.Errorf("outbox event not found: %s", eventID)
	}
	o.RetryCount++
	o.ErrorMessage = errMsg
	if o.RetryCount >= 5 {
		o.Status = "FAILED"
	}
	return nil
}

// SaveOutboxEvent is a test helper for seeding outbox events directly.
func (m *MemoryStore) SaveOutboxEvent(ctx context.Context, o *OutboxEventModel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := *o
	m.outbox[o.ID] = &copied
	return nil
}

// GetOutboxEvent retrieves an outbox event by ID.
func (m *MemoryStore) GetOutboxEvent(ctx context.Context, eventID string) (*OutboxEventModel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o, ok := m.outbox[eventID]
	if !ok {
		return nil, fmt.Errorf("outbox event not found: %s", eventID)
	}
	copied := *o
	return &copied, nil
}
