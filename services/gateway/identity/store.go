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
	ErrUserNotFound                 = errors.New("user not found")
	ErrUserAlreadyExists            = errors.New("user with this email already exists")
	ErrTenantNotFound               = errors.New("tenant not found")
	ErrMembershipNotFound           = errors.New("tenant membership not found")
	ErrMembershipAlreadyExists      = errors.New("user already has a membership in this tenant")
	ErrCannotRemoveLastAdmin        = errors.New("cannot remove or demote the last active tenant administrator")
	ErrInvalidOrExpiredVerification = errors.New("invalid or expired verification code")
	ErrEmailNotVerified             = errors.New("email address has not been verified")
	ErrInvitationNotFound           = errors.New("invitation not found")
	ErrInvitationExpired            = errors.New("invitation has expired")
	ErrInvitationAccepted           = errors.New("invitation has already been accepted")
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
	ListTenants(ctx context.Context) ([]TenantWithStats, error)
	UpdateTenantStatus(ctx context.Context, tenantID uuid.UUID, status string) (*Tenant, error)

	ListSuperAdmins(ctx context.Context) ([]SafeUser, error)
	CreateOrPromoteSuperAdmin(ctx context.Context, email, passwordHash, displayName string) (*SafeUser, error)
	GetPlatformStats(ctx context.Context) (map[string]int64, error)

	CreateMembership(ctx context.Context, membership *Membership) error
	GetMembership(ctx context.Context, userID, tenantID uuid.UUID) (*Membership, error)
	ListUserMemberships(ctx context.Context, userID uuid.UUID) ([]Membership, error)
	ListTenantMembers(ctx context.Context, tenantID uuid.UUID) ([]MemberInfo, error)
	UpdateMembership(ctx context.Context, membership *Membership) error
	DeleteMembership(ctx context.Context, userID, tenantID uuid.UUID) error
	CountActiveAdmins(ctx context.Context, tenantID uuid.UUID) (int64, error)

	SaveVerification(ctx context.Context, email, otpCode, token string, expiresAt time.Time) error
	VerifyOTPOrToken(ctx context.Context, email, otpCode, token string) (*User, error)
	IsEmailVerified(ctx context.Context, email string) (bool, error)

	CreateInvitation(ctx context.Context, inv *Invitation) error
	GetInvitationByToken(ctx context.Context, token string) (*Invitation, error)
	AcceptInvitation(ctx context.Context, token, fullName, passwordHash string) (*User, *Membership, *Tenant, error)
	DeleteInvitation(ctx context.Context, tenantID, invitationID uuid.UUID) error
	DeleteInvitationByEmail(ctx context.Context, tenantID uuid.UUID, email string) error
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

// EmailVerificationModel maps to the 'email_verifications' table.
type EmailVerificationModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	Email     string    `gorm:"column:email;type:text;not null"`
	OTPCode   string    `gorm:"column:otp_code;type:text;not null"`
	Token     string    `gorm:"column:token;type:text;not null"`
	ExpiresAt time.Time `gorm:"column:expires_at;type:timestamptz;not null"`
	CreatedAt time.Time `gorm:"column:created_at;type:timestamptz;not null"`
}

func (EmailVerificationModel) TableName() string {
	return "email_verifications"
}

// InvitationModel maps to the 'invitations' table.
type InvitationModel struct {
	ID         uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	TenantID   uuid.UUID  `gorm:"column:tenant_id;type:uuid;not null;index"`
	Email      string     `gorm:"column:email;type:text;not null"`
	Role       string     `gorm:"column:role;type:text;not null"`
	Token      string     `gorm:"column:token;type:text;not null;uniqueIndex"`
	InvitedBy  string     `gorm:"column:invited_by;type:text"`
	Status     string     `gorm:"column:status;type:text;not null;default:'pending'"`
	ExpiresAt  time.Time  `gorm:"column:expires_at;type:timestamptz;not null"`
	CreatedAt  time.Time  `gorm:"column:created_at;type:timestamptz;not null"`
	AcceptedAt *time.Time `gorm:"column:accepted_at;type:timestamptz"`
}

func (InvitationModel) TableName() string {
	return "invitations"
}

