package vault

import (
	"context"

	"github.com/google/uuid"
)

type contextKey string

const (
	tenantIDContextKey contextKey = "arda_vault_tenant_id"
)

var fallbackResolvers []func(context.Context) (uuid.UUID, bool)

// RegisterTenantResolver registers an external context tenant extractor (e.g. from gateway auth or registry).
func RegisterTenantResolver(resolver func(context.Context) (uuid.UUID, bool)) {
	if resolver != nil {
		fallbackResolvers = append(fallbackResolvers, resolver)
	}
}

// WithTenantID returns a new context containing the validated tenant ID.
func WithTenantID(ctx context.Context, tenantID uuid.UUID) context.Context {
	return context.WithValue(ctx, tenantIDContextKey, tenantID)
}

// TenantIDFromContext extracts a validated tenant ID from context.
// It checks the vault-specific context value first, and then any registered fallback resolvers.
func TenantIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	if ctx == nil {
		return uuid.Nil, false
	}
	if val, ok := ctx.Value(tenantIDContextKey).(uuid.UUID); ok && val != uuid.Nil {
		return val, true
	}
	for _, resolver := range fallbackResolvers {
		if id, ok := resolver(ctx); ok && id != uuid.Nil {
			return id, true
		}
	}
	return uuid.Nil, false
}
