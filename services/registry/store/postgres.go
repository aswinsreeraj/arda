package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// PostgresStore implements Store using a PostgreSQL database.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore creates a new PostgresStore.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

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

	query := `
		INSERT INTO agents (id, tenant_id, name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)
	`
	_, err := s.db.ExecContext(ctx, query, agent.ID, agent.TenantID, agent.Name, agent.CreatedAt, agent.UpdatedAt)
	if err != nil {
		return s.mapError(err, "create agent")
	}
	return nil
}

func (s *PostgresStore) CreateAgentWithInitialVersion(ctx context.Context, agent *domain.Agent, version *domain.AgentVersion) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.NewInternalError("failed to begin transaction", err)
	}
	defer tx.Rollback()

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

	queryAgent := `
		INSERT INTO agents (id, tenant_id, name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)
	`
	if _, err := tx.ExecContext(ctx, queryAgent, agent.ID, agent.TenantID, agent.Name, agent.CreatedAt, agent.UpdatedAt); err != nil {
		return s.mapError(err, "create agent in transaction")
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

	configJSON, err := json.Marshal(version.Configuration)
	if err != nil {
		return domain.NewValidationError("failed to marshal configuration to json", err.Error())
	}

	queryVersion := `
		INSERT INTO agent_versions (id, agent_id, version_number, configuration, published, created_at, updated_at)
		VALUES ($1, $2, $3, $4, false, $5, $6)
	`
	if _, err := tx.ExecContext(ctx, queryVersion, version.ID, version.AgentID, version.VersionNumber, configJSON, version.CreatedAt, version.UpdatedAt); err != nil {
		return s.mapError(err, "create initial version in transaction")
	}

	if err := tx.Commit(); err != nil {
		return domain.NewInternalError("failed to commit transaction", err)
	}
	return nil
}

func (s *PostgresStore) GetAgent(ctx context.Context, tenantID, agentID uuid.UUID) (*domain.Agent, error) {
	query := `
		SELECT id, tenant_id, name, created_at, updated_at
		FROM agents
		WHERE id = $1 AND tenant_id = $2
	`
	var agent domain.Agent
	err := s.db.QueryRowContext(ctx, query, agentID, tenantID).Scan(
		&agent.ID,
		&agent.TenantID,
		&agent.Name,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.NewNotFoundError("agent not found")
		}
		return nil, s.mapError(err, "get agent")
	}
	return &agent, nil
}

func (s *PostgresStore) GetNextVersionNumber(ctx context.Context, agentID uuid.UUID) (int, error) {
	query := `
		SELECT COALESCE(MAX(version_number), 0) + 1
		FROM agent_versions
		WHERE agent_id = $1
	`
	var nextNum int
	if err := s.db.QueryRowContext(ctx, query, agentID).Scan(&nextNum); err != nil {
		return 0, s.mapError(err, "get next version number")
	}
	return nextNum, nil
}

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

	configJSON, err := json.Marshal(version.Configuration)
	if err != nil {
		return domain.NewValidationError("failed to serialize configuration to json", err.Error())
	}

	query := `
		INSERT INTO agent_versions (id, agent_id, version_number, configuration, published, created_at, updated_at)
		VALUES ($1, $2, $3, $4, false, $5, $6)
	`
	_, err = s.db.ExecContext(ctx, query,
		version.ID,
		version.AgentID,
		version.VersionNumber,
		configJSON,
		version.CreatedAt,
		version.UpdatedAt,
	)
	if err != nil {
		return s.mapError(err, "create agent version")
	}
	return nil
}

func (s *PostgresStore) GetVersion(ctx context.Context, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	query := `
		SELECT id, agent_id, version_number, configuration, published, created_at, updated_at, published_at
		FROM agent_versions
		WHERE id = $1 AND agent_id = $2
	`
	return s.scanVersion(s.db.QueryRowContext(ctx, query, versionID, agentID))
}

func (s *PostgresStore) GetLatestPublishedVersion(ctx context.Context, agentID uuid.UUID) (*domain.AgentVersion, error) {
	query := `
		SELECT id, agent_id, version_number, configuration, published, created_at, updated_at, published_at
		FROM agent_versions
		WHERE agent_id = $1 AND published = true
		ORDER BY version_number DESC
		LIMIT 1
	`
	ver, err := s.scanVersion(s.db.QueryRowContext(ctx, query, agentID))
	if err != nil {
		if domainErr, ok := err.(*domain.DomainError); ok && domainErr.Code == domain.CodeNotFound {
			return nil, domain.NewNotFoundError("no published version found for agent")
		}
		return nil, err
	}
	return ver, nil
}

