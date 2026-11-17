package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aswinsreeraj/arda/services/review/domain"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PostgresStore implements Store using PostgreSQL via GORM.
type PostgresStore struct {
	db *gorm.DB
}

// NewPostgresStore constructs a PostgresStore.
func NewPostgresStore(db *gorm.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// CreateCase persists a review case and optional outbox event atomically.
func (s *PostgresStore) CreateCase(ctx context.Context, rc *domain.ReviewCase, outbox *OutboxEventModel) error {
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
	if rc.EscalationReasons == nil {
		rc.EscalationReasons = []string{}
	}
	if rc.ExecutionMode == "" {
		rc.ExecutionMode = "realtime"
	}

	model := ReviewCaseModel{
		ID:                   rc.ID,
		TenantID:             rc.TenantID,
		ExecutionID:          rc.ExecutionID,
		AgentID:              rc.AgentID,
		AgentName:            rc.AgentName,
		AgentVersionID:       rc.AgentVersionID,
		AgentVersionNumber:   rc.AgentVersionNumber,
		ExecutionMode:        rc.ExecutionMode,
		Status:               string(rc.Status),
		ProposedDecision:     rc.ProposedDecision,
		Confidence:           rc.Confidence,
		ReliabilityThreshold: rc.ReliabilityThreshold,
		EscalationReasons:    rc.EscalationReasons,
		FrozenEvidence:       rc.FrozenEvidence,
		Decision:             string(rc.Decision),
		FinalDecision:        rc.FinalDecision,
		ResolutionReason:     rc.ResolutionReason,
		ReviewerID:           rc.ReviewerID,
		AuditRecordID:        rc.AuditRecordID,
		ResolvedAt:           rc.ResolvedAt,
		CreatedAt:            rc.CreatedAt,
		UpdatedAt:            rc.UpdatedAt,
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model).Error; err != nil {
			return fmt.Errorf("failed to insert review case: %w", err)
		}
		if outbox != nil {
			if outbox.CreatedAt.IsZero() {
				outbox.CreatedAt = now
			}
			if err := tx.Create(outbox).Error; err != nil {
				return fmt.Errorf("failed to insert initial outbox event: %w", err)
			}
		}
		return nil
	})
}

// GetCase retrieves a review case by ID with tenant isolation.
func (s *PostgresStore) GetCase(ctx context.Context, tenantID uuid.UUID, caseID string) (*domain.ReviewCase, error) {
	var model ReviewCaseModel
	err := s.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ?", caseID, tenantID).
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.NewNotFoundError("review case not found")
		}
		return nil, fmt.Errorf("failed to get review case: %w", err)
	}
	return model.ToDomain(), nil
}

// ListCases retrieves tenant-scoped review cases with filtering and pagination.
func (s *PostgresStore) ListCases(ctx context.Context, filter domain.ListFilter) ([]domain.ReviewCaseSummary, string, int32, error) {
	query := s.db.WithContext(ctx).Model(&ReviewCaseModel{}).Where("tenant_id = ?", filter.TenantID)

	status := filter.Status
	if status != "" && !strings.EqualFold(string(status), "all") && string(status) != "*" {
		query = query.Where("status = ?", string(status))
	} else if status == "" {
		query = query.Where("status = ?", string(domain.CaseStatusOpen))
	}

	if filter.AgentID != uuid.Nil {
		query = query.Where("agent_id = ?", filter.AgentID)
	}

	if filter.EscalationReason != "" {
		query = query.Where("escalation_reasons::text ILIKE ?", "%"+filter.EscalationReason+"%")
	}

	var totalCount int64
	if err := query.Count(&totalCount).Error; err != nil {
		return nil, "", 0, fmt.Errorf("failed to count review cases: %w", err)
	}

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
			if parsedOffset, err := strconv.Atoi(string(decoded)); err == nil && parsedOffset >= 0 {
				offset = parsedOffset
			}
		}
	}

	var models []ReviewCaseModel
	err := query.Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&models).Error
	if err != nil {
		return nil, "", 0, fmt.Errorf("failed to list review cases: %w", err)
	}

	summaries := make([]domain.ReviewCaseSummary, 0, len(models))
	for _, m := range models {
		summaries = append(summaries, domain.ReviewCaseSummary{
			CaseID:            m.ID,
			ExecutionID:       m.ExecutionID,
			AgentID:           m.AgentID,
			AgentName:         m.AgentName,
			ProposedDecision:  m.ProposedDecision,
			Confidence:        m.Confidence,
			EscalationReasons: m.EscalationReasons,
			Status:            domain.CaseStatus(m.Status),
			CreatedAt:         m.CreatedAt,
		})
	}

	var nextPageToken string
	if int64(offset+len(models)) < totalCount {
		nextPageToken = base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(offset + len(models))))
	}

	return summaries, nextPageToken, int32(totalCount), nil
}

