package platformconfig

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Change represents a single configuration key whose value changed during an update.
type Change struct {
	Key    string
	Label  string
	OldVal string
	NewVal string
}

// Store persists and retrieves platform-wide configuration settings and operator audit logs.
type Store interface {
	List(ctx context.Context) ([]Item, error)
	Update(ctx context.Context, updates map[string]string, operator string) ([]Item, []Change, error)
	GetSnapshot(ctx context.Context) Snapshot
	RecordAudit(ctx context.Context, title, detail, operator string) error
	ListAuditEvents(ctx context.Context, limit int) ([]AuditEvent, error)
}

// PostgresStore backs Store with PostgreSQL via GORM and a short-lived snapshot cache
// so Gateway and Temporal Worker processes pick up live changes within seconds.
type PostgresStore struct {
	db       *gorm.DB
	cacheTTL time.Duration

	mu          sync.RWMutex
	cachedSnap  Snapshot
	cacheExpiry time.Time
}

// NewPostgresStore auto-migrates platform_configs and platform_audit_logs and seeds defaults.
func NewPostgresStore(db *gorm.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, fmt.Errorf("db cannot be nil")
	}
	if err := db.AutoMigrate(&Item{}, &AuditEvent{}); err != nil {
		return nil, fmt.Errorf("failed to migrate platform config tables: %w", err)
	}

	st := &PostgresStore{
		db:         db,
		cacheTTL:   2 * time.Second,
		cachedSnap: DefaultSnapshot(),
	}

	if err := st.seedDefaults(context.Background()); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *PostgresStore) seedDefaults(ctx context.Context) error {
	now := time.Now().UTC()
	for _, def := range DefaultItems() {
		def.UpdatedAt = now
		if err := s.db.WithContext(ctx).
			Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).
			Create(&def).Error; err != nil {
			return fmt.Errorf("failed to seed platform config %s: %w", def.Key, err)
		}
	}
	return nil
}

// List returns all platform configuration items in canonical display order.
func (s *PostgresStore) List(ctx context.Context) ([]Item, error) {
	var rows []Item
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list platform configs: %w", err)
	}

	byKey := make(map[string]Item, len(rows))
	for _, r := range rows {
		byKey[r.Key] = r
	}

	defaults := DefaultItems()
	result := make([]Item, 0, len(defaults))
	for _, def := range defaults {
		if existing, ok := byKey[def.Key]; ok {
			existing.Label = def.Label
			existing.Description = def.Description
			existing.Category = def.Category
			result = append(result, existing)
		} else {
			result = append(result, def)
		}
	}
	return result, nil
}

