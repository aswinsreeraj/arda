package identity

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
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


// EmailSender defines the interface for delivering verification and notification emails.
type EmailSender interface {
	SendInvitationEmail(to, inviterName, orgName, inviteLink string) error
	SendVerificationOTPEmail(to, otpCode, confirmLink string) error
	AppURL() string
}

// TokenIssuer defines the contract for issuing access JWT tokens.
type TokenIssuer interface {
	IssueToken(id auth.Identity, ttl time.Duration) (string, error)
}

// Service provides identity, authentication, and tenant membership operations.
type Service struct {
	store               Store
	tokenIssuer         TokenIssuer
	tokenTTL            time.Duration
	requireVerification bool
	mailer              EmailSender
}

// NewService creates a new identity Service.
func NewService(store Store, tokenIssuer TokenIssuer) *Service {
	return &Service{
		store:       store,
		tokenIssuer: tokenIssuer,
		tokenTTL:    1 * time.Hour,
	}
}

// SetRequireVerification configures whether user signup requires OTP/email verification.
func (s *Service) SetRequireVerification(req bool) {
	s.requireVerification = req
}

// SetMailer sets the email delivery client for verification and invitations.
func (s *Service) SetMailer(m EmailSender) {
	s.mailer = m
}

// SetTokenTTL configures the access token lifetime.
func (s *Service) SetTokenTTL(ttl time.Duration) {
	if ttl > 0 {
		s.tokenTTL = ttl
	}
}

