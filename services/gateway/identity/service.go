package identity

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials     = errors.New("invalid email or password")
	ErrAccountDisabled        = errors.New("user account is disabled")
	ErrNoActiveMembership     = errors.New("user has no active tenant memberships")
	ErrUnauthorizedTenant     = errors.New("unauthorized or inactive membership for requested tenant")
	ErrInvalidEmail           = errors.New("invalid email address format")
	ErrWeakPassword           = errors.New("password must be between 8 and 128 characters")
	ErrPermissionDenied       = errors.New("permission denied")
	ErrCannotAssignSuperAdmin = errors.New("cannot assign super_admin as a tenant membership role")
	ErrInvalidRole            = errors.New("invalid membership role")
)

// TokenIssuer defines the contract for issuing access JWT tokens.
type TokenIssuer interface {
	IssueToken(id auth.Identity, ttl time.Duration) (string, error)
}

// Service provides identity, authentication, and tenant membership operations.
type Service struct {
	store       Store
	tokenIssuer TokenIssuer
	tokenTTL    time.Duration
}

// NewService creates a new identity Service.
func NewService(store Store, tokenIssuer TokenIssuer) *Service {
	return &Service{
		store:       store,
		tokenIssuer: tokenIssuer,
		tokenTTL:    1 * time.Hour,
	}
}

// SetTokenTTL configures the access token lifetime.
func (s *Service) SetTokenTTL(ttl time.Duration) {
	if ttl > 0 {
		s.tokenTTL = ttl
	}
}

