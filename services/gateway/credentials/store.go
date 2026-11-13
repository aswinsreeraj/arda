package credentials

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrNotFound indicates the credential record does not exist for the tenant.
var ErrNotFound = errors.New("credential not found")

// Store defines persistence operations for credential metadata.
type Store interface {
	Save(ctx context.Context, cred *Credential) error
	Get(ctx context.Context, tenantID, id uuid.UUID) (*Credential, error)
	GetByCategoryAndProvider(ctx context.Context, tenantID uuid.UUID, category Category, provider string) (*Credential, error)
	List(ctx context.Context, tenantID uuid.UUID) ([]Credential, error)
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
}

// CredentialModel represents the PostgreSQL database table structure.
type CredentialModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	TenantID  uuid.UUID `gorm:"column:tenant_id;type:uuid;not null;index"`
	Category  string    `gorm:"column:category;type:varchar(50);not null"`
	Provider  string    `gorm:"column:provider;type:varchar(100);not null"`
	Reference string    `gorm:"column:reference;type:text;not null"`
	CreatedAt time.Time `gorm:"column:created_at;type:timestamptz;not null"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:timestamptz;not null"`
}

func (CredentialModel) TableName() string {
	return "credentials"
}

func (m CredentialModel) ToDomain() *Credential {
	return &Credential{
		ID:        m.ID,
		TenantID:  m.TenantID,
		Category:  Category(m.Category),
		Provider:  m.Provider,
		Reference: m.Reference,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

// PostgresStore implements Store backed by PostgreSQL GORM.
type PostgresStore struct {
	db *gorm.DB
}

// NewPostgresStore creates a new PostgresStore instance.
func NewPostgresStore(db *gorm.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) Save(ctx context.Context, cred *Credential) error {
	now := time.Now().UTC()
	if cred.CreatedAt.IsZero() {
		cred.CreatedAt = now
	}
	cred.UpdatedAt = now

	cleanCategory := strings.ToLower(strings.TrimSpace(string(cred.Category)))
	cleanProvider := strings.ToLower(strings.TrimSpace(cred.Provider))
	cred.Category = Category(cleanCategory)
	cred.Provider = cleanProvider

	model := CredentialModel{
		ID:        cred.ID,
		TenantID:  cred.TenantID,
		Category:  cleanCategory,
		Provider:  cleanProvider,
		Reference: cred.Reference,
		CreatedAt: cred.CreatedAt,
		UpdatedAt: cred.UpdatedAt,
	}

	var existing CredentialModel
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND LOWER(category) = ? AND LOWER(provider) = ?", cred.TenantID, cleanCategory, cleanProvider).
		First(&existing).Error

	if err == nil {
		cred.ID = existing.ID
		model.ID = existing.ID
		model.CreatedAt = existing.CreatedAt
		cred.CreatedAt = existing.CreatedAt
		return s.db.WithContext(ctx).Save(&model).Error
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "category"}, {Name: "provider"}},
			DoUpdates: clause.AssignmentColumns([]string{"reference", "updated_at"}),
		}).Create(&model).Error
	}
	return err
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, id uuid.UUID) (*Credential, error) {
	var model CredentialModel
	err := s.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ?", id, tenantID).
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return model.ToDomain(), nil
}

func (s *PostgresStore) GetByCategoryAndProvider(ctx context.Context, tenantID uuid.UUID, category Category, provider string) (*Credential, error) {
	cleanCategory := strings.ToLower(strings.TrimSpace(string(category)))
	cleanProvider := strings.ToLower(strings.TrimSpace(provider))
	var model CredentialModel
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND LOWER(category) = ? AND LOWER(provider) = ?", tenantID, cleanCategory, cleanProvider).
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return model.ToDomain(), nil
}

func (s *PostgresStore) List(ctx context.Context, tenantID uuid.UUID) ([]Credential, error) {
	var models []CredentialModel
	err := s.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at DESC").
		Find(&models).Error
	if err != nil {
		return nil, err
	}
	creds := make([]Credential, len(models))
	for i, m := range models {
		creds[i] = *m.ToDomain()
	}
	return creds, nil
}

func (s *PostgresStore) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	result := s.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ?", id, tenantID).
		Delete(&CredentialModel{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// MemoryStore implements an in-memory Store for unit tests.
type MemoryStore struct {
	mu    sync.RWMutex
	creds map[uuid.UUID]*Credential
}

// NewMemoryStore creates a thread-safe in-memory Store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		creds: make(map[uuid.UUID]*Credential),
	}
}

func (m *MemoryStore) Save(ctx context.Context, cred *Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	if cred.CreatedAt.IsZero() {
		cred.CreatedAt = now
	}
	cred.UpdatedAt = now

	cleanCategory := strings.ToLower(strings.TrimSpace(string(cred.Category)))
	cleanProvider := strings.ToLower(strings.TrimSpace(cred.Provider))
	cred.Category = Category(cleanCategory)
	cred.Provider = cleanProvider

	for _, c := range m.creds {
		if c.TenantID == cred.TenantID && strings.EqualFold(string(c.Category), cleanCategory) && strings.EqualFold(c.Provider, cleanProvider) {
			cred.ID = c.ID
			cred.CreatedAt = c.CreatedAt
			break
		}
	}
	m.creds[cred.ID] = cred
	return nil
}

func (m *MemoryStore) Get(ctx context.Context, tenantID, id uuid.UUID) (*Credential, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	c, ok := m.creds[id]
	if !ok || c.TenantID != tenantID {
		return nil, ErrNotFound
	}
	copied := *c
	return &copied, nil
}

func (m *MemoryStore) GetByCategoryAndProvider(ctx context.Context, tenantID uuid.UUID, category Category, provider string) (*Credential, error) {
	cleanCategory := strings.ToLower(strings.TrimSpace(string(category)))
	cleanProvider := strings.ToLower(strings.TrimSpace(provider))
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, c := range m.creds {
		if c.TenantID == tenantID && strings.EqualFold(string(c.Category), cleanCategory) && strings.EqualFold(c.Provider, cleanProvider) {
			copied := *c
			return &copied, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemoryStore) List(ctx context.Context, tenantID uuid.UUID) ([]Credential, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []Credential
	for _, c := range m.creds {
		if c.TenantID == tenantID {
			result = append(result, *c)
		}
	}
	return result, nil
}

func (m *MemoryStore) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	c, ok := m.creds[id]
	if !ok || c.TenantID != tenantID {
		return ErrNotFound
	}
	delete(m.creds, id)
	return nil
}
