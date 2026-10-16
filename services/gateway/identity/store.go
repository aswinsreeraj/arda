package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrUserNotFound             = errors.New("user not found")
	ErrUserAlreadyExists         = errors.New("user with this email already exists")
	ErrTenantNotFound           = errors.New("tenant not found")
	ErrMembershipNotFound       = errors.New("tenant membership not found")
	ErrMembershipAlreadyExists   = errors.New("user already has a membership in this tenant")
	ErrCannotRemoveLastAdmin    = errors.New("cannot remove or demote the last active tenant administrator")
)

// Store defines persistence operations for user accounts, tenants, and memberships.
type Store interface {
	CreateUser(ctx context.Context, user *User) error
	GetUserByID(ctx context.Context, id uuid.UUID) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	UpdateUser(ctx context.Context, user *User) error

	CreateTenantWithAdmin(ctx context.Context, tenant *Tenant, adminUserID uuid.UUID) (*Tenant, *Membership, error)
	RegisterTenantWithAdmin(ctx context.Context, tenant *Tenant, user *User) (*Tenant, *User, *Membership, error)
	GetTenantByID(ctx context.Context, id uuid.UUID) (*Tenant, error)

	CreateMembership(ctx context.Context, membership *Membership) error
	GetMembership(ctx context.Context, userID, tenantID uuid.UUID) (*Membership, error)
	ListUserMemberships(ctx context.Context, userID uuid.UUID) ([]Membership, error)
	ListTenantMembers(ctx context.Context, tenantID uuid.UUID) ([]MemberInfo, error)
	UpdateMembership(ctx context.Context, membership *Membership) error
	DeleteMembership(ctx context.Context, userID, tenantID uuid.UUID) error
	CountActiveAdmins(ctx context.Context, tenantID uuid.UUID) (int64, error)
}

// UserModel maps to the 'app_users' table.
type UserModel struct {
	ID              uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	Email           string     `gorm:"column:email;type:text;not null"`
	PasswordHash    string     `gorm:"column:password_hash;type:text;not null"`
	DisplayName     string     `gorm:"column:display_name;type:text;not null;default:''"`
	Status          string     `gorm:"column:status;type:text;not null;default:'active'"`
	IsSuperAdmin    bool       `gorm:"column:is_super_admin;type:boolean;not null;default:false"`
	EmailVerifiedAt *time.Time `gorm:"column:email_verified_at;type:timestamptz"`
	CreatedAt       time.Time  `gorm:"column:created_at;type:timestamptz;not null"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;type:timestamptz;not null"`
}

func (UserModel) TableName() string {
	return "app_users"
}

func (m UserModel) ToDomain() *User {
	return &User{
		ID:              m.ID,
		Email:           m.Email,
		PasswordHash:    m.PasswordHash,
		DisplayName:     m.DisplayName,
		Status:          UserStatus(m.Status),
		IsSuperAdmin:    m.IsSuperAdmin,
		EmailVerifiedAt: m.EmailVerifiedAt,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
}

// TenantModel maps to the 'tenants' table.
type TenantModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	Name      string    `gorm:"column:name;type:text;not null"`
	Status    string    `gorm:"column:status;type:text;not null;default:'active'"`
	CreatedAt time.Time `gorm:"column:created_at;type:timestamptz;not null"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:timestamptz;not null"`
}

func (TenantModel) TableName() string {
	return "tenants"
}