// Register creates a new user account with a secure password hash.
func (s *Service) Register(ctx context.Context, req RegisterRequest) (*SafeUser, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if err := validateEmail(email); err != nil {
		return nil, err
	}

	if len(req.Password) < 8 || len(req.Password) > 128 {
		return nil, ErrWeakPassword
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	user := &User{
		ID:           uuid.New(),
		Email:        email,
		PasswordHash: string(hash),
		DisplayName:  strings.TrimSpace(req.DisplayName),
		Status:       UserStatusActive,
		IsSuperAdmin: false,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}

	if err := s.store.CreateUser(ctx, user); err != nil {
		return nil, err
	}

	orgName := strings.TrimSpace(req.TenantName)
	if orgName == "" {
		orgName = strings.TrimSpace(req.OrganizationName)
	}
	if orgName != "" {
		tenant := &Tenant{
			ID:        uuid.New(),
			Name:      orgName,
			Status:    "active",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		if _, _, err := s.store.CreateTenantWithAdmin(ctx, tenant, user.ID); err != nil {
			return nil, fmt.Errorf("failed to provision organization: %w", err)
		}
	}

	safe := user.ToSafeUser()
	return &safe, nil
}

// RegisterTenant provisions a company tenant, creates the administrator user, and establishes company_admin membership in one go.
func (s *Service) RegisterTenant(ctx context.Context, req RegisterTenantRequest) (*RegisterTenantResponse, error) {
	companyName := strings.TrimSpace(req.CompanyName)
	if companyName == "" {
		companyName = strings.TrimSpace(req.TenantName)
	}
	if companyName == "" {
		return nil, errors.New("company_name is required")
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		email = strings.ToLower(strings.TrimSpace(req.AdminEmail))
	}
	if err := validateEmail(email); err != nil {
		return nil, err
	}

	if len(req.Password) < 8 || len(req.Password) > 128 {
		return nil, ErrWeakPassword
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	now := time.Now().UTC()
	user := &User{
		ID:           uuid.New(),
		Email:        email,
		PasswordHash: string(hash),
		DisplayName:  strings.TrimSpace(req.DisplayName),
		Status:       UserStatusActive,
		IsSuperAdmin: false,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	tenant := &Tenant{
		ID:        uuid.New(),
		Name:      companyName,
		Status:    "active",
		CreatedAt: now,
		UpdatedAt: now,
	}

	createdTenant, createdUser, createdMem, err := s.store.RegisterTenantWithAdmin(ctx, tenant, user)
	if err != nil {
		return nil, err
	}

	identity := auth.Identity{
		UserID:   createdUser.ID.String(),
		TenantID: createdTenant.ID,
		Role:     auth.RoleCompanyAdmin,
	}

	tokenStr, err := s.tokenIssuer.IssueToken(identity, s.tokenTTL)
	if err != nil {
		return nil, fmt.Errorf("failed to issue initial access token: %w", err)
	}

	return &RegisterTenantResponse{
		Tenant:      *createdTenant,
		User:        createdUser.ToSafeUser(),
		Membership:  *createdMem,
		AccessToken: tokenStr,
		TokenType:   "Bearer",
		ExpiresIn:   int64(s.tokenTTL.Seconds()),
	}, nil
}

// Login authenticates a user by email and password, resolves active tenant membership, and issues an access JWT.
func (s *Service) Login(ctx context.Context, req LoginRequest) (*LoginResponse, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" {
		return nil, ErrInvalidCredentials
	}

	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("error querying user: %w", err)
	}

	if user.Status != UserStatusActive {
		return nil, ErrAccountDisabled
	}

	// Verify password hash
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	// Resolve tenant and role
	var activeTenantID uuid.UUID
	var resolvedRole auth.Role

	var reqTenantUUID uuid.UUID
	if strings.TrimSpace(req.TenantID) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(req.TenantID))
		if err != nil || parsed == uuid.Nil {
			return nil, fmt.Errorf("invalid requested tenant_id: %w", err)
		}
		reqTenantUUID = parsed
	}

	if user.IsSuperAdmin {
		resolvedRole = auth.RoleSuperAdmin
		if reqTenantUUID != uuid.Nil {
			// Verify tenant exists
			_, err := s.store.GetTenantByID(ctx, reqTenantUUID)
			if err != nil {
				return nil, fmt.Errorf("requested tenant not found: %w", err)
			}
			activeTenantID = reqTenantUUID
		} else {
			// Find first membership or default tenant
			memberships, _ := s.store.ListUserMemberships(ctx, user.ID)
			if len(memberships) > 0 {
				activeTenantID = memberships[0].TenantID
			} else {
				return nil, errors.New("super_admin must supply a tenant_id or have at least one tenant membership to scope API operations")
			}
		}
	} else {
		// Non-super_admin: membership required
		memberships, err := s.store.ListUserMemberships(ctx, user.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve memberships: %w", err)
		}

		var activeMemberships []Membership
		for _, m := range memberships {
			if m.Status == MembershipStatusActive {
				activeMemberships = append(activeMemberships, m)
			}
		}

		if len(activeMemberships) == 0 {
			return nil, ErrNoActiveMembership
		}

		if reqTenantUUID != uuid.Nil {
			var match *Membership
			for _, m := range activeMemberships {
				if m.TenantID == reqTenantUUID {
					match = &m
					break
				}
			}
			if match == nil {
				return nil, ErrUnauthorizedTenant
			}
			activeTenantID = match.TenantID
			resolvedRole = match.Role
		} else {
			// Default to first active membership
			activeTenantID = activeMemberships[0].TenantID
			resolvedRole = activeMemberships[0].Role
		}
	}

	identity := auth.Identity{
		UserID:   user.ID.String(),
		TenantID: activeTenantID,
		Role:     resolvedRole,
	}

	tokenStr, err := s.tokenIssuer.IssueToken(identity, s.tokenTTL)
	if err != nil {
		return nil, fmt.Errorf("failed to issue access token: %w", err)
	}

	return &LoginResponse{
		AccessToken:    tokenStr,
		TokenType:      "Bearer",
		ExpiresIn:      int64(s.tokenTTL.Seconds()),
		User:           user.ToSafeUser(),
		ActiveTenantID: activeTenantID,
		Role:           resolvedRole,
	}, nil
}