func (m *InvitationModel) ToDomain() *Invitation {
	return &Invitation{
		ID:         m.ID,
		TenantID:   m.TenantID,
		Email:      m.Email,
		Role:       auth.Role(m.Role),
		Token:      m.Token,
		InvitedBy:  m.InvitedBy,
		Status:     m.Status,
		ExpiresAt:  m.ExpiresAt,
		CreatedAt:  m.CreatedAt,
		AcceptedAt: m.AcceptedAt,
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

func (s *PostgresStore) ListTenants(ctx context.Context) ([]TenantWithStats, error) {
	type row struct {
		ID          uuid.UUID `gorm:"column:id"`
		Name        string    `gorm:"column:name"`
		Status      string    `gorm:"column:status"`
		CreatedAt   time.Time `gorm:"column:created_at"`
		UpdatedAt   time.Time `gorm:"column:updated_at"`
		MemberCount int64     `gorm:"column:member_count"`
		AgentCount  int64     `gorm:"column:agent_count"`
	}
	var rows []row
	query := `
		SELECT 
			t.id, 
			t.name, 
			t.status, 
			t.created_at, 
			t.updated_at,
			COALESCE(m.member_count, 0) AS member_count,
			COALESCE(a.agent_count, 0) AS agent_count
		FROM tenants t
		LEFT JOIN (
			SELECT tenant_id, COUNT(*) AS member_count 
			FROM tenant_memberships 
			GROUP BY tenant_id
		) m ON m.tenant_id = t.id
		LEFT JOIN (
			SELECT tenant_id, COUNT(*) AS agent_count 
			FROM agents 
			GROUP BY tenant_id
		) a ON a.tenant_id = t.id
		ORDER BY t.created_at DESC`
	if err := s.db.WithContext(ctx).Raw(query).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list tenants with stats: %w", err)
	}

	result := make([]TenantWithStats, len(rows))
	for i, r := range rows {
		slug := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(r.Name), " ", "-"))
		result[i] = TenantWithStats{
			ID:          r.ID,
			Name:        r.Name,
			Slug:        slug,
			Tier:        "enterprise",
			Status:      r.Status,
			AgentCount:  r.AgentCount,
			MemberCount: r.MemberCount,
			CreatedAt:   r.CreatedAt,
			UpdatedAt:   r.UpdatedAt,
		}
	}
	return result, nil
}

func (s *PostgresStore) UpdateTenantStatus(ctx context.Context, tenantID uuid.UUID, status string) (*Tenant, error) {
	var model TenantModel
	err := s.db.WithContext(ctx).Where("id = ?", tenantID).First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTenantNotFound
		}
		return nil, fmt.Errorf("failed to query tenant: %w", err)
	}
	model.Status = status
	model.UpdatedAt = time.Now().UTC()
	if err := s.db.WithContext(ctx).Save(&model).Error; err != nil {
		return nil, fmt.Errorf("failed to update tenant status: %w", err)
	}
	return model.ToDomain(), nil
}

func (s *PostgresStore) ListSuperAdmins(ctx context.Context) ([]SafeUser, error) {
	var models []UserModel
	err := s.db.WithContext(ctx).Where("is_super_admin = ?", true).Order("created_at ASC").Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list super admins: %w", err)
	}
	result := make([]SafeUser, len(models))
	for i, m := range models {
		result[i] = m.ToDomain().ToSafeUser()
	}
	return result, nil
}

