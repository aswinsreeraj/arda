package http

import (
	"context"
	"net/http"
	"strings"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
)

type contextKey string

const tenantIDContextKey contextKey = "tenant_id"

const TenantHeader = "X-Tenant-ID"

// WithTenantID returns a new context with the given tenant ID.
func WithTenantID(ctx context.Context, tenantID uuid.UUID) context.Context {
	return context.WithValue(ctx, tenantIDContextKey, tenantID)
}

// TenantIDFromContext extracts the tenant ID from context.
func TenantIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	val, ok := ctx.Value(tenantIDContextKey).(uuid.UUID)
	return val, ok && val != uuid.Nil
}

// TenantResolver defines an interface for extracting tenant identity from an incoming HTTP request.
// This isolates tenant resolution behind an explicit interface boundary until full JWT authentication is integrated.
type TenantResolver interface {
	ResolveTenant(r *http.Request) (uuid.UUID, error)
}

// ContextTenantResolver extracts the tenant UUID exclusively from trusted request context.
// It strictly rejects missing or nil tenant IDs with an unauthenticated domain error
// and never falls back to client-supplied headers.
type ContextTenantResolver struct{}

func (c ContextTenantResolver) ResolveTenant(r *http.Request) (uuid.UUID, error) {
	tenantID, ok := TenantIDFromContext(r.Context())
	if !ok || tenantID == uuid.Nil {
		return uuid.Nil, domain.NewUnauthenticatedError("missing or invalid tenant context")
	}
	return tenantID, nil
}

// HeaderTenantResolver extracts the tenant UUID from the X-Tenant-ID header.
//
// SECURITY WARNING:
// This resolver trusts unauthenticated client-supplied HTTP headers and must ONLY be used
// for standalone local testing or loopback execution. In production or behind an authenticating gateway,
// use ContextTenantResolver to ensure tenant identity is derived exclusively from cryptographically verified claims.
type HeaderTenantResolver struct{}

func (h HeaderTenantResolver) ResolveTenant(r *http.Request) (uuid.UUID, error) {
	tenantHeader := strings.TrimSpace(r.Header.Get(TenantHeader))
	if tenantHeader == "" {
		return uuid.Nil, domain.NewUnauthenticatedError("missing required X-Tenant-ID header")
	}
	tenantID, err := uuid.Parse(tenantHeader)
	if err != nil {
		return uuid.Nil, domain.NewUnauthenticatedError("invalid X-Tenant-ID format: must be a valid UUID")
	}
	return tenantID, nil
}

// TenantMiddleware enforces that every request contains a valid tenant identity in context.
func TenantMiddleware(resolver TenantResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID, err := resolver.ResolveTenant(r)
		if err != nil {
			writeErrorResponse(w, err)
			return
		}
		ctx := WithTenantID(r.Context(), tenantID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