func (s *PostgresStore) GetLatestDraftVersion(ctx context.Context, agentID uuid.UUID) (*domain.AgentVersion, error) {
	query := `
		SELECT id, agent_id, version_number, configuration, published, created_at, updated_at, published_at
		FROM agent_versions
		WHERE agent_id = $1 AND published = false
		ORDER BY version_number DESC
		LIMIT 1
	`
	ver, err := s.scanVersion(s.db.QueryRowContext(ctx, query, agentID))
	if err != nil {
		if domainErr, ok := err.(*domain.DomainError); ok && domainErr.Code == domain.CodeNotFound {
			return nil, nil // No draft is not an error
		}
		return nil, err
	}
	return ver, nil
}

func (s *PostgresStore) PublishVersion(ctx context.Context, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	query := `
		UPDATE agent_versions
		SET published = true, published_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND agent_id = $2 AND published = false
		RETURNING id, agent_id, version_number, configuration, published, created_at, updated_at, published_at
	`
	ver, err := s.scanVersion(s.db.QueryRowContext(ctx, query, versionID, agentID))
	if err == nil {
		return ver, nil
	}

	// If no rows were returned, check if the version exists and is already published
	existing, checkErr := s.GetVersion(ctx, agentID, versionID)
	if checkErr != nil {
		return nil, checkErr
	}
	if existing.Published {
		return nil, domain.NewConflictError(domain.CodeAlreadyPublished, fmt.Sprintf("version %d is already published", existing.VersionNumber))
	}

	return nil, s.mapError(err, "publish version")
}

func (s *PostgresStore) UpdateDraftVersion(ctx context.Context, version *domain.AgentVersion) error {
	// Check if already published
	existing, err := s.GetVersion(ctx, version.AgentID, version.ID)
	if err != nil {
		return err
	}
	if existing.Published {
		return domain.NewConflictError(domain.CodePublishedImmutable, fmt.Sprintf("published version %d is immutable and cannot be updated", existing.VersionNumber))
	}

	configJSON, err := json.Marshal(version.Configuration)
	if err != nil {
		return domain.NewValidationError("failed to serialize configuration to json", err.Error())
	}

	query := `
		UPDATE agent_versions
		SET configuration = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2 AND agent_id = $3 AND published = false
		RETURNING updated_at
	`
	err = s.db.QueryRowContext(ctx, query, configJSON, version.ID, version.AgentID).Scan(&version.UpdatedAt)
	if err != nil {
		return s.mapError(err, "update draft version")
	}
	return nil
}

func (s *PostgresStore) DeleteVersion(ctx context.Context, agentID, versionID uuid.UUID) error {
	existing, err := s.GetVersion(ctx, agentID, versionID)
	if err != nil {
		return err
	}
	if existing.Published {
		return domain.NewConflictError(domain.CodePublishedImmutable, fmt.Sprintf("published version %d is immutable and cannot be deleted", existing.VersionNumber))
	}

	query := `
		DELETE FROM agent_versions
		WHERE id = $1 AND agent_id = $2 AND published = false
	`
	res, err := s.db.ExecContext(ctx, query, versionID, agentID)
	if err != nil {
		return s.mapError(err, "delete version")
	}
	rows, err := res.RowsAffected()
	if err == nil && rows == 0 {
		return domain.NewNotFoundError("version not found")
	}
	return nil
}

func (s *PostgresStore) scanVersion(row *sql.Row) (*domain.AgentVersion, error) {
	var ver domain.AgentVersion
	var configBytes []byte
	var publishedAt sql.NullTime

	err := row.Scan(
		&ver.ID,
		&ver.AgentID,
		&ver.VersionNumber,
		&configBytes,
		&ver.Published,
		&ver.CreatedAt,
		&ver.UpdatedAt,
		&publishedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.NewNotFoundError("agent version not found")
		}
		return nil, s.mapError(err, "scan version")
	}

	if publishedAt.Valid {
		ver.PublishedAt = &publishedAt.Time
	}

	if err := json.Unmarshal(configBytes, &ver.Configuration); err != nil {
		return nil, domain.NewInternalError("failed to parse configuration json from database", err)
	}

	return &ver, nil
}

func (s *PostgresStore) mapError(err error, op string) error {
	if err == nil {
		return nil
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		switch pqErr.Code {
		case "23505": // unique_violation
			return domain.NewConflictError(domain.CodeVersionConflict, fmt.Sprintf("conflict during %s: %s", op, pqErr.Detail))
		case "23503": // foreign_key_violation
			return domain.NewNotFoundError(fmt.Sprintf("referenced entity not found during %s", op))
		case "P0001": // raise_exception from PostgreSQL trigger
			return domain.NewConflictError(domain.CodePublishedImmutable, pqErr.Message)
		}
	}
	return domain.NewInternalError(fmt.Sprintf("database failure during %s", op), err)
}
