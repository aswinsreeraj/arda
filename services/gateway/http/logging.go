package http

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type reqIDKey string

const RequestIDHeader = "X-Request-ID"
const requestIDContextKey reqIDKey = "request_id"

// WithRequestID stores the request ID in context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey, id)
}

// RequestIDFromContext retrieves the request ID from context.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDContextKey).(string); ok {
		return id
	}
	return ""
}

// LoggingMiddleware logs request metadata without leaking credentials or tokens.
func LoggingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := strings.TrimSpace(c.GetHeader(RequestIDHeader))
		if reqID == "" {
			reqID = uuid.New().String()
		}

		tracker := &auth.IdentityTracker{}
		ctx := WithRequestID(c.Request.Context(), reqID)
		ctx = context.WithValue(ctx, auth.TrackerContextKey, tracker)
		c.Request = c.Request.WithContext(ctx)
		c.Header(RequestIDHeader, reqID)

		start := time.Now()
		c.Next()
		duration := time.Since(start)

		tenantIDStr := "-"
		userIDStr := "-"
		roleStr := "-"
		if tracker.Identity != nil {
			tenantIDStr = tracker.Identity.TenantID.String()
			userIDStr = tracker.Identity.UserID
			roleStr = string(tracker.Identity.Role)
		} else if identity, ok := auth.IdentityFromContext(c.Request.Context()); ok {
			tenantIDStr = identity.TenantID.String()
			userIDStr = identity.UserID
			roleStr = string(identity.Role)
		}

		log.Printf("[GATEWAY] req_id=%s method=%s path=%s status=%d duration_ms=%d tenant_id=%s user_id=%s role=%s",
			reqID,
			c.Request.Method,
			c.Request.URL.Path,
			c.Writer.Status(),
			duration.Milliseconds(),
			tenantIDStr,
			userIDStr,
			roleStr,
		)
	}
}
