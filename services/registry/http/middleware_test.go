package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	registryhttp "github.com/aswinsreeraj/arda/services/registry/http"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func init() {
	gin.SetMode(gin.TestMode)
}

type errorResponsePayload struct {
	Error domain.DomainError `json:"error"`
}

func TestContextTenantResolver(t *testing.T) {
	legitTenantID := uuid.New()
	spoofedTenantID := uuid.New()
	resolver := registryhttp.ContextTenantResolver{}

	t.Run("succeeds with trusted tenant context without X-Tenant-ID header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
		req = req.WithContext(registryhttp.WithTenantID(req.Context(), legitTenantID))

		resolvedID, err := resolver.ResolveTenant(req)
		if err != nil {
			t.Fatalf("unexpected error resolving tenant from context: %v", err)
		}
		if resolvedID != legitTenantID {
			t.Fatalf("expected resolved ID %s, got %s", legitTenantID, resolvedID)
		}
	})

	t.Run("rejects request when tenant context is missing", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)

		_, err := resolver.ResolveTenant(req)
		if err == nil {
			t.Fatalf("expected unauthenticated error for missing context, got nil")
		}

		domErr, ok := err.(*domain.DomainError)
		if !ok || domErr.Code != domain.CodeUnauthenticated {
			t.Fatalf("expected CodeUnauthenticated, got: %v", err)
		}
	})

	t.Run("rejects request when tenant context is uuid.Nil", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
		req = req.WithContext(registryhttp.WithTenantID(req.Context(), uuid.Nil))

		_, err := resolver.ResolveTenant(req)
		if err == nil {
			t.Fatalf("expected error for nil tenant context, got nil")
		}
	})

	t.Run("forged X-Tenant-ID header cannot override trusted context tenant", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
		req.Header.Set("X-Tenant-ID", spoofedTenantID.String())
		req = req.WithContext(registryhttp.WithTenantID(req.Context(), legitTenantID))

		resolvedID, err := resolver.ResolveTenant(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolvedID != legitTenantID {
			t.Fatalf("spoofing vulnerability: resolver returned %s instead of %s", resolvedID, legitTenantID)
		}
		if resolvedID == spoofedTenantID {
			t.Fatalf("CRITICAL SECURITY FLAW: resolver accepted forged header!")
		}
	})
}

func TestTenantMiddleware_WithContextResolver(t *testing.T) {
	legitTenantID := uuid.New()
	spoofedTenantID := uuid.New()
	resolver := registryhttp.ContextTenantResolver{}

	t.Run("middleware propagates context tenant to downstream handler without header", func(t *testing.T) {
		var capturedTenantID uuid.UUID
		router := gin.New()
		router.Use(registryhttp.TenantMiddleware(resolver))
		router.GET("/test", func(c *gin.Context) {
			if id, ok := registryhttp.TenantIDFromContext(c.Request.Context()); ok {
				capturedTenantID = id
			}
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req = req.WithContext(registryhttp.WithTenantID(req.Context(), legitTenantID))
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d", rec.Code)
		}
		if capturedTenantID != legitTenantID {
			t.Fatalf("expected inner handler to receive %s, got %s", legitTenantID, capturedTenantID)
		}
	})

	t.Run("middleware returns 401 unauthenticated when context is absent", func(t *testing.T) {
		innerCalled := false
		router := gin.New()
		router.Use(registryhttp.TenantMiddleware(resolver))
		router.GET("/test", func(c *gin.Context) {
			innerCalled = true
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if innerCalled {
			t.Fatalf("inner handler should not have been called without tenant context")
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected HTTP 401, got %d", rec.Code)
		}

		var payload errorResponsePayload
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("failed to parse error response: %v", err)
		}
		if payload.Error.Code != domain.CodeUnauthenticated {
			t.Fatalf("expected UNAUTHENTICATED, got %s", payload.Error.Code)
		}
	})

	t.Run("middleware ignores forged X-Tenant-ID and uses trusted context", func(t *testing.T) {
		var capturedTenantID uuid.UUID
		router := gin.New()
		router.Use(registryhttp.TenantMiddleware(resolver))
		router.GET("/test", func(c *gin.Context) {
			if id, ok := registryhttp.TenantIDFromContext(c.Request.Context()); ok {
				capturedTenantID = id
			}
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Tenant-ID", spoofedTenantID.String())
		req = req.WithContext(registryhttp.WithTenantID(req.Context(), legitTenantID))
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d", rec.Code)
		}
		if capturedTenantID != legitTenantID {
			t.Fatalf("expected %s, got %s", legitTenantID, capturedTenantID)
		}
	})
}
