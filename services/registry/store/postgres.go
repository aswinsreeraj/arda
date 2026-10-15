package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// PostgresStore implements Store using a PostgreSQL database via GORM.
type PostgresStore struct {
	db *gorm.DB
}

// NewPostgresStore creates a new PostgresStore with the provided GORM database handle.
func NewPostgresStore(db *gorm.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// CreateAgent inserts a new agent record.
func (s *PostgresStore) CreateAgent(ctx context.Context, agent *domain.Agent) error {
	if agent.ID == uuid.Nil {
		agent.ID = uuid.New()
	}
	now := time.Now().UTC()
	if agent.CreatedAt.IsZero() {
		agent.CreatedAt = now
	}
	if agent.UpdatedAt.IsZero() {
		agent.UpdatedAt = now
	}

	model := AgentModel{
		ID:        agent.ID,
		TenantID:  agent.TenantID,
		Name:      agent.Name,
		CreatedAt: agent.CreatedAt,
		UpdatedAt: agent.UpdatedAt,
	}

	if err := s.db.WithContext(ctx).Create(&model).Error; err != nil {
		return s.mapError(err, "create agent")
	}
	return nil
}

// CreateAgentWithInitialVersion inserts an agent and its initial draft version within a single transaction.
func (s *PostgresStore) CreateAgentWithInitialVersion(ctx context.Context, agent *domain.Agent, version *domain.AgentVersion) error {
	if agent.ID == uuid.Nil {
		agent.ID = uuid.New()
	}
	now := time.Now().UTC()
	if agent.CreatedAt.IsZero() {
		agent.CreatedAt = now
	}
	if agent.UpdatedAt.IsZero() {
		agent.UpdatedAt = now
	}

	version.AgentID = agent.ID
	if version.ID == uuid.Nil {
		version.ID = uuid.New()
	}
	if version.VersionNumber <= 0 {
		version.VersionNumber = 1
	}
	if version.CreatedAt.IsZero() {
		version.CreatedAt = now
	}
	if version.UpdatedAt.IsZero() {
		version.UpdatedAt = now
	}

	agentModel := AgentModel{
		ID:        agent.ID,
		TenantID:  agent.TenantID,
		Name:      agent.Name,
		CreatedAt: agent.CreatedAt,
		UpdatedAt: agent.UpdatedAt,
	}

	versionModel := AgentVersionModel{
		ID:            version.ID,
		AgentID:       version.AgentID,
		VersionNumber: version.VersionNumber,
		Configuration: version.Configuration,
		Published:     false,
		CreatedAt:     version.CreatedAt,
		UpdatedAt:     version.UpdatedAt,
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&agentModel).Error; err != nil {
			return err
		}
		if err := tx.Create(&versionModel).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return s.mapError(err, "create agent with initial version in transaction")
	}
	return nil
}

// GetAgent retrieves an agent scoped to a tenant. Returns ErrNotFound if not present.
func (s *PostgresStore) GetAgent(ctx context.Context, tenantID, agentID uuid.UUID) (*domain.Agent, error) {
	var model AgentModel
	err := s.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ?", agentID, tenantID).
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.NewNotFoundError("agent not found")
		}
		return nil, s.mapError(err, "get agent")
	}
	return model.ToDomain(), nil
}

// GetNextVersionNumber returns the next incremental version number for an agent.
func (s *PostgresStore) GetNextVersionNumber(ctx context.Context, agentID uuid.UUID) (int, error) {
	var nextNum int
	row := s.db.WithContext(ctx).Model(&AgentVersionModel{}).
		Where("agent_id = ?", agentID).
		Select("COALESCE(MAX(version_number), 0) + 1").
		Row()
	if err := row.Scan(&nextNum); err != nil {
		return 0, s.mapError(err, "get next version number")
	}
	return nextNum, nil
}

// CreateVersion inserts a new draft version for an agent.
func (s *PostgresStore) CreateVersion(ctx context.Context, version *domain.AgentVersion) error {
	if version.ID == uuid.Nil {
		version.ID = uuid.New()
	}
	now := time.Now().UTC()
	if version.CreatedAt.IsZero() {
		version.CreatedAt = now
	}
	if version.UpdatedAt.IsZero() {
		version.UpdatedAt = now
	}

	model := AgentVersionModel{
		ID:            version.ID,
		AgentID:       version.AgentID,
		VersionNumber: version.VersionNumber,
		Configuration: version.Configuration,
		Published:     false,
		CreatedAt:     version.CreatedAt,
		UpdatedAt:     version.UpdatedAt,
	}

	if err := s.db.WithContext(ctx).Create(&model).Error; err != nil {
		return s.mapError(err, "create agent version")
	}
	return nil
}

// GetVersion retrieves a specific version of an agent.
func (s *PostgresStore) GetVersion(ctx context.Context, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	var model AgentVersionModel
	err := s.db.WithContext(ctx).
		Where("id = ? AND agent_id = ?", versionID, agentID).
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.NewNotFoundError("agent version not found")
		}
		return nil, s.mapError(err, "get version")
	}
	return model.ToDomain(), nil
}

