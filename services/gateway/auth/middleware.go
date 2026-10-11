package auth

import (
	"net/http"
	"strings"

	registryhttp "github.com/aswinsreeraj/arda/services/registry/http"
)

// ErrorWriter writes a standardized JSON error response.
type ErrorWriter func(w http.ResponseWriter, status int, code, message string)

// AuthenticateMiddleware returns an HTTP middleware that enforces JWT validation.
func AuthenticateMiddleware(validator *TokenValidator, writeErr ErrorWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Strip any untrusted client-supplied tenant headers
			r.Header.Del(registryhttp.TenantHeader)

			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing authorization header")
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid authorization header format: must be 'Bearer <token>'")
				return
			}

			tokenString := strings.TrimSpace(parts[1])
			if tokenString == "" {
				writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "bearer token is empty")
				return
			}

			identity, err := validator.ValidateToken(tokenString)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid or expired bearer token")
				return
			}

			// Track identity for outer structured logging middleware
			TrackIdentity(r.Context(), identity)

			// Propagate validated identity in context
			ctx := WithIdentity(r.Context(), identity)

			// Propagate validated tenant_id into registry's context boundary as well
			ctx = registryhttp.WithTenantID(ctx, identity.TenantID)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireActionMiddleware enforces that the authenticated identity possesses permission for the specified action.
func RequireActionMiddleware(action Action, writeErr ErrorWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := IdentityFromContext(r.Context())
			if !ok {
				writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
				return
			}

			if err := Authorize(identity, action); err != nil {
				writeErr(w, http.StatusForbidden, "FORBIDDEN", err.Error())
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
