package grpc

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type contextKey string

const (
	tenantContextKey contextKey = "arda-tenant-id"
	userContextKey   contextKey = "arda-user-id"
	roleContextKey   contextKey = "arda-role"

	// MetadataTenantID is the trusted internal metadata header propagating validated tenant identity.
	MetadataTenantID = "x-tenant-id"

	// MetadataUserID is the trusted internal metadata header propagating validated user identity.
	MetadataUserID = "x-user-id"

	// MetadataRole is the trusted internal metadata header propagating validated user role.
	MetadataRole = "x-role"
)

// InternalAuthInterceptor validates that incoming gRPC calls possess trusted internal tenant context.
func InternalAuthInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing internal request metadata")
		}

		tenantVals := md.Get(MetadataTenantID)
		if len(tenantVals) == 0 || strings.TrimSpace(tenantVals[0]) == "" {
			return nil, status.Error(codes.Unauthenticated, "missing trusted tenant context in gRPC metadata")
		}

		tenantUUID, err := uuid.Parse(strings.TrimSpace(tenantVals[0]))
		if err != nil || tenantUUID == uuid.Nil {
			return nil, status.Error(codes.Unauthenticated, "invalid tenant uuid in gRPC metadata")
		}

		ctx = context.WithValue(ctx, tenantContextKey, tenantUUID)

		if userVals := md.Get(MetadataUserID); len(userVals) > 0 {
			ctx = context.WithValue(ctx, userContextKey, strings.TrimSpace(userVals[0]))
		}
		if roleVals := md.Get(MetadataRole); len(roleVals) > 0 {
			ctx = context.WithValue(ctx, roleContextKey, strings.TrimSpace(roleVals[0]))
		}

		return handler(ctx, req)
	}
}

// TenantIDFromContext retrieves the trusted tenant UUID from context.
func TenantIDFromContext(ctx context.Context) (uuid.UUID, error) {
	if val, ok := ctx.Value(tenantContextKey).(uuid.UUID); ok && val != uuid.Nil {
		return val, nil
	}
	return uuid.Nil, status.Error(codes.Unauthenticated, "missing trusted tenant context")
}

// ContextWithTenantID injects a trusted tenant ID into context (primarily for unit tests).
func ContextWithTenantID(ctx context.Context, tenantID uuid.UUID) context.Context {
	return context.WithValue(ctx, tenantContextKey, tenantID)
}