func (s *PostgresStore) CreateOrPromoteSuperAdmin(ctx context.Context, email, passwordHash, displayName string) (*SafeUser, error) {
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	var model UserModel
	err := s.db.WithContext(ctx).Where("LOWER(email) = ?", normalizedEmail).First(&model).Error
	if err == nil {
		model.IsSuperAdmin = true
		model.Status = string(UserStatusActive)
		if displayName != "" {
			model.DisplayName = displayName
		}
		if passwordHash != "" {
			model.PasswordHash = passwordHash
		}
		model.UpdatedAt = time.Now().UTC()
		if err := s.db.WithContext(ctx).Save(&model).Error; err != nil {
			return nil, fmt.Errorf("failed to promote existing user: %w", err)
		}
		u := model.ToDomain().ToSafeUser()
		return &u, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("failed to query user: %w", err)
	}

	now := time.Now().UTC()
	newUser := UserModel{
		ID:              uuid.New(),
		Email:           normalizedEmail,
		PasswordHash:    passwordHash,
		DisplayName:     displayName,
		Status:          string(UserStatusActive),
		IsSuperAdmin:    true,
		EmailVerifiedAt: &now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.db.WithContext(ctx).Create(&newUser).Error; err != nil {
		return nil, fmt.Errorf("failed to create super admin user: %w", err)
	}
	u := newUser.ToDomain().ToSafeUser()
	return &u, nil
}

func (s *PostgresStore) GetPlatformStats(ctx context.Context) (map[string]int64, error) {
	var activeTenants, totalTenants, totalAgents, totalInvocations, totalReviews int64
	s.db.WithContext(ctx).Table("tenants").Where("status = ?", "active").Count(&activeTenants)
	s.db.WithContext(ctx).Table("tenants").Count(&totalTenants)
	s.db.WithContext(ctx).Table("agents").Count(&totalAgents)
	s.db.WithContext(ctx).Table("tool_invocations").Count(&totalInvocations)
	s.db.WithContext(ctx).Table("review_cases").Count(&totalReviews)
	return map[string]int64{
		"active_tenants":    activeTenants,
		"total_tenants":     totalTenants,
		"total_agents":      totalAgents,
		"total_invocations": totalInvocations,
		"total_reviews":     totalReviews,
	}, nil
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

	var pendingInvs []InvitationModel
	_ = s.db.WithContext(ctx).
		Where("tenant_id = ? AND status = ? AND expires_at > ?", tenantID, "pending", time.Now().UTC()).
		Order("created_at ASC").
		Find(&pendingInvs).Error

	for _, inv := range pendingInvs {
		exists := false
		for _, m := range members {
			if strings.EqualFold(m.Email, inv.Email) {
				exists = true
				break
			}
		}
		if !exists {
			members = append(members, MemberInfo{
				ID:          inv.ID,
				UserID:      uuid.Nil,
				TenantID:    inv.TenantID,
				Email:       inv.Email,
				DisplayName: "Pending invite",
				Role:        auth.Role(inv.Role),
				Status:      MembershipStatus("invited"),
				CreatedAt:   inv.CreatedAt,
			})
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

func (s *PostgresStore) SaveVerification(ctx context.Context, email, otpCode, token string, expiresAt time.Time) error {
	normEmail := strings.ToLower(strings.TrimSpace(email))
	model := EmailVerificationModel{
		ID:        uuid.New(),
		Email:     normEmail,
		OTPCode:   otpCode,
		Token:     token,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now().UTC(),
	}
	return s.db.WithContext(ctx).Create(&model).Error
}

func (s *PostgresStore) VerifyOTPOrToken(ctx context.Context, email, otpCode, token string) (*User, error) {
	normEmail := strings.ToLower(strings.TrimSpace(email))
	token = strings.TrimSpace(token)
	otpCode = strings.TrimSpace(otpCode)
	now := time.Now().UTC()

	var record EmailVerificationModel
	query := s.db.WithContext(ctx).Where("expires_at > ?", now)

	if token != "" {
		query = query.Where("token = ?", token)
	} else if normEmail != "" && otpCode != "" {
		query = query.Where("email = ? AND otp_code = ?", normEmail, otpCode)
	} else {
		return nil, ErrInvalidOrExpiredVerification
	}

	if err := query.Order("created_at DESC").First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidOrExpiredVerification
		}
		return nil, fmt.Errorf("failed to query verification: %w", err)
	}

	// Fetch user and mark verified
	var user UserModel
	if err := s.db.WithContext(ctx).Where("LOWER(TRIM(email)) = ?", record.Email).First(&user).Error; err != nil {
		return nil, ErrUserNotFound
	}

	user.EmailVerifiedAt = &now
	user.UpdatedAt = now
	if err := s.db.WithContext(ctx).Save(&user).Error; err != nil {
		return nil, fmt.Errorf("failed to update user email verification: %w", err)
	}

	// Delete used verification records for this email
	_ = s.db.WithContext(ctx).Where("LOWER(TRIM(email)) = ?", record.Email).Delete(&EmailVerificationModel{}).Error

	return user.ToDomain(), nil
}

func (s *PostgresStore) IsEmailVerified(ctx context.Context, email string) (bool, error) {
	normEmail := strings.ToLower(strings.TrimSpace(email))
	var user UserModel
	if err := s.db.WithContext(ctx).Where("LOWER(TRIM(email)) = ?", normEmail).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, ErrUserNotFound
		}
		return false, err
	}
	return user.EmailVerifiedAt != nil, nil
}

