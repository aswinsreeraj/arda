package auth

import (
	"net/http"
	"strings"

	registryhttp "github.com/aswinsreeraj/arda/services/registry/http"
	"github.com/gin-gonic/gin"
)

// ErrorWriter writes a standardized JSON error response.
type ErrorWriter func(w http.ResponseWriter, status int, code, message string)

// AuthenticateMiddleware returns a Gin middleware that enforces JWT validation.
func AuthenticateMiddleware(validator *TokenValidator, writeErr ErrorWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Strip any untrusted client-supplied tenant headers
		c.Request.Header.Del(registryhttp.TenantHeader)

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.Abort()
			writeErr(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "missing authorization header")
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.Abort()
			writeErr(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid authorization header format: must be 'Bearer <token>'")
			return
		}

		tokenString := strings.TrimSpace(parts[1])
		if tokenString == "" {
			c.Abort()
			writeErr(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "bearer token is empty")
			return
		}

		identity, err := validator.ValidateToken(tokenString)
		if err != nil {
			c.Abort()
			writeErr(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid or expired bearer token")
			return
		}

		// Track identity for outer structured logging middleware
		TrackIdentity(c.Request.Context(), identity)

		// Propagate validated identity in context
		ctx := WithIdentity(c.Request.Context(), identity)

		// Propagate validated tenant_id into registry's context boundary as well
		ctx = registryhttp.WithTenantID(ctx, identity.TenantID)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

// RequireActionMiddleware enforces that the authenticated identity possesses permission for the specified action.
func RequireActionMiddleware(action Action, writeErr ErrorWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := IdentityFromContext(c.Request.Context())
		if !ok {
			c.Abort()
			writeErr(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
			return
		}

		if err := Authorize(identity, action); err != nil {
			c.Abort()
			writeErr(c.Writer, http.StatusForbidden, "FORBIDDEN", err.Error())
			return
		}

		c.Next()
	}
}
