package identity_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/aswinsreeraj/arda/services/gateway/identity"
	"github.com/google/uuid"
)

type mockTokenIssuer struct {
	lastIdentity auth.Identity
	tokenToReturn string
	errToReturn   error
}

func (m *mockTokenIssuer) IssueToken(id auth.Identity, ttl time.Duration) (string, error) {
	m.lastIdentity = id
	if m.errToReturn != nil {
		return "", m.errToReturn
	}
	if m.tokenToReturn != "" {
		return m.tokenToReturn, nil
	}
	return "mock.jwt.token", nil
}

func setupTestService() (*identity.Service, *identity.MemoryStore, *mockTokenIssuer) {
	store := identity.NewMemoryStore()
	issuer := &mockTokenIssuer{}
	svc := identity.NewService(store, issuer)
	return svc, store, issuer
}

func TestRegister(t *testing.T) {
	svc, store, _ := setupTestService()
	ctx := context.Background()

	// 1. Successful registration
	req := identity.RegisterRequest{
		Email:       "  Alice@Example.COM ",
		Password:    "password123",
		DisplayName: "Alice Developer",
	}

	user, err := svc.Register(ctx, req)
	if err != nil {
		t.Fatalf("unexpected registration error: %v", err)
	}

	if user.Email != "alice@example.com" {
		t.Fatalf("expected normalized email 'alice@example.com', got %q", user.Email)
	}
	if user.DisplayName != "Alice Developer" {
		t.Fatalf("expected display name 'Alice Developer', got %q", user.DisplayName)
	}
	if user.IsSuperAdmin {
		t.Fatalf("new user should not be super_admin")
	}

	// Verify password hash in store
	dbUser, err := store.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("failed to fetch user from store: %v", err)
	}
	if dbUser.PasswordHash == "" || dbUser.PasswordHash == "password123" {
		t.Fatalf("password must be hashed, got %q", dbUser.PasswordHash)
	}

	// 2. Duplicate registration rejected
	_, err = svc.Register(ctx, req)
	if !errors.Is(err, identity.ErrUserAlreadyExists) {
		t.Fatalf("expected ErrUserAlreadyExists on duplicate email, got %v", err)
	}

	// 3. Invalid email rejected
	_, err = svc.Register(ctx, identity.RegisterRequest{
		Email:    "not-an-email",
		Password: "password123",
	})
	if !errors.Is(err, identity.ErrInvalidEmail) {
		t.Fatalf("expected ErrInvalidEmail, got %v", err)
	}

	// 4. Short password rejected
	_, err = svc.Register(ctx, identity.RegisterRequest{
		Email:    "bob@example.com",
		Password: "short",
	})
	if !errors.Is(err, identity.ErrWeakPassword) {
		t.Fatalf("expected ErrWeakPassword, got %v", err)
	}
}

func TestLogin(t *testing.T) {
	svc, store, issuer := setupTestService()
	ctx := context.Background()

	// Register user
	user, err := svc.Register(ctx, identity.RegisterRequest{
		Email:    "carol@example.com",
		Password: "mypassword123",
	})
	if err != nil {
		t.Fatalf("setup registration failed: %v", err)
	}

	// Setup tenant and membership
	tenant := &identity.Tenant{
		ID:   uuid.New(),
		Name: "Carol's Org",
	}
	_, _, err = store.CreateTenantWithAdmin(ctx, tenant, user.ID)
	if err != nil {
		t.Fatalf("failed to setup tenant: %v", err)
	}

	// 1. Successful login
	loginRes, err := svc.Login(ctx, identity.LoginRequest{
		Email:    " CAROL@example.com ",
		Password: "mypassword123",
	})
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	if loginRes.AccessToken != "mock.jwt.token" {
		t.Fatalf("unexpected access token: %v", loginRes.AccessToken)
	}
	if loginRes.ActiveTenantID != tenant.ID {
		t.Fatalf("expected active tenant %s, got %s", tenant.ID, loginRes.ActiveTenantID)
	}
	if loginRes.Role != auth.RoleCompanyAdmin {
		t.Fatalf("expected role %s, got %s", auth.RoleCompanyAdmin, loginRes.Role)
	}
	if issuer.lastIdentity.UserID != user.ID.String() {
		t.Fatalf("expected issued token for user %s, got %s", user.ID, issuer.lastIdentity.UserID)
	}

	// 2. Invalid password rejected
	_, err = svc.Login(ctx, identity.LoginRequest{
		Email:    "carol@example.com",
		Password: "wrongpassword",
	})
	if !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials on wrong password, got %v", err)
	}

	// 3. Unknown user rejected
	_, err = svc.Login(ctx, identity.LoginRequest{
		Email:    "unknown@example.com",
		Password: "anyPassword123",
	})
	if !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials on unknown user, got %v", err)
	}

	// 4. Disabled user rejected
	dbUser, _ := store.GetUserByID(ctx, user.ID)
	dbUser.Status = identity.UserStatusDisabled
	_ = store.UpdateUser(ctx, dbUser)

	_, err = svc.Login(ctx, identity.LoginRequest{
		Email:    "carol@example.com",
		Password: "mypassword123",
	})
	if !errors.Is(err, identity.ErrAccountDisabled) {
		t.Fatalf("expected ErrAccountDisabled, got %v", err)
	}
}

