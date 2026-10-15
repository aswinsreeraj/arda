package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/aswinsreeraj/arda/services/registry/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Handler handles HTTP requests for the Agent Registry API.
type Handler struct {
	svc *service.RegistryService
}

// NewHandler creates a new Handler with the given RegistryService.
func NewHandler(svc *service.RegistryService) *Handler {
	return &Handler{svc: svc}
}

// CreateAgentHandler handles POST /api/v1/agents
func (h *Handler) CreateAgentHandler(c *gin.Context) {
	tenantID, ok := TenantIDFromContext(c.Request.Context())
	if !ok {
		writeErrorResponse(c, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	var req service.CreateAgentRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeErrorResponse(c, domain.NewValidationError("invalid json request body", err.Error()))
		return
	}

	detail, err := h.svc.CreateAgent(c.Request.Context(), tenantID, req)
	if err != nil {
		writeErrorResponse(c, err)
		return
	}

	writeJSON(c, http.StatusCreated, detail)
}

// GetAgentHandler handles GET /api/v1/agents/:id
func (h *Handler) GetAgentHandler(c *gin.Context) {
	tenantID, ok := TenantIDFromContext(c.Request.Context())
	if !ok {
		writeErrorResponse(c, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	idStr := c.Param("id")
	agentID, err := uuid.Parse(idStr)
	if err != nil {
		writeErrorResponse(c, domain.NewValidationError("invalid agent id: must be a valid uuid"))
		return
	}

	detail, err := h.svc.GetAgent(c.Request.Context(), tenantID, agentID)
	if err != nil {
		writeErrorResponse(c, err)
		return
	}

	writeJSON(c, http.StatusOK, detail)
}

// CreateVersionHandler handles POST /api/v1/agents/:id/versions
func (h *Handler) CreateVersionHandler(c *gin.Context) {
	tenantID, ok := TenantIDFromContext(c.Request.Context())
	if !ok {
		writeErrorResponse(c, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	idStr := c.Param("id")
	agentID, err := uuid.Parse(idStr)
	if err != nil {
		writeErrorResponse(c, domain.NewValidationError("invalid agent id: must be a valid uuid"))
		return
	}

	// Support both { "configuration": { ... } } and direct flat configuration in body
	var body struct {
		Configuration *domain.Configuration `json:"configuration"`
		// Flat fields
		PromptTemplate       string               `json:"prompt_template"`
		Tools                []string             `json:"tools"`
		ReliabilityThreshold *float64             `json:"reliability_threshold"`
		ExecutionMode        domain.ExecutionMode `json:"execution_mode"`
		Guardrails           map[string]any       `json:"guardrails"`
		Budgets              map[string]any       `json:"budgets"`
	}

	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		writeErrorResponse(c, domain.NewValidationError("invalid json request body", err.Error()))
		return
	}

	var cfg domain.Configuration
	if body.Configuration != nil {
		cfg = *body.Configuration
	} else {
		threshold := 0.85
		if body.ReliabilityThreshold != nil {
			threshold = *body.ReliabilityThreshold
		}
		mode := body.ExecutionMode
		if mode == "" {
			mode = domain.ExecutionModeSupervised
		}
		tools := body.Tools
		if tools == nil {
			tools = []string{}
		}
		cfg = domain.Configuration{
			PromptTemplate:       body.PromptTemplate,
			Tools:                tools,
			ReliabilityThreshold: threshold,
			ExecutionMode:        mode,
			Guardrails:           body.Guardrails,
			Budgets:              body.Budgets,
		}
	}

	version, err := h.svc.CreateDraftVersion(c.Request.Context(), tenantID, agentID, cfg)
	if err != nil {
		writeErrorResponse(c, err)
		return
	}

	writeJSON(c, http.StatusCreated, version)
}

// PublishVersionHandler handles POST /api/v1/agents/:id/versions/:versionId/publish
func (h *Handler) PublishVersionHandler(c *gin.Context) {
	tenantID, ok := TenantIDFromContext(c.Request.Context())
	if !ok {
		writeErrorResponse(c, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	agentIDStr := c.Param("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		writeErrorResponse(c, domain.NewValidationError("invalid agent id: must be a valid uuid"))
		return
	}

	versionIDStr := c.Param("versionId")
	versionID, err := uuid.Parse(versionIDStr)
	if err != nil {
		writeErrorResponse(c, domain.NewValidationError("invalid version id: must be a valid uuid"))
		return
	}

	version, err := h.svc.PublishVersion(c.Request.Context(), tenantID, agentID, versionID)
	if err != nil {
		writeErrorResponse(c, err)
		return
	}

	writeJSON(c, http.StatusOK, version)
}

// GetLatestPublishedVersionHandler handles GET /api/v1/agents/:id/versions/latest
func (h *Handler) GetLatestPublishedVersionHandler(c *gin.Context) {
	tenantID, ok := TenantIDFromContext(c.Request.Context())
	if !ok {
		writeErrorResponse(c, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	agentIDStr := c.Param("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		writeErrorResponse(c, domain.NewValidationError("invalid agent id: must be a valid uuid"))
		return
	}

	version, err := h.svc.GetLatestPublishedVersion(c.Request.Context(), tenantID, agentID)
	if err != nil {
		writeErrorResponse(c, err)
		return
	}

	writeJSON(c, http.StatusOK, version)
}

// GetVersionHandler handles GET /api/v1/agents/:id/versions/:versionId
func (h *Handler) GetVersionHandler(c *gin.Context) {
	tenantID, ok := TenantIDFromContext(c.Request.Context())
	if !ok {
		writeErrorResponse(c, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	agentIDStr := c.Param("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		writeErrorResponse(c, domain.NewValidationError("invalid agent id: must be a valid uuid"))
		return
	}

	versionIDStr := c.Param("versionId")
	versionID, err := uuid.Parse(versionIDStr)
	if err != nil {
		writeErrorResponse(c, domain.NewValidationError("invalid version id: must be a valid uuid"))
		return
	}

	version, err := h.svc.GetVersion(c.Request.Context(), tenantID, agentID, versionID)
	if err != nil {
		writeErrorResponse(c, err)
		return
	}

	writeJSON(c, http.StatusOK, version)
}

// UpdateVersionHandler handles PUT /api/v1/agents/:id/versions/:versionId
// Rejects modification if the version is published.
func (h *Handler) UpdateVersionHandler(c *gin.Context) {
	tenantID, ok := TenantIDFromContext(c.Request.Context())
	if !ok {
		writeErrorResponse(c, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	agentIDStr := c.Param("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		writeErrorResponse(c, domain.NewValidationError("invalid agent id: must be a valid uuid"))
		return
	}

	versionIDStr := c.Param("versionId")
	versionID, err := uuid.Parse(versionIDStr)
	if err != nil {
		writeErrorResponse(c, domain.NewValidationError("invalid version id: must be a valid uuid"))
		return
	}

	var cfg domain.Configuration
	if err := json.NewDecoder(c.Request.Body).Decode(&cfg); err != nil {
		writeErrorResponse(c, domain.NewValidationError("invalid json request body", err.Error()))
		return
	}

	ver, err := h.svc.UpdateDraftVersion(c.Request.Context(), tenantID, agentID, versionID, cfg)
	if err != nil {
		writeErrorResponse(c, err)
		return
	}

	writeJSON(c, http.StatusOK, ver)
}

// DeleteVersionHandler handles DELETE /api/v1/agents/:id/versions/:versionId
// Rejects deletion if the version is published.
func (h *Handler) DeleteVersionHandler(c *gin.Context) {
	tenantID, ok := TenantIDFromContext(c.Request.Context())
	if !ok {
		writeErrorResponse(c, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	agentIDStr := c.Param("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		writeErrorResponse(c, domain.NewValidationError("invalid agent id: must be a valid uuid"))
		return
	}

	versionIDStr := c.Param("versionId")
	versionID, err := uuid.Parse(versionIDStr)
	if err != nil {
		writeErrorResponse(c, domain.NewValidationError("invalid version id: must be a valid uuid"))
		return
	}

	if err := h.svc.DeleteDraftVersion(c.Request.Context(), tenantID, agentID, versionID); err != nil {
		writeErrorResponse(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// Response helpers

type errorPayload struct {
	Error domain.DomainError `json:"error"`
}

func writeErrorResponse(c *gin.Context, err error) {
	var domErr *domain.DomainError
	if !errors.As(err, &domErr) {
		domErr = domain.NewInternalError("internal server error", err)
	}

	statusCode := http.StatusInternalServerError
	switch domErr.Code {
	case domain.CodeNotFound:
		statusCode = http.StatusNotFound
	case domain.CodeValidationFailed:
		statusCode = http.StatusBadRequest
	case domain.CodeVersionConflict, domain.CodeAlreadyPublished, domain.CodePublishedImmutable:
		statusCode = http.StatusConflict
	case domain.CodeUnauthenticated:
		statusCode = http.StatusUnauthorized
	}

	c.AbortWithStatusJSON(statusCode, errorPayload{Error: *domErr})
}

func writeJSON(c *gin.Context, statusCode int, data any) {
	c.JSON(statusCode, data)
}
