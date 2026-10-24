package execution

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aswinsreeraj/arda/services/gateway/auth"
	registryhttp "github.com/aswinsreeraj/arda/services/registry/http"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Handler manages HTTP transport and routing for the Execution Service.
type Handler struct {
	svc          *Service
	jwtValidator *auth.TokenValidator
}

// NewHandler creates a new Handler with the given Service and JWT validator.
func NewHandler(svc *Service, jwtValidator *auth.TokenValidator) *Handler {
	return &Handler{
		svc:          svc,
		jwtValidator: jwtValidator,
	}
}

// RegisterRoutes registers execution endpoints and health checks on the Gin engine.
func (h *Handler) RegisterRoutes(router *gin.Engine) {
	// Public health check route
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := router.Group("")

	// If JWT validator is configured, enforce standard Bearer token authentication
	if h.jwtValidator != nil {
		api.Use(auth.AuthenticateMiddleware(h.jwtValidator, func(w http.ResponseWriter, status int, code, message string) {
			writeErrorResponse(w, status, code, message)
		}))
	}

	// Canonical agent execution routes
	api.POST("/v1/agents/:id/execute", h.SubmitExecutionHandler)
	api.POST("/api/v1/agents/:id/execute", h.SubmitExecutionHandler)

	// Direct execution submission routes
	api.POST("/v1/executions", h.SubmitExecutionHandler)
	api.POST("/api/v1/executions", h.SubmitExecutionHandler)
}

// SubmitExecutionHandler handles POST execution requests, extracts the trusted tenant identity,
// validates inputs, and submits the workflow through the execution service.
func (h *Handler) SubmitExecutionHandler(c *gin.Context) {
	// Strip any untrusted client-supplied tenant headers
	c.Request.Header.Del(registryhttp.TenantHeader)

	// Extract trusted caller identity from request context
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		writeErrorResponse(c.Writer, http.StatusUnauthorized, string(CodeUnauthenticated), "authentication required")
		return
	}

	var req SubmitExecutionRequest
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
			writeErrorResponse(c.Writer, http.StatusBadRequest, string(CodeValidationFailed), "invalid json request body")
			return
		}
	}

	// If agent ID is provided in the URL path, bind it and verify consistency
	if pathID := c.Param("id"); pathID != "" {
		parsedPathID, err := uuid.Parse(pathID)
		if err != nil {
			writeErrorResponse(c.Writer, http.StatusBadRequest, string(CodeValidationFailed), "invalid agent id in URL path: must be a valid uuid")
			return
		}
		if req.AgentID != uuid.Nil && req.AgentID != parsedPathID {
			writeErrorResponse(c.Writer, http.StatusBadRequest, string(CodeValidationFailed), "agent_id in URL path does not match agent_id in request body")
			return
		}
		req.AgentID = parsedPathID
	}

	// Dispatch execution request
	resp, err := h.svc.SubmitExecution(c.Request.Context(), identity, req)
	if err != nil {
		var execErr *ExecutionError
		if errors.As(err, &execErr) {
			writeErrorResponse(c.Writer, execErr.HTTPStatus, string(execErr.Code), execErr.Message)
			return
		}
		writeErrorResponse(c.Writer, http.StatusInternalServerError, string(CodeInternalError), "failed to process execution request")
		return
	}

	c.JSON(http.StatusAccepted, resp)
}

func writeErrorResponse(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"details": map[string]any{},
		},
	})
}
