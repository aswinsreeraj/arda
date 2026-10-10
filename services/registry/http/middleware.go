package http

import (
	"context"
	"net/http"
	"strings"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
)

type contextKey string

const (
	tenantIDContextKey contextKey = "tenant_id"
	TenantHeader                  = "X-Tenant-ID"
)

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

// HeaderTenantResolver extracts the tenant UUID from the trusted X-Tenant-ID header.
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