func (m TenantModel) ToDomain() *Tenant {
	return &Tenant{
		ID:        m.ID,
		Name:      m.Name,
		Status:    m.Status,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

// MembershipModel maps to the 'tenant_memberships' table.
type MembershipModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	UserID    uuid.UUID `gorm:"column:user_id;type:uuid;not null;index"`
	TenantID  uuid.UUID `gorm:"column:tenant_id;type:uuid;not null;index"`
	Role      string    `gorm:"column:role;type:text;not null"`
	Status    string    `gorm:"column:status;type:text;not null;default:'active'"`
	CreatedAt time.Time `gorm:"column:created_at;type:timestamptz;not null"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:timestamptz;not null"`
}

func (MembershipModel) TableName() string {
	return "tenant_memberships"
}

func (m MembershipModel) ToDomain() *Membership {
	return &Membership{
		ID:        m.ID,
		UserID:    m.UserID,
		TenantID:  m.TenantID,
		Role:      auth.Role(m.Role),
		Status:    MembershipStatus(m.Status),
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

// PostgresStore implements Store backed by PostgreSQL via GORM.
type PostgresStore struct {
	db *gorm.DB
}

// NewPostgresStore creates a new PostgresStore.
func NewPostgresStore(db *gorm.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) CreateUser(ctx context.Context, u *User) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	now := time.Now().UTC()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	if u.UpdatedAt.IsZero() {
		u.UpdatedAt = now
	}

	model := UserModel{
		ID:              u.ID,
		Email:           strings.ToLower(strings.TrimSpace(u.Email)),
		PasswordHash:    u.PasswordHash,
		DisplayName:     u.DisplayName,
		Status:          string(u.Status),
		IsSuperAdmin:    u.IsSuperAdmin,
		EmailVerifiedAt: u.EmailVerifiedAt,
		CreatedAt:       u.CreatedAt,
		UpdatedAt:       u.UpdatedAt,
	}

	err := s.db.WithContext(ctx).Create(&model).Error
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return ErrUserAlreadyExists
		}
		return fmt.Errorf("failed to create user: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetUserByID(ctx context.Context, id uuid.UUID) (*User, error) {
	var model UserModel
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("failed to get user by id: %w", err)
	}
	return model.ToDomain(), nil
}

func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	normEmail := strings.ToLower(strings.TrimSpace(email))
	var model UserModel
	err := s.db.WithContext(ctx).Where("LOWER(TRIM(email)) = ?", normEmail).First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("failed to get user by email: %w", err)
	}
	return model.ToDomain(), nil
}

func (s *PostgresStore) UpdateUser(ctx context.Context, u *User) error {
	u.UpdatedAt = time.Now().UTC()
	model := UserModel{
		ID:              u.ID,
		Email:           strings.ToLower(strings.TrimSpace(u.Email)),
		PasswordHash:    u.PasswordHash,
		DisplayName:     u.DisplayName,
		Status:          string(u.Status),
		IsSuperAdmin:    u.IsSuperAdmin,
		EmailVerifiedAt: u.EmailVerifiedAt,
		UpdatedAt:       u.UpdatedAt,
	}
	res := s.db.WithContext(ctx).Model(&UserModel{}).Where("id = ?", u.ID).Updates(model)
	if res.Error != nil {
		return fmt.Errorf("failed to update user: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (s *PostgresStore) CreateTenantWithAdmin(ctx context.Context, t *Tenant, adminUserID uuid.UUID) (*Tenant, *Membership, error) {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = now
	}
	if t.Status == "" {
		t.Status = "active"
	}

	membership := &Membership{
		ID:        uuid.New(),
		UserID:    adminUserID,
		TenantID:  t.ID,
		Role:      auth.RoleCompanyAdmin,
		Status:    MembershipStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Create tenant
		tModel := TenantModel{
			ID:        t.ID,
			Name:      t.Name,
			Status:    t.Status,
			CreatedAt: t.CreatedAt,
			UpdatedAt: t.UpdatedAt,
		}
		if err := tx.Create(&tModel).Error; err != nil {
			return fmt.Errorf("failed to insert tenant: %w", err)
		}

		// 2. Create membership
		mModel := MembershipModel{
			ID:        membership.ID,
			UserID:    membership.UserID,
			TenantID:  membership.TenantID,
			Role:      string(membership.Role),
			Status:    string(membership.Status),
			CreatedAt: membership.CreatedAt,
			UpdatedAt: membership.UpdatedAt,
		}
		if err := tx.Create(&mModel).Error; err != nil {
			return fmt.Errorf("failed to insert tenant administrator membership: %w", err)
		}
		return nil
	})

	if err != nil {
		return nil, nil, err
	}
	return t, membership, nil
}