func TestTenantAndMembershipManagement(t *testing.T) {
	svc, _, _ := setupTestService()
	ctx := context.Background()

	// Register Admin
	admin, _ := svc.Register(ctx, identity.RegisterRequest{
		Email:    "admin@corp.com",
		Password: "adminPassword123",
	})

	// Register Developer
	dev, _ := svc.Register(ctx, identity.RegisterRequest{
		Email:    "dev@corp.com",
		Password: "devPassword123",
	})

	adminCaller := auth.Identity{
		UserID:   admin.ID.String(),
		TenantID: uuid.Nil,
		Role:     auth.RoleCompanyAdmin,
	}

	// 1. Create Tenant with Admin
	tenant, mem, err := svc.CreateTenant(ctx, adminCaller, identity.CreateTenantRequest{
		Name: "Acme Corp",
	})
	if err != nil {
		t.Fatalf("failed to create tenant: %v", err)
	}
	if mem.Role != auth.RoleCompanyAdmin {
		t.Fatalf("expected admin membership, got %s", mem.Role)
	}

	// Update admin caller with tenant ID
	adminCaller.TenantID = tenant.ID

	// 2. Add Developer to Tenant
	devMem, err := svc.AddTenantMember(ctx, adminCaller, tenant.ID, identity.AddMemberRequest{
		UserID: dev.ID,
		Role:   auth.RoleAgentDeveloper,
	})
	if err != nil {
		t.Fatalf("failed to add member: %v", err)
	}
	if devMem.Role != auth.RoleAgentDeveloper {
		t.Fatalf("expected role agent_developer, got %s", devMem.Role)
	}

	// 3. Cannot assign super_admin as tenant role
	_, err = svc.AddTenantMember(ctx, adminCaller, tenant.ID, identity.AddMemberRequest{
		Email: "dev@corp.com",
		Role:  auth.RoleSuperAdmin,
	})
	if !errors.Is(err, identity.ErrCannotAssignSuperAdmin) {
		t.Fatalf("expected ErrCannotAssignSuperAdmin, got %v", err)
	}

	// 4. Cross-tenant administration denied
	otherTenantID := uuid.New()
	_, err = svc.AddTenantMember(ctx, adminCaller, otherTenantID, identity.AddMemberRequest{
		UserID: dev.ID,
		Role:   auth.RoleAgentDeveloper,
	})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected cross-tenant denial, got %v", err)
	}

	// 5. List Members
	members, err := svc.ListTenantMembers(ctx, adminCaller, tenant.ID)
	if err != nil {
		t.Fatalf("failed to list members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}

	// 6. Demoting/Deleting the last active admin must fail
	err = svc.DeleteTenantMember(ctx, adminCaller, tenant.ID, admin.ID)
	if !errors.Is(err, identity.ErrCannotRemoveLastAdmin) {
		t.Fatalf("expected ErrCannotRemoveLastAdmin when removing only admin, got %v", err)
	}

	_, err = svc.UpdateTenantMember(ctx, adminCaller, tenant.ID, admin.ID, identity.UpdateMemberRequest{
		Role: auth.RoleAgentDeveloper,
	})
	if !errors.Is(err, identity.ErrCannotRemoveLastAdmin) {
		t.Fatalf("expected ErrCannotRemoveLastAdmin when demoting only admin, got %v", err)
	}

	// 7. Developer cannot manage members
	devCaller := auth.Identity{
		UserID:   dev.ID.String(),
		TenantID: tenant.ID,
		Role:     auth.RoleAgentDeveloper,
	}
	err = svc.DeleteTenantMember(ctx, devCaller, tenant.ID, admin.ID)
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied for developer managing members, got %v", err)
	}
}