// ResolveCase atomically verifies the case is OPEN, transitions to RESOLVED,
// updates resolution fields, and enqueues outbox event in the same transaction with row-level locking.
func (s *PostgresStore) ResolveCase(
	ctx context.Context,
	req domain.ResolveCaseRequest,
	auditRecordID string,
	finalDecision string,
	outbox *OutboxEventModel,
) (*domain.ReviewCase, error) {
	var resolvedCase *domain.ReviewCase

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model ReviewCaseModel
		// Use SELECT FOR UPDATE for concurrency safety
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ?", req.CaseID, req.TenantID).
			First(&model).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.NewNotFoundError("review case not found")
			}
			return fmt.Errorf("failed to query review case for resolution: %w", err)
		}

		if model.Status != string(domain.CaseStatusOpen) {
			return domain.NewConflictError("review case is already resolved", domain.ErrCaseAlreadyResolved)
		}

		now := time.Now().UTC()
		model.Status = string(domain.CaseStatusResolved)
		model.Decision = string(req.Decision)
		model.FinalDecision = finalDecision
		model.ResolutionReason = req.Reason
		model.ReviewerID = req.ReviewerID
		model.AuditRecordID = auditRecordID
		model.ResolvedAt = &now
		model.UpdatedAt = now

		if err := tx.Save(&model).Error; err != nil {
			return fmt.Errorf("failed to update review case resolution: %w", err)
		}

		if outbox != nil {
			if outbox.CreatedAt.IsZero() {
				outbox.CreatedAt = now
			}
			if err := tx.Create(outbox).Error; err != nil {
				return fmt.Errorf("failed to write resolution outbox event: %w", err)
			}
		}

		resolvedCase = model.ToDomain()
		return nil
	})

	if err != nil {
		return nil, err
	}
	return resolvedCase, nil
}

// FetchPendingOutboxEvents retrieves pending outbox records ordered chronologically.
func (s *PostgresStore) FetchPendingOutboxEvents(ctx context.Context, limit int) ([]OutboxEventModel, error) {
	if limit <= 0 {
		limit = 50
	}
	var events []OutboxEventModel
	err := s.db.WithContext(ctx).
		Where("status = ?", "PENDING").
		Order("created_at ASC").
		Limit(limit).
		Find(&events).Error
	if err != nil {
		return nil, fmt.Errorf("failed to fetch pending outbox events: %w", err)
	}
	return events, nil
}

// MarkOutboxEventPublished marks the given event as published.
func (s *PostgresStore) MarkOutboxEventPublished(ctx context.Context, eventID string) error {
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Model(&OutboxEventModel{}).
		Where("id = ?", eventID).
		Updates(map[string]any{
			"status":       "PUBLISHED",
			"published_at": now,
		}).Error
	if err != nil {
		return fmt.Errorf("failed to mark outbox event as published: %w", err)
	}
	return nil
}

// MarkOutboxEventFailed increments retry count and sets error message.
func (s *PostgresStore) MarkOutboxEventFailed(ctx context.Context, eventID string, errMsg string) error {
	err := s.db.WithContext(ctx).Model(&OutboxEventModel{}).
		Where("id = ?", eventID).
		Updates(map[string]any{
			"retry_count":   gorm.Expr("retry_count + 1"),
			"error_message": errMsg,
		}).Error
	if err != nil {
		return fmt.Errorf("failed to record outbox failure: %w", err)
	}
	return nil
}