// GetLatestPublishedVersion retrieves the latest published version of an agent.
func (s *PostgresStore) GetLatestPublishedVersion(ctx context.Context, agentID uuid.UUID) (*domain.AgentVersion, error) {
	var model AgentVersionModel
	err := s.db.WithContext(ctx).
		Where("agent_id = ? AND published = ?", agentID, true).
		Order("version_number DESC").
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.NewNotFoundError("no published version found for agent")
		}
		return nil, s.mapError(err, "get latest published version")
	}
	return model.ToDomain(), nil
}

// GetLatestDraftVersion retrieves the latest draft version of an agent, or nil if none.
func (s *PostgresStore) GetLatestDraftVersion(ctx context.Context, agentID uuid.UUID) (*domain.AgentVersion, error) {
	var model AgentVersionModel
	err := s.db.WithContext(ctx).
		Where("agent_id = ? AND published = ?", agentID, false).
		Order("version_number DESC").
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil // No draft is not an error
		}
		return nil, s.mapError(err, "get latest draft version")
	}
	return model.ToDomain(), nil
}

// PublishVersion transitions a draft version to published.
func (s *PostgresStore) PublishVersion(ctx context.Context, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&AgentVersionModel{}).
		Where("id = ? AND agent_id = ? AND published = ?", versionID, agentID, false).
		Updates(map[string]any{
			"published":    true,
			"published_at": now,
			"updated_at":   now,
		})
	if result.Error != nil {
		return nil, s.mapError(result.Error, "publish version")
	}
	if result.RowsAffected == 0 {
		// If no rows were updated, check if the version exists and is already published
		existing, checkErr := s.GetVersion(ctx, agentID, versionID)
		if checkErr != nil {
			return nil, checkErr
		}
		if existing.Published {
			return nil, domain.NewConflictError(domain.CodeAlreadyPublished, fmt.Sprintf("version %d is already published", existing.VersionNumber))
		}
		return nil, domain.NewNotFoundError("agent version not found")
	}
	return s.GetVersion(ctx, agentID, versionID)
}

// UpdateDraftVersion updates an existing draft version. Rejects update if version is published.
func (s *PostgresStore) UpdateDraftVersion(ctx context.Context, version *domain.AgentVersion) error {
	existing, err := s.GetVersion(ctx, version.AgentID, version.ID)
	if err != nil {
		return err
	}
	if existing.Published {
		return domain.NewConflictError(domain.CodePublishedImmutable, fmt.Sprintf("published version %d is immutable and cannot be updated", existing.VersionNumber))
	}

	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&AgentVersionModel{}).
		Where("id = ? AND agent_id = ? AND published = ?", version.ID, version.AgentID, false).
		Updates(map[string]any{
			"configuration": version.Configuration,
			"updated_at":    now,
		})
	if result.Error != nil {
		return s.mapError(result.Error, "update draft version")
	}
	if result.RowsAffected == 0 {
		return domain.NewNotFoundError("draft version not found")
	}
	version.UpdatedAt = now
	return nil
}

// DeleteVersion deletes an existing draft version. Rejects deletion if version is published.
func (s *PostgresStore) DeleteVersion(ctx context.Context, agentID, versionID uuid.UUID) error {
	existing, err := s.GetVersion(ctx, agentID, versionID)
	if err != nil {
		return err
	}
	if existing.Published {
		return domain.NewConflictError(domain.CodePublishedImmutable, fmt.Sprintf("published version %d is immutable and cannot be deleted", existing.VersionNumber))
	}

	result := s.db.WithContext(ctx).
		Where("id = ? AND agent_id = ? AND published = ?", versionID, agentID, false).
		Delete(&AgentVersionModel{})
	if result.Error != nil {
		return s.mapError(result.Error, "delete version")
	}
	if result.RowsAffected == 0 {
		return domain.NewNotFoundError("version not found")
	}
	return nil
}

func (s *PostgresStore) mapError(err error, op string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.NewNotFoundError(fmt.Sprintf("%s: not found", op))
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return domain.NewConflictError(domain.CodeVersionConflict, fmt.Sprintf("conflict during %s", op))
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return domain.NewConflictError(domain.CodeVersionConflict, fmt.Sprintf("conflict during %s: %s", op, pgErr.Detail))
		case "23503": // foreign_key_violation
			return domain.NewNotFoundError(fmt.Sprintf("referenced entity not found during %s", op))
		case "P0001": // raise_exception from PostgreSQL trigger
			return domain.NewConflictError(domain.CodePublishedImmutable, pgErr.Message)
		}
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		switch pqErr.Code {
		case "23505":
			return domain.NewConflictError(domain.CodeVersionConflict, fmt.Sprintf("conflict during %s: %s", op, pqErr.Detail))
		case "23503":
			return domain.NewNotFoundError(fmt.Sprintf("referenced entity not found during %s", op))
		case "P0001":
			return domain.NewConflictError(domain.CodePublishedImmutable, pqErr.Message)
		}
	}
	return domain.NewInternalError(fmt.Sprintf("database failure during %s", op), err)
}