func (s *PostgresStore) CreateInvitation(ctx context.Context, inv *Invitation) error {
	if inv.ID == uuid.Nil {
		inv.ID = uuid.New()
	}
	now := time.Now().UTC()
	if inv.CreatedAt.IsZero() {
		inv.CreatedAt = now
	}
	if inv.ExpiresAt.IsZero() {
		inv.ExpiresAt = now.Add(7 * 24 * time.Hour)
	}
	if inv.Status == "" {
		inv.Status = "pending"
	}

	model := InvitationModel{
		ID:        inv.ID,
		TenantID:  inv.TenantID,
		Email:     strings.ToLower(strings.TrimSpace(inv.Email)),
		Role:      string(inv.Role),
		Token:     inv.Token,
		InvitedBy: inv.InvitedBy,
		Status:    inv.Status,
		ExpiresAt: inv.ExpiresAt,
		CreatedAt: inv.CreatedAt,
	}

	return s.db.WithContext(ctx).Create(&model).Error
}

func (s *PostgresStore) GetInvitationByToken(ctx context.Context, token string) (*Invitation, error) {
	var model InvitationModel
	err := s.db.WithContext(ctx).Where("token = ?", strings.TrimSpace(token)).First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvitationNotFound
		}
		return nil, fmt.Errorf("failed to get invitation by token: %w", err)
	}
	return model.ToDomain(), nil
}