// Update validates and persists updated configuration values atomically, writing
// platform_audit_logs entries for every changed setting.
func (s *PostgresStore) Update(ctx context.Context, updates map[string]string, operator string) ([]Item, []Change, error) {
	if len(updates) == 0 {
		items, err := s.List(ctx)
		return items, nil, err
	}

	normalized := make(map[string]string, len(updates))
	for k, v := range updates {
		norm, err := ValidateValue(k, v)
		if err != nil {
			return nil, nil, err
		}
		normalized[k] = norm
	}

	current, err := s.List(ctx)
	if err != nil {
		return nil, nil, err
	}

	now := time.Now().UTC()
	var changes []Change

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range current {
			newVal, ok := normalized[item.Key]
			if !ok {
				continue
			}
			if newVal == item.Value {
				continue
			}

			updated := item
			updated.Value = newVal
			updated.UpdatedBy = operator
			updated.UpdatedAt = now
			if err := tx.Save(&updated).Error; err != nil {
				return fmt.Errorf("failed to save config %s: %w", item.Key, err)
			}

			changes = append(changes, Change{
				Key:    item.Key,
				Label:  item.Label,
				OldVal: item.Value,
				NewVal: newVal,
			})

			audit := AuditEvent{
				ID:        fmt.Sprintf("plat-%s", uuid.NewString()[:12]),
				Title:     fmt.Sprintf("Changed config %s", item.Key),
				Detail:    fmt.Sprintf("%s: %q -> %q", item.Label, item.Value, newVal),
				Operator:  operator,
				CreatedAt: now,
			}
			if err := tx.Create(&audit).Error; err != nil {
				return fmt.Errorf("failed to record platform audit event: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	// Immediately invalidate local cache so this process observes new values at once.
	s.invalidateCache()

	items, err := s.List(ctx)
	return items, changes, err
}

func (s *PostgresStore) invalidateCache() {
	s.mu.Lock()
	s.cacheExpiry = time.Time{}
	s.mu.Unlock()
}

// GetSnapshot returns the typed live platform configuration using a short TTL cache.
func (s *PostgresStore) GetSnapshot(ctx context.Context) Snapshot {
	now := time.Now()
	s.mu.RLock()
	if now.Before(s.cacheExpiry) {
		snap := s.cachedSnap
		s.mu.RUnlock()
		return snap
	}
	s.mu.RUnlock()

	items, err := s.List(ctx)
	if err != nil {
		s.mu.RLock()
		snap := s.cachedSnap
		s.mu.RUnlock()
		return snap
	}
	snap, err := BuildSnapshot(items)
	if err != nil {
		s.mu.RLock()
		fallback := s.cachedSnap
		s.mu.RUnlock()
		return fallback
	}

	s.mu.Lock()
	s.cachedSnap = snap
	s.cacheExpiry = now.Add(s.cacheTTL)
	s.mu.Unlock()
	return snap
}

// RecordAudit writes a platform-level operator audit log entry.
func (s *PostgresStore) RecordAudit(ctx context.Context, title, detail, operator string) error {
	ev := AuditEvent{
		ID:        fmt.Sprintf("plat-%s", uuid.NewString()[:12]),
		Title:     title,
		Detail:    detail,
		Operator:  operator,
		CreatedAt: time.Now().UTC(),
	}
	return s.db.WithContext(ctx).Create(&ev).Error
}

// ListAuditEvents returns recent platform audit events ordered newest-first.
func (s *PostgresStore) ListAuditEvents(ctx context.Context, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var events []AuditEvent
	if err := s.db.WithContext(ctx).Order("created_at DESC").Limit(limit).Find(&events).Error; err != nil {
		return nil, fmt.Errorf("failed to list platform audit events: %w", err)
	}
	return events, nil
}

// MemoryStore provides an in-memory Store implementation for unit tests and DB-less setups.
type MemoryStore struct {
	mu     sync.RWMutex
	items  map[string]Item
	audits []AuditEvent
}

// NewMemoryStore initializes a MemoryStore seeded with DefaultItems.
func NewMemoryStore() *MemoryStore {
	m := &MemoryStore{
		items: make(map[string]Item),
	}
	now := time.Now().UTC()
	for _, def := range DefaultItems() {
		def.UpdatedAt = now
		m.items[def.Key] = def
	}
	return m
}

func (m *MemoryStore) List(_ context.Context) ([]Item, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	defaults := DefaultItems()
	out := make([]Item, 0, len(defaults))
	for _, def := range defaults {
		if existing, ok := m.items[def.Key]; ok {
			out = append(out, existing)
		} else {
			out = append(out, def)
		}
	}
	return out, nil
}

func (m *MemoryStore) Update(_ context.Context, updates map[string]string, operator string) ([]Item, []Change, error) {
	normalized := make(map[string]string, len(updates))
	for k, v := range updates {
		norm, err := ValidateValue(k, v)
		if err != nil {
			return nil, nil, err
		}
		normalized[k] = norm
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	var changes []Change
	for _, def := range DefaultItems() {
		cur, ok := m.items[def.Key]
		if !ok {
			cur = def
		}
		newVal, exists := normalized[def.Key]
		if !exists || newVal == cur.Value {
			continue
		}
		oldVal := cur.Value
		cur.Value = newVal
		cur.UpdatedBy = operator
		cur.UpdatedAt = now
		m.items[def.Key] = cur

		changes = append(changes, Change{
			Key:    cur.Key,
			Label:  cur.Label,
			OldVal: oldVal,
			NewVal: newVal,
		})
		m.audits = append([]AuditEvent{{
			ID:        fmt.Sprintf("plat-%s", uuid.NewString()[:12]),
			Title:     fmt.Sprintf("Changed config %s", cur.Key),
			Detail:    fmt.Sprintf("%s: %q -> %q", cur.Label, oldVal, newVal),
			Operator:  operator,
			CreatedAt: now,
		}}, m.audits...)
	}

	defaults := DefaultItems()
	out := make([]Item, 0, len(defaults))
	for _, def := range defaults {
		out = append(out, m.items[def.Key])
	}
	return out, changes, nil
}

func (m *MemoryStore) GetSnapshot(ctx context.Context) Snapshot {
	items, err := m.List(ctx)
	if err != nil {
		return DefaultSnapshot()
	}
	snap, err := BuildSnapshot(items)
	if err != nil {
		return DefaultSnapshot()
	}
	return snap
}

func (m *MemoryStore) RecordAudit(_ context.Context, title, detail, operator string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audits = append([]AuditEvent{{
		ID:        fmt.Sprintf("plat-%s", uuid.NewString()[:12]),
		Title:     title,
		Detail:    detail,
		Operator:  operator,
		CreatedAt: time.Now().UTC(),
	}}, m.audits...)
	return nil
}

func (m *MemoryStore) ListAuditEvents(_ context.Context, limit int) ([]AuditEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit <= 0 || limit > len(m.audits) {
		limit = len(m.audits)
	}
	out := make([]AuditEvent, limit)
	copy(out, m.audits[:limit])
	return out, nil
}