func (s *PostgresStore) RegisterTenantWithAdmin(ctx context.Context, t *Tenant, u *User) (*Tenant, *User, *Membership, error) {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	now := time.Now().UTC()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	if u.UpdatedAt.IsZero() {
		u.UpdatedAt = now
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = now
	}
	if t.Status == "" {
		t.Status = "active"
	}
	if u.Status == "" {
		u.Status = UserStatusActive
	}

	membership := &Membership{
		ID:        uuid.New(),
		UserID:    u.ID,
		TenantID:  t.ID,
		Role:      auth.RoleCompanyAdmin,
		Status:    MembershipStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		uModel := UserModel{
			ID:              u.ID,
			Email:           strings.ToLower(strings.TrimSpace(u.Email)),
			PasswordHash:    u.PasswordHash,
			DisplayName:     u.DisplayName,
			Status:          string(u.Status),
			IsSuperAdmin:    u.IsSuperAdmin,
			EmailVerifiedAt: u.EmailVerifiedAt,
			CreatedAt:       u.CreatedAt,
			UpdatedAt:       u.UpdatedAt,
		}
		if err := tx.Create(&uModel).Error; err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
				return ErrUserAlreadyExists
			}
			return fmt.Errorf("failed to insert user: %w", err)
		}

		tModel := TenantModel{
			ID:        t.ID,
			Name:      t.Name,
			Status:    t.Status,
			CreatedAt: t.CreatedAt,
			UpdatedAt: t.UpdatedAt,
		}
		if err := tx.Create(&tModel).Error; err != nil {
			return fmt.Errorf("failed to insert tenant: %w", err)
		}

		mModel := MembershipModel{
			ID:        membership.ID,
			UserID:    membership.UserID,
			TenantID:  membership.TenantID,
			Role:      string(membership.Role),
			Status:    string(membership.Status),
			CreatedAt: membership.CreatedAt,
			UpdatedAt: membership.UpdatedAt,
		}
		if err := tx.Create(&mModel).Error; err != nil {
			return fmt.Errorf("failed to insert tenant administrator membership: %w", err)
		}
		return nil
	})

	if err != nil {
		return nil, nil, nil, err
	}
	return t, u, membership, nil
}

func (s *PostgresStore) GetTenantByID(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	var model TenantModel
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTenantNotFound
		}
		return nil, fmt.Errorf("failed to get tenant by id: %w", err)
	}
	return model.ToDomain(), nil
}

func (s *PostgresStore) CreateMembership(ctx context.Context, m *Membership) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = now
	}
	if m.Status == "" {
		m.Status = MembershipStatusActive
	}

	model := MembershipModel{
		ID:        m.ID,
		UserID:    m.UserID,
		TenantID:  m.TenantID,
		Role:      string(m.Role),
		Status:    string(m.Status),
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}

	err := s.db.WithContext(ctx).Create(&model).Error
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return ErrMembershipAlreadyExists
		}
		return fmt.Errorf("failed to create tenant membership: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetMembership(ctx context.Context, userID, tenantID uuid.UUID) (*Membership, error) {
	var model MembershipModel
	err := s.db.WithContext(ctx).Where("user_id = ? AND tenant_id = ?", userID, tenantID).First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrMembershipNotFound
		}
		return nil, fmt.Errorf("failed to get membership: %w", err)
	}
	return model.ToDomain(), nil
}

func (s *PostgresStore) ListUserMemberships(ctx context.Context, userID uuid.UUID) ([]Membership, error) {
	var models []MembershipModel
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at ASC").Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list user memberships: %w", err)
	}
	res := make([]Membership, len(models))
	for i, m := range models {
		res[i] = *m.ToDomain()
	}
	return res, nil
}

func (s *PostgresStore) ListTenantMembers(ctx context.Context, tenantID uuid.UUID) ([]MemberInfo, error) {
	type resultRow struct {
		ID          uuid.UUID
		UserID      uuid.UUID
		TenantID    uuid.UUID
		Email       string
		DisplayName string
		Role        string
		Status      string
		CreatedAt   time.Time
	}

	var rows []resultRow
	err := s.db.WithContext(ctx).
		Table("tenant_memberships").
		Select("tenant_memberships.id, tenant_memberships.user_id, tenant_memberships.tenant_id, app_users.email, app_users.display_name, tenant_memberships.role, tenant_memberships.status, tenant_memberships.created_at").
		Joins("JOIN app_users ON app_users.id = tenant_memberships.user_id").
		Where("tenant_memberships.tenant_id = ?", tenantID).
		Order("tenant_memberships.created_at ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query tenant members: %w", err)
	}

	members := make([]MemberInfo, len(rows))
	for i, r := range rows {
		members[i] = MemberInfo{
			ID:          r.ID,
			UserID:      r.UserID,
			TenantID:    r.TenantID,
			Email:       r.Email,
			DisplayName: r.DisplayName,
			Role:        auth.Role(r.Role),
			Status:      MembershipStatus(r.Status),
			CreatedAt:   r.CreatedAt,
		}
	}
	return members, nil
}