func generateOTP() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	num := binary.BigEndian.Uint32(b[:]) % 1000000
	return fmt.Sprintf("%06d", num), nil
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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

	if s.requireVerification {
		otp, err := generateOTP()
		if err != nil {
			return nil, fmt.Errorf("failed to generate verification OTP: %w", err)
		}
		token, err := generateToken()
		if err != nil {
			return nil, fmt.Errorf("failed to generate verification token: %w", err)
		}

		expiresAt := time.Now().UTC().Add(24 * time.Hour)
		if err := s.store.SaveVerification(ctx, user.Email, otp, token, expiresAt); err != nil {
			return nil, fmt.Errorf("failed to save email verification: %w", err)
		}

		if s.mailer != nil {
			appURL := "http://localhost:5174"
			if s.mailer.AppURL() != "" {
				appURL = s.mailer.AppURL()
			}
			confirmLink := fmt.Sprintf("%s/verify?token=%s&email=%s", appURL, token, url.QueryEscape(user.Email))
			go func() {
				_ = s.mailer.SendVerificationOTPEmail(user.Email, otp, confirmLink)
			}()
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

	password := req.Password
	if password == "" {
		password = req.AdminPassword
	}
	if len(password) < 8 || len(password) > 128 {
		return nil, ErrWeakPassword
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
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

	if s.requireVerification {
		otp, err := generateOTP()
		if err != nil {
			return nil, fmt.Errorf("failed to generate verification OTP: %w", err)
		}
		token, err := generateToken()
		if err != nil {
			return nil, fmt.Errorf("failed to generate verification token: %w", err)
		}

		expiresAt := time.Now().UTC().Add(24 * time.Hour)
		if err := s.store.SaveVerification(ctx, user.Email, otp, token, expiresAt); err != nil {
			return nil, fmt.Errorf("failed to save email verification: %w", err)
		}

		if s.mailer != nil {
			appURL := "http://localhost:5174"
			if s.mailer.AppURL() != "" {
				appURL = s.mailer.AppURL()
			}
			confirmLink := fmt.Sprintf("%s/verify?token=%s&email=%s", appURL, token, url.QueryEscape(user.Email))
			go func() {
				_ = s.mailer.SendVerificationOTPEmail(user.Email, otp, confirmLink)
			}()
		}

		return &RegisterTenantResponse{
			Tenant:               *createdTenant,
			User:                 createdUser.ToSafeUser(),
			Membership:           *createdMem,
			RequiresVerification: true,
			Message:              "Verification code sent to your email. Please verify before signing in.",
		}, nil
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

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	if s.requireVerification && !user.IsSuperAdmin && user.EmailVerifiedAt == nil {
		return nil, ErrEmailNotVerified
	}


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
			_, err := s.store.GetTenantByID(ctx, reqTenantUUID)
			if err != nil {
				return nil, fmt.Errorf("requested tenant not found: %w", err)
			}
			activeTenantID = reqTenantUUID
		} else {
			memberships, _ := s.store.ListUserMemberships(ctx, user.ID)
			if len(memberships) > 0 {
				activeTenantID = memberships[0].TenantID
			} else {
				return nil, errors.New("super_admin must supply a tenant_id or have at least one tenant membership to scope API operations")
			}
		}
	} else {
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

	targetUser, err := s.store.GetUserByID(ctx, targetUserID)
	if err != nil {
		return nil, err
	}
	if targetUser.Status != UserStatusActive {
		return nil, errors.New("cannot add a disabled user account to a tenant")
	}

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

// VerifyEmail verifies an account via OTP code or confirmation token.
func (s *Service) VerifyEmail(ctx context.Context, req VerifyEmailRequest) (*SafeUser, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	code := strings.TrimSpace(req.Code)
	token := strings.TrimSpace(req.Token)

	if token == "" && (email == "" || code == "") {
		return nil, ErrInvalidOrExpiredVerification
	}

	user, err := s.store.VerifyOTPOrToken(ctx, email, code, token)
	if err != nil {
		return nil, err
	}
	safe := user.ToSafeUser()
	return &safe, nil
}

// ResendVerification generates a new OTP/token and dispatches a verification email.
func (s *Service) ResendVerification(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if err := validateEmail(email); err != nil {
		return err
	}

	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			// Don't leak whether the account exists
			return nil
		}
		return err
	}

	if user.EmailVerifiedAt != nil {
		return errors.New("email is already verified")
	}

	otp, err := generateOTP()
	if err != nil {
		return fmt.Errorf("failed to generate verification OTP: %w", err)
	}
	token, err := generateToken()
	if err != nil {
		return fmt.Errorf("failed to generate verification token: %w", err)
	}

	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	if err := s.store.SaveVerification(ctx, user.Email, otp, token, expiresAt); err != nil {
		return fmt.Errorf("failed to save email verification: %w", err)
	}

	if s.mailer != nil {
		appURL := "http://localhost:5174"
		if s.mailer.AppURL() != "" {
			appURL = s.mailer.AppURL()
		}
		confirmLink := fmt.Sprintf("%s/verify?token=%s&email=%s", appURL, token, url.QueryEscape(user.Email))
		go func() {
			_ = s.mailer.SendVerificationOTPEmail(user.Email, otp, confirmLink)
		}()
	}

	return nil
}

// InviteTenantMember dispatches an invitation email with an invitation link to the requested user.
func (s *Service) InviteTenantMember(ctx context.Context, caller auth.Identity, tenantID uuid.UUID, req InviteMemberRequest) (*InviteMemberResponse, error) {
	if err := s.checkTenantAdminAuth(caller, tenantID); err != nil {
		return nil, err
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	if err := validateEmail(email); err != nil {
		return nil, err
	}

	if req.Role == "" {
		req.Role = auth.RoleAgentDeveloper
	}

	if req.Role == auth.RoleSuperAdmin {
		return nil, ErrCannotAssignSuperAdmin
	}
	if _, ok := auth.ValidRoles[req.Role]; !ok {
		return nil, ErrInvalidRole
	}

	tenant, err := s.store.GetTenantByID(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	inviterName := "Organization Admin"
	if callerUUID, err := uuid.Parse(caller.UserID); err == nil {
		if inviterUser, err := s.store.GetUserByID(ctx, callerUUID); err == nil && inviterUser != nil {
			if inviterUser.DisplayName != "" {
				inviterName = inviterUser.DisplayName
			} else {
				inviterName = inviterUser.Email
			}
		}
	}

	inviteToken, _ := generateToken()
	inv := &Invitation{
		ID:        uuid.New(),
		TenantID:  tenantID,
		Email:     email,
		Role:      req.Role,
		Token:     inviteToken,
		InvitedBy: inviterName,
		Status:    "pending",
		ExpiresAt: time.Now().UTC().Add(7 * 24 * time.Hour),
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateInvitation(ctx, inv); err != nil {
		return nil, fmt.Errorf("failed to persist invitation: %w", err)
	}

	appURL := "http://localhost:5174"
	if s.mailer != nil && s.mailer.AppURL() != "" {
		appURL = s.mailer.AppURL()
	}

	inviteLink := fmt.Sprintf("%s/invite?email=%s&org=%s&role=%s&invitedBy=%s&token=%s",
		appURL,
		url.QueryEscape(email),
		url.QueryEscape(tenant.Name),
		url.QueryEscape(string(req.Role)),
		url.QueryEscape(inviterName),
		inviteToken,
	)

	if s.mailer != nil {
		go func() {
			_ = s.mailer.SendInvitationEmail(email, inviterName, tenant.Name, inviteLink)
		}()
	}

	return &InviteMemberResponse{
		Email:      email,
		Role:       req.Role,
		Status:     "invited",
		InviteLink: inviteLink,
		Message:    fmt.Sprintf("Invitation sent to %s via Mailtrap", email),
	}, nil
}

// AcceptInvitation verifies the token, creates the user account and active membership, and issues an access JWT.
func (s *Service) AcceptInvitation(ctx context.Context, req AcceptInviteRequest) (*AcceptInviteResponse, error) {
	token := strings.TrimSpace(req.Token)
	if token == "" {
		return nil, errors.New("invitation token is required")
	}

	fullName := strings.TrimSpace(req.FullName)
	if fullName == "" {
		return nil, errors.New("full name is required")
	}

	password := req.Password
	if len(password) < 8 || len(password) > 128 {
		return nil, ErrWeakPassword
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	user, mem, tenant, err := s.store.AcceptInvitation(ctx, token, fullName, string(hash))
	if err != nil {
		return nil, err
	}

	identity := auth.Identity{
		UserID:   user.ID.String(),
		TenantID: tenant.ID,
		Role:     mem.Role,
	}

	accessToken, err := s.tokenIssuer.IssueToken(identity, s.tokenTTL)
	if err != nil {
		return nil, fmt.Errorf("failed to issue access token: %w", err)
	}

	return &AcceptInviteResponse{
		AccessToken:    accessToken,
		TokenType:      "Bearer",
		ExpiresIn:      int64(s.tokenTTL.Seconds()),
		User:           user.ToSafeUser(),
		ActiveTenantID: tenant.ID,
		Role:           mem.Role,
	}, nil
}


