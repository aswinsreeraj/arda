package toolgateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Store defines persistence operations for tools, invocations, and audit events.
type Store interface {
	SaveTool(ctx context.Context, tool *ToolDefinition) error
	GetTool(ctx context.Context, tenantID uuid.UUID, id string) (*ToolDefinition, error)
	GetToolByName(ctx context.Context, tenantID uuid.UUID, name string) (*ToolDefinition, error)
	ListTools(ctx context.Context, tenantID uuid.UUID) ([]ToolDefinition, error)
	DeleteTool(ctx context.Context, tenantID uuid.UUID, id string) error

	SaveInvocation(ctx context.Context, inv *ToolInvocation) error
	GetInvocation(ctx context.Context, tenantID uuid.UUID, invocationID string) (*ToolInvocation, error)
	UpdateInvocation(ctx context.Context, inv *ToolInvocation) error

	SaveAuditEvent(ctx context.Context, event *AuditEvent) error
	ListAuditEvents(ctx context.Context, tenantID uuid.UUID, limit int) ([]AuditEvent, error)
}

// MemoryStore provides a thread-safe in-memory store for tools, invocations, and audit logs.
type MemoryStore struct {
	mu          sync.RWMutex
	tools       map[string]ToolDefinition
	invocations map[string]ToolInvocation
	auditEvents []AuditEvent
}

// NewMemoryStore constructs a fresh in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tools:       make(map[string]ToolDefinition),
		invocations: make(map[string]ToolInvocation),
		auditEvents: make([]AuditEvent, 0),
	}
}

func (s *MemoryStore) SaveTool(ctx context.Context, tool *ToolDefinition) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if tool.CreatedAt.IsZero() {
		tool.CreatedAt = now
	}
	tool.UpdatedAt = now

	// Check unique name per tenant
	for _, existing := range s.tools {
		if existing.TenantID == tool.TenantID && existing.Name == tool.Name && existing.ID != tool.ID {
			return fmt.Errorf("tool with name %q already exists in tenant", tool.Name)
		}
	}

	s.tools[tool.ID] = *tool
	return nil
}

func (s *MemoryStore) GetTool(ctx context.Context, tenantID uuid.UUID, id string) (*ToolDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tool, ok := s.tools[id]
	if !ok || tool.TenantID != tenantID {
		return nil, ErrToolNotFound
	}
	t := tool
	return &t, nil
}

func (s *MemoryStore) GetToolByName(ctx context.Context, tenantID uuid.UUID, name string) (*ToolDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, tool := range s.tools {
		if tool.TenantID == tenantID && tool.Name == name {
			t := tool
			return &t, nil
		}
	}
	return nil, ErrToolNotFound
}

func (s *MemoryStore) ListTools(ctx context.Context, tenantID uuid.UUID) ([]ToolDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var res []ToolDefinition
	for _, tool := range s.tools {
		if tool.TenantID == tenantID {
			res = append(res, tool)
		}
	}
	return res, nil
}

func (s *MemoryStore) DeleteTool(ctx context.Context, tenantID uuid.UUID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tool, ok := s.tools[id]
	if !ok || tool.TenantID != tenantID {
		return ErrToolNotFound
	}
	delete(s.tools, id)
	return nil
}

func (s *MemoryStore) SaveInvocation(ctx context.Context, inv *ToolInvocation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if inv.CreatedAt.IsZero() {
		inv.CreatedAt = now
	}
	inv.UpdatedAt = now

	if _, exists := s.invocations[inv.InvocationID]; exists {
		return fmt.Errorf("invocation %s already exists", inv.InvocationID)
	}

	s.invocations[inv.InvocationID] = *inv
	return nil
}

func (s *MemoryStore) GetInvocation(ctx context.Context, tenantID uuid.UUID, invocationID string) (*ToolInvocation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	inv, ok := s.invocations[invocationID]
	if !ok || inv.TenantID != tenantID {
		return nil, errors.New("invocation not found")
	}
	i := inv
	return &i, nil
}