func (s *PostgresStore) UpdateMembership(ctx context.Context, m *Membership) error {
	m.UpdatedAt = time.Now().UTC()
	res := s.db.WithContext(ctx).Model(&MembershipModel{}).
		Where("user_id = ? AND tenant_id = ?", m.UserID, m.TenantID).
		Updates(map[string]interface{}{
			"role":       string(m.Role),
			"status":     string(m.Status),
			"updated_at": m.UpdatedAt,
		})
	if res.Error != nil {
		return fmt.Errorf("failed to update membership: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrMembershipNotFound
	}
	return nil
}

func (s *PostgresStore) DeleteMembership(ctx context.Context, userID, tenantID uuid.UUID) error {
	res := s.db.WithContext(ctx).Where("user_id = ? AND tenant_id = ?", userID, tenantID).Delete(&MembershipModel{})
	if res.Error != nil {
		return fmt.Errorf("failed to delete membership: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrMembershipNotFound
	}
	return nil
}

func (s *PostgresStore) CountActiveAdmins(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&MembershipModel{}).
		Where("tenant_id = ? AND role = ? AND status = ?", tenantID, string(auth.RoleCompanyAdmin), string(MembershipStatusActive)).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("failed to count active admins: %w", err)
	}
	return count, nil
}

// MemoryStore provides a thread-safe in-memory Store implementation for testing.
type MemoryStore struct {
	mu          sync.RWMutex
	users       map[uuid.UUID]*User
	usersByMail map[string]uuid.UUID
	tenants     map[uuid.UUID]*Tenant
	memberships map[string]*Membership // key: userID:tenantID
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		users:       make(map[uuid.UUID]*User),
		usersByMail: make(map[string]uuid.UUID),
		tenants:     make(map[uuid.UUID]*Tenant),
		memberships: make(map[string]*Membership),
	}
}

func (m *MemoryStore) CreateUser(ctx context.Context, u *User) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	normMail := strings.ToLower(strings.TrimSpace(u.Email))
	if _, exists := m.usersByMail[normMail]; exists {
		return ErrUserAlreadyExists
	}
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	now := time.Now().UTC()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	if u.UpdatedAt.IsZero() {
		u.UpdatedAt = now
	}

	copied := *u
	copied.Email = normMail
	m.users[u.ID] = &copied
	m.usersByMail[normMail] = u.ID
	return nil
}

func (m *MemoryStore) GetUserByID(ctx context.Context, id uuid.UUID) (*User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	u, exists := m.users[id]
	if !exists {
		return nil, ErrUserNotFound
	}
	copied := *u
	return &copied, nil
}

func (m *MemoryStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	normMail := strings.ToLower(strings.TrimSpace(email))
	id, exists := m.usersByMail[normMail]
	if !exists {
		return nil, ErrUserNotFound
	}
	u := m.users[id]
	copied := *u
	return &copied, nil
}

func (m *MemoryStore) UpdateUser(ctx context.Context, u *User) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, exists := m.users[u.ID]
	if !exists {
		return ErrUserNotFound
	}

	normMail := strings.ToLower(strings.TrimSpace(u.Email))
	if normMail != existing.Email {
		if _, collision := m.usersByMail[normMail]; collision {
			return ErrUserAlreadyExists
		}
		delete(m.usersByMail, existing.Email)
		m.usersByMail[normMail] = u.ID
	}

	u.UpdatedAt = time.Now().UTC()
	copied := *u
	copied.Email = normMail
	m.users[u.ID] = &copied
	return nil
}

func (m *MemoryStore) CreateTenantWithAdmin(ctx context.Context, t *Tenant, adminUserID uuid.UUID) (*Tenant, *Membership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = now
	}
	if t.Status == "" {
		t.Status = "active"
	}

	tCopy := *t
	m.tenants[t.ID] = &tCopy

	membership := &Membership{
		ID:        uuid.New(),
		UserID:    adminUserID,
		TenantID:  t.ID,
		Role:      auth.RoleCompanyAdmin,
		Status:    MembershipStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	mCopy := *membership
	key := fmt.Sprintf("%s:%s", adminUserID, t.ID)
	m.memberships[key] = &mCopy

	return &tCopy, &mCopy, nil
}

