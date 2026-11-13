package store

import (
	"context"

	"github.com/aswinsreeraj/arda/services/review/domain"
	"github.com/google/uuid"
)

// Store defines persistence operations for review cases and outbox events.
type Store interface {
	// CreateCase persists a review case and initial outbox event atomically.
	CreateCase(ctx context.Context, reviewCase *domain.ReviewCase, outboxEvent *OutboxEventModel) error

	// GetCase retrieves a review case by ID, enforcing tenant scoping.
	GetCase(ctx context.Context, tenantID uuid.UUID, caseID string) (*domain.ReviewCase, error)

	// ListCases retrieves tenant-scoped review cases matching the filter with pagination.
	ListCases(ctx context.Context, filter domain.ListFilter) ([]domain.ReviewCaseSummary, string, int32, error)

	// ResolveCase atomically verifies that the case is OPEN, transitions it to RESOLVED,
	// updates the final resolution data, and enqueues the outbox event in a single transaction.
	ResolveCase(ctx context.Context, req domain.ResolveCaseRequest, auditRecordID string, finalDecision string, outboxEvent *OutboxEventModel) (*domain.ReviewCase, error)

	// FetchPendingOutboxEvents retrieves unhandled outbox events for publishing.
	FetchPendingOutboxEvents(ctx context.Context, limit int) ([]OutboxEventModel, error)

	// MarkOutboxEventPublished updates the status of an outbox event to PUBLISHED.
	MarkOutboxEventPublished(ctx context.Context, eventID string) error

	// MarkOutboxEventFailed records a retry or failure for an outbox event.
	MarkOutboxEventFailed(ctx context.Context, eventID string, errMsg string) error
}