func (s *MemoryStore) UpdateInvocation(ctx context.Context, inv *ToolInvocation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.invocations[inv.InvocationID]
	if !ok || existing.TenantID != inv.TenantID {
		return errors.New("invocation not found")
	}

	inv.UpdatedAt = time.Now().UTC()
	s.invocations[inv.InvocationID] = *inv
	return nil
}

func (s *MemoryStore) SaveAuditEvent(ctx context.Context, event *AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	s.auditEvents = append(s.auditEvents, *event)
	return nil
}

func (s *MemoryStore) ListAuditEvents(ctx context.Context, tenantID uuid.UUID, limit int) ([]AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var res []AuditEvent
	for i := len(s.auditEvents) - 1; i >= 0; i-- {
		if s.auditEvents[i].TenantID == tenantID {
			res = append(res, s.auditEvents[i])
			if limit > 0 && len(res) >= limit {
				break
			}
		}
	}
	return res, nil
}

// GormStore implements Store backed by PostgreSQL GORM.
type GormStore struct {
	db *gorm.DB
}

// NewGormStore creates a GormStore and performs schema auto-migration.
func NewGormStore(db *gorm.DB) (*GormStore, error) {
	if err := db.AutoMigrate(&ToolDefinition{}, &ToolInvocation{}, &AuditEvent{}); err != nil {
		return nil, fmt.Errorf("failed to auto-migrate tool gateway schema: %w", err)
	}
	return &GormStore{db: db}, nil
}

func (g *GormStore) SaveTool(ctx context.Context, tool *ToolDefinition) error {
	return g.db.WithContext(ctx).Save(tool).Error
}

func (g *GormStore) GetTool(ctx context.Context, tenantID uuid.UUID, id string) (*ToolDefinition, error) {
	var tool ToolDefinition
	err := g.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).First(&tool).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrToolNotFound
		}
		return nil, err
	}
	return &tool, nil
}

func (g *GormStore) GetToolByName(ctx context.Context, tenantID uuid.UUID, name string) (*ToolDefinition, error) {
	var tool ToolDefinition
	err := g.db.WithContext(ctx).Where("tenant_id = ? AND name = ?", tenantID, name).First(&tool).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrToolNotFound
		}
		return nil, err
	}
	return &tool, nil
}

func (g *GormStore) ListTools(ctx context.Context, tenantID uuid.UUID) ([]ToolDefinition, error) {
	var tools []ToolDefinition
	err := g.db.WithContext(ctx).Where("tenant_id = ?", tenantID).Order("name ASC").Find(&tools).Error
	return tools, err
}

func (g *GormStore) DeleteTool(ctx context.Context, tenantID uuid.UUID, id string) error {
	res := g.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).Delete(&ToolDefinition{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrToolNotFound
	}
	return nil
}

func (g *GormStore) SaveInvocation(ctx context.Context, inv *ToolInvocation) error {
	return g.db.WithContext(ctx).Create(inv).Error
}

func (g *GormStore) GetInvocation(ctx context.Context, tenantID uuid.UUID, invocationID string) (*ToolInvocation, error) {
	var inv ToolInvocation
	err := g.db.WithContext(ctx).Where("tenant_id = ? AND invocation_id = ?", tenantID, invocationID).First(&inv).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("invocation not found")
		}
		return nil, err
	}
	return &inv, nil
}

func (g *GormStore) UpdateInvocation(ctx context.Context, inv *ToolInvocation) error {
	return g.db.WithContext(ctx).Save(inv).Error
}

func (g *GormStore) SaveAuditEvent(ctx context.Context, event *AuditEvent) error {
	return g.db.WithContext(ctx).Create(event).Error
}

func (g *GormStore) ListAuditEvents(ctx context.Context, tenantID uuid.UUID, limit int) ([]AuditEvent, error) {
	var events []AuditEvent
	query := g.db.WithContext(ctx).Where("tenant_id = ?", tenantID).Order("timestamp DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&events).Error
	return events, err
}