func (s *PostgresStore) AcceptInvitation(ctx context.Context, token, fullName, passwordHash string) (*User, *Membership, *Tenant, error) {
	var user *User
	var membership *Membership
	var tenant *Tenant

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var inv InvitationModel
		if err := tx.Where("token = ?", strings.TrimSpace(token)).First(&inv).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvitationNotFound
			}
			return fmt.Errorf("failed to query invitation: %w", err)
		}

		if inv.Status == "accepted" {
			return ErrInvitationAccepted
		}

		now := time.Now().UTC()
		if inv.ExpiresAt.Before(now) {
			return ErrInvitationExpired
		}

		// Retrieve tenant
		var tModel TenantModel
		if err := tx.Where("id = ?", inv.TenantID).First(&tModel).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTenantNotFound
			}
			return fmt.Errorf("failed to find tenant: %w", err)
		}
		tenant = tModel.ToDomain()

		normEmail := strings.ToLower(strings.TrimSpace(inv.Email))

		// Check if user already exists
		var uModel UserModel
		userErr := tx.Where("LOWER(TRIM(email)) = ?", normEmail).First(&uModel).Error
		if userErr != nil && !errors.Is(userErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("failed to query user by email: %w", userErr)
		}

		if errors.Is(userErr, gorm.ErrRecordNotFound) {
			// Create user
			uModel = UserModel{
				ID:              uuid.New(),
				Email:           normEmail,
				DisplayName:     strings.TrimSpace(fullName),
				PasswordHash:    passwordHash,
				Status:          string(UserStatusActive),
				IsSuperAdmin:    false,
				EmailVerifiedAt: &now,
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			if err := tx.Create(&uModel).Error; err != nil {
				return fmt.Errorf("failed to create invited user: %w", err)
			}
		} else {
			// User exists, update password and mark email verified
			uModel.PasswordHash = passwordHash
			if strings.TrimSpace(fullName) != "" {
				uModel.DisplayName = strings.TrimSpace(fullName)
			}
			if uModel.EmailVerifiedAt == nil {
				uModel.EmailVerifiedAt = &now
			}
			uModel.UpdatedAt = now
			if err := tx.Save(&uModel).Error; err != nil {
				return fmt.Errorf("failed to update user: %w", err)
			}
		}
		user = uModel.ToDomain()

		// Upsert membership
		var mModel MembershipModel
		memErr := tx.Where("user_id = ? AND tenant_id = ?", user.ID, inv.TenantID).First(&mModel).Error
		if memErr != nil && !errors.Is(memErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("failed to query membership: %w", memErr)
		}

		if errors.Is(memErr, gorm.ErrRecordNotFound) {
			mModel = MembershipModel{
				ID:        uuid.New(),
				UserID:    user.ID,
				TenantID:  inv.TenantID,
				Role:      inv.Role,
				Status:    string(MembershipStatusActive),
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := tx.Create(&mModel).Error; err != nil {
				return fmt.Errorf("failed to create tenant membership: %w", err)
			}
		} else {
			mModel.Role = inv.Role
			mModel.Status = string(MembershipStatusActive)
			mModel.UpdatedAt = now
			if err := tx.Save(&mModel).Error; err != nil {
				return fmt.Errorf("failed to update membership: %w", err)
			}
		}
		membership = mModel.ToDomain()

		// Mark invitation accepted
		inv.Status = "accepted"
		inv.AcceptedAt = &now
		if err := tx.Save(&inv).Error; err != nil {
			return fmt.Errorf("failed to update invitation: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, nil, nil, err
	}
	return user, membership, tenant, nil
}

func (s *PostgresStore) DeleteInvitation(ctx context.Context, tenantID, invitationID uuid.UUID) error {
	res := s.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ?", invitationID, tenantID).
		Delete(&InvitationModel{})
	if res.Error != nil {
		return fmt.Errorf("failed to delete invitation: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrInvitationNotFound
	}
	return nil
}

func (s *PostgresStore) DeleteInvitationByEmail(ctx context.Context, tenantID uuid.UUID, email string) error {
	normEmail := strings.ToLower(strings.TrimSpace(email))
	res := s.db.WithContext(ctx).
		Where("LOWER(TRIM(email)) = ? AND tenant_id = ?", normEmail, tenantID).
		Delete(&InvitationModel{})
	if res.Error != nil {
		return fmt.Errorf("failed to delete invitation by email: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrInvitationNotFound
	}
	return nil
}

// MemoryStore provides a thread-safe in-memory Store implementation for testing.
type MemoryStore struct {
	mu            sync.RWMutex
	users         map[uuid.UUID]*User
	usersByMail   map[string]uuid.UUID
	tenants       map[uuid.UUID]*Tenant
	memberships   map[string]*Membership // key: userID:tenantID
	verifications map[string]*EmailVerificationModel
	invitations   map[string]*InvitationModel
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		users:         make(map[uuid.UUID]*User),
		usersByMail:   make(map[string]uuid.UUID),
		tenants:       make(map[uuid.UUID]*Tenant),
		memberships:   make(map[string]*Membership),
		verifications: make(map[string]*EmailVerificationModel),
		invitations:   make(map[string]*InvitationModel),
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

func (m *MemoryStore) ListTenants(ctx context.Context) ([]TenantWithStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]TenantWithStats, 0, len(m.tenants))
	for _, t := range m.tenants {
		var memberCount int64
		for _, mem := range m.memberships {
			if mem.TenantID == t.ID {
				memberCount++
			}
		}
		slug := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(t.Name), " ", "-"))
		result = append(result, TenantWithStats{
			ID:          t.ID,
			Name:        t.Name,
			Slug:        slug,
			Tier:        "enterprise",
			Status:      t.Status,
			AgentCount:  0,
			MemberCount: memberCount,
			CreatedAt:   t.CreatedAt,
			UpdatedAt:   t.UpdatedAt,
		})
	}
	return result, nil
}

func (m *MemoryStore) UpdateTenantStatus(ctx context.Context, tenantID uuid.UUID, status string) (*Tenant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tenants[tenantID]
	if !ok {
		return nil, ErrTenantNotFound
	}
	t.Status = status
	t.UpdatedAt = time.Now().UTC()
	m.tenants[tenantID] = t
	cp := *t
	return &cp, nil
}

func (m *MemoryStore) ListSuperAdmins(ctx context.Context) ([]SafeUser, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []SafeUser
	for _, u := range m.users {
		if u.IsSuperAdmin {
			result = append(result, u.ToSafeUser())
		}
	}
	return result, nil
}

func (m *MemoryStore) CreateOrPromoteSuperAdmin(ctx context.Context, email, passwordHash, displayName string) (*SafeUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	normalized := strings.ToLower(strings.TrimSpace(email))
	for id, u := range m.users {
		if strings.EqualFold(u.Email, normalized) {
			u.IsSuperAdmin = true
			u.Status = UserStatusActive
			if displayName != "" {
				u.DisplayName = displayName
			}
			if passwordHash != "" {
				u.PasswordHash = passwordHash
			}
			u.UpdatedAt = time.Now().UTC()
			m.users[id] = u
			safe := u.ToSafeUser()
			return &safe, nil
		}
	}

	now := time.Now().UTC()
	newUser := &User{
		ID:              uuid.New(),
		Email:           normalized,
		PasswordHash:    passwordHash,
		DisplayName:     displayName,
		Status:          UserStatusActive,
		IsSuperAdmin:    true,
		EmailVerifiedAt: &now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	m.users[newUser.ID] = newUser
	safe := newUser.ToSafeUser()
	return &safe, nil
}

func (m *MemoryStore) GetPlatformStats(ctx context.Context) (map[string]int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var activeTenants int64
	for _, t := range m.tenants {
		if t.Status == "active" {
			activeTenants++
		}
	}
	return map[string]int64{
		"active_tenants":    activeTenants,
		"total_tenants":     int64(len(m.tenants)),
		"total_agents":      0,
		"total_invocations": 0,
		"total_reviews":     0,
	}, nil
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

	for _, inv := range m.invitations {
		if inv.TenantID == tenantID && inv.Status == "pending" && inv.ExpiresAt.After(time.Now().UTC()) {
			exists := false
			for _, item := range res {
				if strings.EqualFold(item.Email, inv.Email) {
					exists = true
					break
				}
			}
			if !exists {
				res = append(res, MemberInfo{
					ID:          inv.ID,
					UserID:      uuid.Nil,
					TenantID:    inv.TenantID,
					Email:       inv.Email,
					DisplayName: "Pending invite",
					Role:        auth.Role(inv.Role),
					Status:      MembershipStatus("invited"),
					CreatedAt:   inv.CreatedAt,
				})
			}
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

func (m *MemoryStore) SaveVerification(ctx context.Context, email, otpCode, token string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	normEmail := strings.ToLower(strings.TrimSpace(email))
	model := &EmailVerificationModel{
		ID:        uuid.New(),
		Email:     normEmail,
		OTPCode:   otpCode,
		Token:     token,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now().UTC(),
	}
	m.verifications[model.ID.String()] = model
	return nil
}

func (m *MemoryStore) VerifyOTPOrToken(ctx context.Context, email, otpCode, token string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	normEmail := strings.ToLower(strings.TrimSpace(email))
	token = strings.TrimSpace(token)
	otpCode = strings.TrimSpace(otpCode)
	now := time.Now().UTC()

	var matched *EmailVerificationModel

	for _, v := range m.verifications {
		if v.ExpiresAt.Before(now) {
			continue
		}
		if token != "" && v.Token == token {
			if matched == nil || v.CreatedAt.After(matched.CreatedAt) {
				matched = v
			}
		} else if token == "" && normEmail != "" && otpCode != "" && v.Email == normEmail && v.OTPCode == otpCode {
			if matched == nil || v.CreatedAt.After(matched.CreatedAt) {
				matched = v
			}
		}
	}

	if matched == nil {
		return nil, ErrInvalidOrExpiredVerification
	}

	userID, exists := m.usersByMail[matched.Email]
	if !exists {
		return nil, ErrUserNotFound
	}

	user := m.users[userID]
	user.EmailVerifiedAt = &now
	user.UpdatedAt = now

	// Delete used verification records for this email
	for k, v := range m.verifications {
		if v.Email == matched.Email {
			delete(m.verifications, k)
		}
	}

	copied := *user
	return &copied, nil
}

func (m *MemoryStore) IsEmailVerified(ctx context.Context, email string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	normEmail := strings.ToLower(strings.TrimSpace(email))
	userID, exists := m.usersByMail[normEmail]
	if !exists {
		return false, ErrUserNotFound
	}
	u := m.users[userID]
	return u.EmailVerifiedAt != nil, nil
}

func (m *MemoryStore) CreateInvitation(ctx context.Context, inv *Invitation) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if inv.ID == uuid.Nil {
		inv.ID = uuid.New()
	}
	now := time.Now().UTC()
	if inv.CreatedAt.IsZero() {
		inv.CreatedAt = now
	}
	if inv.ExpiresAt.IsZero() {
		inv.ExpiresAt = now.Add(7 * 24 * time.Hour)
	}
	if inv.Status == "" {
		inv.Status = "pending"
	}

	model := &InvitationModel{
		ID:        inv.ID,
		TenantID:  inv.TenantID,
		Email:     strings.ToLower(strings.TrimSpace(inv.Email)),
		Role:      string(inv.Role),
		Token:     inv.Token,
		InvitedBy: inv.InvitedBy,
		Status:    inv.Status,
		ExpiresAt: inv.ExpiresAt,
		CreatedAt: inv.CreatedAt,
	}

	m.invitations[inv.Token] = model
	return nil
}

func (m *MemoryStore) GetInvitationByToken(ctx context.Context, token string) (*Invitation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	inv, exists := m.invitations[strings.TrimSpace(token)]
	if !exists {
		return nil, ErrInvitationNotFound
	}
	return inv.ToDomain(), nil
}

func (m *MemoryStore) AcceptInvitation(ctx context.Context, token, fullName, passwordHash string) (*User, *Membership, *Tenant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	inv, exists := m.invitations[strings.TrimSpace(token)]
	if !exists {
		return nil, nil, nil, ErrInvitationNotFound
	}

	if inv.Status == "accepted" {
		return nil, nil, nil, ErrInvitationAccepted
	}

	now := time.Now().UTC()
	if inv.ExpiresAt.Before(now) {
		return nil, nil, nil, ErrInvitationExpired
	}

	tenant, exists := m.tenants[inv.TenantID]
	if !exists {
		return nil, nil, nil, ErrTenantNotFound
	}

	normEmail := strings.ToLower(strings.TrimSpace(inv.Email))
	userID, exists := m.usersByMail[normEmail]
	var user *User
	if !exists {
		user = &User{
			ID:              uuid.New(),
			Email:           normEmail,
			DisplayName:     strings.TrimSpace(fullName),
			PasswordHash:    passwordHash,
			Status:          UserStatusActive,
			IsSuperAdmin:    false,
			EmailVerifiedAt: &now,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		uCopy := *user
		m.users[user.ID] = &uCopy
		m.usersByMail[normEmail] = user.ID
	} else {
		user = m.users[userID]
		user.PasswordHash = passwordHash
		if strings.TrimSpace(fullName) != "" {
			user.DisplayName = strings.TrimSpace(fullName)
		}
		if user.EmailVerifiedAt == nil {
			user.EmailVerifiedAt = &now
		}
		user.UpdatedAt = now
	}

	memKey := fmt.Sprintf("%s:%s", user.ID, inv.TenantID)
	membership, memExists := m.memberships[memKey]
	if !memExists {
		membership = &Membership{
			ID:        uuid.New(),
			UserID:    user.ID,
			TenantID:  inv.TenantID,
			Role:      auth.Role(inv.Role),
			Status:    MembershipStatusActive,
			CreatedAt: now,
			UpdatedAt: now,
		}
		memCopy := *membership
		m.memberships[memKey] = &memCopy
	} else {
		membership.Role = auth.Role(inv.Role)
		membership.Status = MembershipStatusActive
		membership.UpdatedAt = now
	}

	inv.Status = "accepted"
	inv.AcceptedAt = &now

	uRes := *user
	mRes := *membership
	tRes := *tenant
	return &uRes, &mRes, &tRes, nil
}

func (m *MemoryStore) DeleteInvitation(ctx context.Context, tenantID, invitationID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for token, inv := range m.invitations {
		if inv.ID == invitationID && inv.TenantID == tenantID {
			delete(m.invitations, token)
			return nil
		}
	}
	return ErrInvitationNotFound
}

func (m *MemoryStore) DeleteInvitationByEmail(ctx context.Context, tenantID uuid.UUID, email string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	normEmail := strings.ToLower(strings.TrimSpace(email))
	for token, inv := range m.invitations {
		if strings.EqualFold(inv.Email, normEmail) && inv.TenantID == tenantID {
			delete(m.invitations, token)
			return nil
		}
	}
	return ErrInvitationNotFound
}