func (m *MemoryStore) RegisterTenantWithAdmin(ctx context.Context, t *Tenant, u *User) (*Tenant, *User, *Membership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	normMail := strings.ToLower(strings.TrimSpace(u.Email))
	if _, exists := m.usersByMail[normMail]; exists {
		return nil, nil, nil, ErrUserAlreadyExists
	}
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	now := time.Now().UTC()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	if u.UpdatedAt.IsZero() {
		u.UpdatedAt = now
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = now
	}
	if t.Status == "" {
		t.Status = "active"
	}
	if u.Status == "" {
		u.Status = UserStatusActive
	}

	uCopy := *u
	uCopy.Email = normMail
	m.users[u.ID] = &uCopy
	m.usersByMail[normMail] = u.ID

	tCopy := *t
	m.tenants[t.ID] = &tCopy

	membership := &Membership{
		ID:        uuid.New(),
		UserID:    u.ID,
		TenantID:  t.ID,
		Role:      auth.RoleCompanyAdmin,
		Status:    MembershipStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	mCopy := *membership
	key := fmt.Sprintf("%s:%s", u.ID, t.ID)
	m.memberships[key] = &mCopy

	return &tCopy, &uCopy, &mCopy, nil
}

func (m *MemoryStore) GetTenantByID(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	t, exists := m.tenants[id]
	if !exists {
		return nil, ErrTenantNotFound
	}
	copied := *t
	return &copied, nil
}

func (m *MemoryStore) CreateMembership(ctx context.Context, mem *Membership) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s:%s", mem.UserID, mem.TenantID)
	if _, exists := m.memberships[key]; exists {
		return ErrMembershipAlreadyExists
	}

	if mem.ID == uuid.Nil {
		mem.ID = uuid.New()
	}
	now := time.Now().UTC()
	if mem.CreatedAt.IsZero() {
		mem.CreatedAt = now
	}
	if mem.UpdatedAt.IsZero() {
		mem.UpdatedAt = now
	}
	if mem.Status == "" {
		mem.Status = MembershipStatusActive
	}

	copied := *mem
	m.memberships[key] = &copied
	return nil
}

func (m *MemoryStore) GetMembership(ctx context.Context, userID, tenantID uuid.UUID) (*Membership, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s:%s", userID, tenantID)
	mem, exists := m.memberships[key]
	if !exists {
		return nil, ErrMembershipNotFound
	}
	copied := *mem
	return &copied, nil
}

func (m *MemoryStore) ListUserMemberships(ctx context.Context, userID uuid.UUID) ([]Membership, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var res []Membership
	for _, mem := range m.memberships {
		if mem.UserID == userID {
			res = append(res, *mem)
		}
	}
	return res, nil
}

func (m *MemoryStore) ListTenantMembers(ctx context.Context, tenantID uuid.UUID) ([]MemberInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var res []MemberInfo
	for _, mem := range m.memberships {
		if mem.TenantID == tenantID {
			u := m.users[mem.UserID]
			email := ""
			name := ""
			if u != nil {
				email = u.Email
				name = u.DisplayName
			}
			res = append(res, MemberInfo{
				ID:          mem.ID,
				UserID:      mem.UserID,
				TenantID:    mem.TenantID,
				Email:       email,
				DisplayName: name,
				Role:        mem.Role,
				Status:      mem.Status,
				CreatedAt:   mem.CreatedAt,
			})
		}
	}
	return res, nil
}

func (m *MemoryStore) UpdateMembership(ctx context.Context, mem *Membership) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s:%s", mem.UserID, mem.TenantID)
	existing, exists := m.memberships[key]
	if !exists {
		return ErrMembershipNotFound
	}
	mem.UpdatedAt = time.Now().UTC()
	existing.Role = mem.Role
	existing.Status = mem.Status
	existing.UpdatedAt = mem.UpdatedAt
	return nil
}

func (m *MemoryStore) DeleteMembership(ctx context.Context, userID, tenantID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s:%s", userID, tenantID)
	if _, exists := m.memberships[key]; !exists {
		return ErrMembershipNotFound
	}
	delete(m.memberships, key)
	return nil
}

func (m *MemoryStore) CountActiveAdmins(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var count int64
	for _, mem := range m.memberships {
		if mem.TenantID == tenantID && mem.Role == auth.RoleCompanyAdmin && mem.Status == MembershipStatusActive {
			count++
		}
	}
	return count, nil
}