// GetCurrentUser returns full profile and active tenant membership details.
func (s *Service) GetCurrentUser(ctx context.Context, caller auth.Identity) (*CurrentUserResponse, error) {
	userUUID, err := uuid.Parse(caller.UserID)
	if err != nil {
		return nil, fmt.Errorf("invalid caller user id: %w", err)
	}

	user, err := s.store.GetUserByID(ctx, userUUID)
	if err != nil {
		return nil, err
	}

	memberships, err := s.store.ListUserMemberships(ctx, userUUID)
	if err != nil {
		return nil, err
	}

	var activeTenant *Tenant
	if caller.TenantID != uuid.Nil {
		t, err := s.store.GetTenantByID(ctx, caller.TenantID)
		if err == nil {
			activeTenant = t
		}
	}

	return &CurrentUserResponse{
		User:         user.ToSafeUser(),
		ActiveTenant: activeTenant,
		Role:         caller.Role,
		Memberships:  memberships,
	}, nil
}

// CreateTenant provisions a new tenant and sets the caller as the initial company_admin.
func (s *Service) CreateTenant(ctx context.Context, caller auth.Identity, req CreateTenantRequest) (*Tenant, *Membership, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, nil, errors.New("tenant name is required")
	}

	userUUID, err := uuid.Parse(caller.UserID)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid caller user id: %w", err)
	}

	// Verify caller user account exists
	_, err = s.store.GetUserByID(ctx, userUUID)
	if err != nil {
		return nil, nil, fmt.Errorf("caller user not found: %w", err)
	}

	tenant := &Tenant{
		ID:        uuid.New(),
		Name:      name,
		Status:    "active",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	return s.store.CreateTenantWithAdmin(ctx, tenant, userUUID)
}

