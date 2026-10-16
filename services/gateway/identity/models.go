package identity

import (
	"time"

	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/google/uuid"
)

// UserStatus represents user account status.
type UserStatus string

const (
	UserStatusActive   UserStatus = "active"
	UserStatusDisabled UserStatus = "disabled"
)

// MembershipStatus represents tenant membership status.
type MembershipStatus string

const (
	MembershipStatusActive   MembershipStatus = "active"
	MembershipStatusDisabled MembershipStatus = "disabled"
)

// User represents a persistent human user account.
type User struct {
	ID              uuid.UUID  `json:"id"`
	Email           string     `json:"email"`
	PasswordHash    string     `json:"-"`
	DisplayName     string     `json:"display_name"`
	Status          UserStatus `json:"status"`
	IsSuperAdmin    bool       `json:"is_super_admin"`
	EmailVerifiedAt *time.Time `json:"email_verified_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// SafeUser returns user details without sensitive fields.
type SafeUser struct {
	ID           uuid.UUID  `json:"id"`
	Email        string     `json:"email"`
	DisplayName  string     `json:"display_name"`
	Status       UserStatus `json:"status"`
	IsSuperAdmin bool       `json:"is_super_admin"`
	CreatedAt    time.Time  `json:"created_at"`
}

// ToSafeUser converts a User model into SafeUser.
func (u *User) ToSafeUser() SafeUser {
	return SafeUser{
		ID:           u.ID,
		Email:        u.Email,
		DisplayName:  u.DisplayName,
		Status:       u.Status,
		IsSuperAdmin: u.IsSuperAdmin,
		CreatedAt:    u.CreatedAt,
	}
}

// Tenant represents an organization or tenant entity.
type Tenant struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Membership represents a user's association and role within a tenant.
type Membership struct {
	ID        uuid.UUID        `json:"id"`
	UserID    uuid.UUID        `json:"user_id"`
	TenantID  uuid.UUID        `json:"tenant_id"`
	Role      auth.Role        `json:"role"`
	Status    MembershipStatus `json:"status"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// MemberInfo represents enriched membership details for administrative listings.
type MemberInfo struct {
	ID          uuid.UUID        `json:"id"`
	UserID      uuid.UUID        `json:"user_id"`
	TenantID    uuid.UUID        `json:"tenant_id"`
	Email       string           `json:"email"`
	DisplayName string           `json:"display_name"`
	Role        auth.Role        `json:"role"`
	Status      MembershipStatus `json:"status"`
	CreatedAt   time.Time        `json:"created_at"`
}

// RegisterRequest represents the payload for user registration.
type RegisterRequest struct {
	Email            string `json:"email"`
	Password         string `json:"password"`
	DisplayName      string `json:"display_name"`
	OrganizationName string `json:"organization_name,omitempty"`
	TenantName       string `json:"tenant_name,omitempty"`
}

// RegisterTenantRequest represents the payload to register a tenant and company admin in one go.
type RegisterTenantRequest struct {
	CompanyName      string `json:"company_name"`
	TenantName       string `json:"tenant_name,omitempty"`
	AdminEmail       string `json:"admin_email"`
	Email            string `json:"email,omitempty"`
	Password         string `json:"password"`
	DisplayName      string `json:"display_name"`
}

// RegisterTenantResponse represents the combined tenant + admin registration response.
type RegisterTenantResponse struct {
	Tenant      Tenant     `json:"tenant"`
	User        SafeUser   `json:"user"`
	Membership  Membership `json:"membership"`
	AccessToken string     `json:"access_token"`
	TokenType   string     `json:"token_type"`
	ExpiresIn   int64      `json:"expires_in"`
}

// LoginRequest represents credentials submitted for authentication.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TenantID string `json:"tenant_id,omitempty"`
}

// LoginResponse represents the token and caller profile returned on successful login.
type LoginResponse struct {
	AccessToken    string    `json:"access_token"`
	TokenType      string    `json:"token_type"`
	ExpiresIn      int64     `json:"expires_in"`
	User           SafeUser  `json:"user"`
	ActiveTenantID uuid.UUID `json:"active_tenant_id"`
	Role           auth.Role `json:"role"`
}

// CurrentUserResponse represents profile and active tenant membership information.
type CurrentUserResponse struct {
	User         SafeUser     `json:"user"`
	ActiveTenant *Tenant      `json:"active_tenant,omitempty"`
	Role         auth.Role    `json:"role"`
	Memberships  []Membership `json:"memberships"`
}

// CreateTenantRequest represents parameters to provision a new tenant.
type CreateTenantRequest struct {
	Name string `json:"name"`
}

// AddMemberRequest represents parameters to associate an existing user to a tenant.
type AddMemberRequest struct {
	UserID uuid.UUID `json:"user_id,omitempty"`
	Email  string    `json:"email,omitempty"`
	Role   auth.Role `json:"role"`
}

// UpdateMemberRequest represents updates to a member's role or status.
type UpdateMemberRequest struct {
	Role   auth.Role        `json:"role,omitempty"`
	Status MembershipStatus `json:"status,omitempty"`
}
