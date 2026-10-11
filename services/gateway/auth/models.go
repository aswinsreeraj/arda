package auth

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Role defines an authorized role within the ARDA governance platform.
type Role string

const (
	RoleSuperAdmin     Role = "super_admin"
	RoleCompanyAdmin   Role = "company_admin"
	RoleAgentDeveloper Role = "agent_developer"
	RoleReviewer       Role = "reviewer"
)

// ValidRoles is the set of recognized ARDA roles.
var ValidRoles = map[Role]struct{}{
	RoleSuperAdmin:     {},
	RoleCompanyAdmin:   {},
	RoleAgentDeveloper: {},
	RoleReviewer:       {},
}

// ParseRole validates and converts a string into a Role.
func ParseRole(s string) (Role, error) {
	r := Role(s)
	if _, ok := ValidRoles[r]; !ok {
		return "", fmt.Errorf("unknown role: %s", s)
	}
	return r, nil
}

// Identity represents the validated identity extracted from verified JWT claims.
type Identity struct {
	UserID   string    `json:"user_id"`
	TenantID uuid.UUID `json:"tenant_id"`
	Role     Role      `json:"role"`
}

type contextKey string

const (
	identityContextKey contextKey = "gateway_identity"
	TrackerContextKey  contextKey = "identity_tracker"
)

// IdentityTracker holds a mutable pointer to the verified identity for per-request structured logging.
type IdentityTracker struct {
	Identity *Identity
}

// TrackIdentity sets the validated identity in the request's tracker if present.
func TrackIdentity(ctx context.Context, id Identity) {
	if tracker, ok := ctx.Value(TrackerContextKey).(*IdentityTracker); ok {
		tracker.Identity = &id
	}
}

// WithIdentity stores the validated Identity in the request context.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityContextKey, id)
}

// IdentityFromContext extracts the validated Identity from context.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityContextKey).(Identity)
	return id, ok && id.TenantID != uuid.Nil
}
