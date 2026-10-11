package http

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/aswinsreeraj/arda/services/gateway/auth"
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

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

// LoggingMiddleware logs request metadata without leaking credentials or tokens.
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := strings.TrimSpace(r.Header.Get(RequestIDHeader))
		if reqID == "" {
			reqID = uuid.New().String()
		}

		tracker := &auth.IdentityTracker{}
		ctx := WithRequestID(r.Context(), reqID)
		ctx = context.WithValue(ctx, auth.TrackerContextKey, tracker)
		w.Header().Set(RequestIDHeader, reqID)

		rec := &statusRecorder{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		start := time.Now()
		next.ServeHTTP(rec, r.WithContext(ctx))
		duration := time.Since(start)

		tenantIDStr := "-"
		userIDStr := "-"
		roleStr := "-"
		if tracker.Identity != nil {
			tenantIDStr = tracker.Identity.TenantID.String()
			userIDStr = tracker.Identity.UserID
			roleStr = string(tracker.Identity.Role)
		} else if identity, ok := auth.IdentityFromContext(r.Context()); ok {
			tenantIDStr = identity.TenantID.String()
			userIDStr = identity.UserID
			roleStr = string(identity.Role)
		}

		log.Printf("[GATEWAY] req_id=%s method=%s path=%s status=%d duration_ms=%d tenant_id=%s user_id=%s role=%s",
			reqID,
			r.Method,
			r.URL.Path,
			rec.statusCode,
			duration.Milliseconds(),
			tenantIDStr,
			userIDStr,
			roleStr,
		)
	})
}