// AddTenantMember associates an existing user to a tenant with a designated role.
func (s *Service) AddTenantMember(ctx context.Context, caller auth.Identity, tenantID uuid.UUID, req AddMemberRequest) (*Membership, error) {
	if err := s.checkTenantAdminAuth(caller, tenantID); err != nil {
		return nil, err
	}

	if req.Role == auth.RoleSuperAdmin {
		return nil, ErrCannotAssignSuperAdmin
	}
	if _, ok := auth.ValidRoles[req.Role]; !ok || req.Role == "" {
		return nil, ErrInvalidRole
	}

	// Resolve user by UserID or Email
	var targetUserID uuid.UUID
	if req.UserID != uuid.Nil {
		targetUserID = req.UserID
	} else if strings.TrimSpace(req.Email) != "" {
		u, err := s.store.GetUserByEmail(ctx, req.Email)
		if err != nil {
			return nil, fmt.Errorf("user with email %q not found: %w", req.Email, err)
		}
		targetUserID = u.ID
	} else {
		return nil, errors.New("user_id or email is required")
	}

	// Verify target user exists
	targetUser, err := s.store.GetUserByID(ctx, targetUserID)
	if err != nil {
		return nil, err
	}
	if targetUser.Status != UserStatusActive {
		return nil, errors.New("cannot add a disabled user account to a tenant")
	}

	// Verify tenant exists
	_, err = s.store.GetTenantByID(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	membership := &Membership{
		ID:        uuid.New(),
		UserID:    targetUserID,
		TenantID:  tenantID,
		Role:      req.Role,
		Status:    MembershipStatusActive,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := s.store.CreateMembership(ctx, membership); err != nil {
		return nil, err
	}
	return membership, nil
}

// ListTenantMembers lists all members belonging to a tenant.
func (s *Service) ListTenantMembers(ctx context.Context, caller auth.Identity, tenantID uuid.UUID) ([]MemberInfo, error) {
	// Must belong to the tenant or be super_admin
	if caller.Role != auth.RoleSuperAdmin && caller.TenantID != tenantID {
		return nil, ErrPermissionDenied
	}
	return s.store.ListTenantMembers(ctx, tenantID)
}

// GetTenantMember retrieves member info for a specific user in a tenant.
func (s *Service) GetTenantMember(ctx context.Context, caller auth.Identity, tenantID, targetUserID uuid.UUID) (*MemberInfo, error) {
	if caller.Role != auth.RoleSuperAdmin && caller.TenantID != tenantID {
		return nil, ErrPermissionDenied
	}
	mem, err := s.store.GetMembership(ctx, targetUserID, tenantID)
	if err != nil {
		return nil, err
	}
	u, err := s.store.GetUserByID(ctx, targetUserID)
	if err != nil {
		return nil, err
	}
	return &MemberInfo{
		ID:          mem.ID,
		UserID:      mem.UserID,
		TenantID:    mem.TenantID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Role:        mem.Role,
		Status:      mem.Status,
		CreatedAt:   mem.CreatedAt,
	}, nil
}

// UpdateTenantMember updates a member's role or status.
func (s *Service) UpdateTenantMember(ctx context.Context, caller auth.Identity, tenantID, targetUserID uuid.UUID, req UpdateMemberRequest) (*Membership, error) {
	if err := s.checkTenantAdminAuth(caller, tenantID); err != nil {
		return nil, err
	}

	if req.Role == auth.RoleSuperAdmin {
		return nil, ErrCannotAssignSuperAdmin
	}

	membership, err := s.store.GetMembership(ctx, targetUserID, tenantID)
	if err != nil {
		return nil, err
	}

	// Check if this demotes or deactivates the last active admin
	willDemote := req.Role != "" && req.Role != auth.RoleCompanyAdmin && membership.Role == auth.RoleCompanyAdmin
	willDeactivate := req.Status == MembershipStatusDisabled && membership.Role == auth.RoleCompanyAdmin && membership.Status == MembershipStatusActive

	if willDemote || willDeactivate {
		count, err := s.store.CountActiveAdmins(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		if count <= 1 {
			return nil, ErrCannotRemoveLastAdmin
		}
	}

	if req.Role != "" {
		if _, ok := auth.ValidRoles[req.Role]; !ok {
			return nil, ErrInvalidRole
		}
		membership.Role = req.Role
	}
	if req.Status != "" {
		if req.Status != MembershipStatusActive && req.Status != MembershipStatusDisabled {
			return nil, errors.New("invalid membership status")
		}
		membership.Status = req.Status
	}

	if err := s.store.UpdateMembership(ctx, membership); err != nil {
		return nil, err
	}
	return membership, nil
}

// DeleteTenantMember removes a user's membership from a tenant.
func (s *Service) DeleteTenantMember(ctx context.Context, caller auth.Identity, tenantID, targetUserID uuid.UUID) error {
	if err := s.checkTenantAdminAuth(caller, tenantID); err != nil {
		return err
	}

	membership, err := s.store.GetMembership(ctx, targetUserID, tenantID)
	if err != nil {
		return err
	}

	// Check if removing the last active company_admin
	if membership.Role == auth.RoleCompanyAdmin && membership.Status == MembershipStatusActive {
		count, err := s.store.CountActiveAdmins(ctx, tenantID)
		if err != nil {
			return err
		}
		if count <= 1 {
			return ErrCannotRemoveLastAdmin
		}
	}

	return s.store.DeleteMembership(ctx, targetUserID, tenantID)
}

func (s *Service) checkTenantAdminAuth(caller auth.Identity, tenantID uuid.UUID) error {
	if caller.Role == auth.RoleSuperAdmin {
		return nil
	}
	if caller.Role == auth.RoleCompanyAdmin && caller.TenantID == tenantID {
		return nil
	}
	return ErrPermissionDenied
}

func validateEmail(email string) error {
	if email == "" {
		return ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return ErrInvalidEmail
	}
	parts := strings.Split(email, "@")
	if len(parts) != 2 || !strings.Contains(parts[1], ".") {
		return ErrInvalidEmail
	}
	return nil
}
