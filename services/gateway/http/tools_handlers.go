package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/aswinsreeraj/arda/pkg/toolgateway"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ToolsHandler handles administrative and governance HTTP endpoints for tool definitions and audit logs.
type ToolsHandler struct {
	registry toolgateway.ToolRegistry
	store    toolgateway.Store
	writeErr func(w http.ResponseWriter, status int, code, message string)
}

// NewToolsHandler creates a new ToolsHandler.
func NewToolsHandler(registry toolgateway.ToolRegistry, store toolgateway.Store, writeErr func(w http.ResponseWriter, status int, code, message string)) *ToolsHandler {
	return &ToolsHandler{
		registry: registry,
		store:    store,
		writeErr: writeErr,
	}
}

func (h *ToolsHandler) getTenantID(c *gin.Context) (uuid.UUID, bool) {
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok || identity.TenantID == uuid.Nil {
		h.writeErr(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return uuid.Nil, false
	}
	return identity.TenantID, true
}

// RegisterToolHandler handles POST /api/v1/tools
func (h *ToolsHandler) RegisterToolHandler(c *gin.Context) {
	tenantID, ok := h.getTenantID(c)
	if !ok {
		return
	}

	var req toolgateway.ToolDefinition
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
		return
	}

	// Tenant isolation: override tenant ID from authenticated context
	req.TenantID = tenantID

	tool, err := h.registry.RegisterTool(c.Request.Context(), req)
	if err != nil {
		if errors.Is(err, toolgateway.ErrInvalidToolDefinition) {
			h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
			return
		}
		h.writeErr(c.Writer, http.StatusBadRequest, "REGISTRATION_FAILED", err.Error())
		return
	}

	c.JSON(http.StatusCreated, tool)
}

// ListToolsHandler handles GET /api/v1/tools
func (h *ToolsHandler) ListToolsHandler(c *gin.Context) {
	tenantID, ok := h.getTenantID(c)
	if !ok {
		return
	}

	tools, err := h.registry.ListTools(c.Request.Context(), tenantID)
	if err != nil {
		h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	if tools == nil {
		tools = []toolgateway.ToolDefinition{}
	}

	c.JSON(http.StatusOK, tools)
}

// GetToolHandler handles GET /api/v1/tools/:id
func (h *ToolsHandler) GetToolHandler(c *gin.Context) {
	tenantID, ok := h.getTenantID(c)
	if !ok {
		return
	}

	id := c.Param("id")
	if id == "" {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "tool id is required")
		return
	}

	tool, err := h.registry.GetTool(c.Request.Context(), tenantID, id)
	if err != nil {
		if errors.Is(err, toolgateway.ErrToolNotFound) {
			h.writeErr(c.Writer, http.StatusNotFound, "NOT_FOUND", "tool not found")
			return
		}
		h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	c.JSON(http.StatusOK, tool)
}

// UpdateToolHandler handles PUT /api/v1/tools/:id
func (h *ToolsHandler) UpdateToolHandler(c *gin.Context) {
	tenantID, ok := h.getTenantID(c)
	if !ok {
		return
	}

	id := c.Param("id")
	if id == "" {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "tool id is required")
		return
	}

	var updates toolgateway.ToolUpdate
	if err := json.NewDecoder(c.Request.Body).Decode(&updates); err != nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
		return
	}

	tool, err := h.registry.UpdateTool(c.Request.Context(), tenantID, id, updates)
	if err != nil {
		if errors.Is(err, toolgateway.ErrToolNotFound) {
			h.writeErr(c.Writer, http.StatusNotFound, "NOT_FOUND", "tool not found")
			return
		}
		if errors.Is(err, toolgateway.ErrInvalidToolDefinition) {
			h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
			return
		}
		h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	c.JSON(http.StatusOK, tool)
}

// EnableToolHandler handles PATCH /api/v1/tools/:id/enable
func (h *ToolsHandler) EnableToolHandler(c *gin.Context) {
	tenantID, ok := h.getTenantID(c)
	if !ok {
		return
	}

	id := c.Param("id")
	if id == "" {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "tool id is required")
		return
	}

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
		return
	}

	tool, err := h.registry.SetToolEnabled(c.Request.Context(), tenantID, id, body.Enabled)
	if err != nil {
		if errors.Is(err, toolgateway.ErrToolNotFound) {
			h.writeErr(c.Writer, http.StatusNotFound, "NOT_FOUND", "tool not found")
			return
		}
		h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	c.JSON(http.StatusOK, tool)
}

// DeleteToolHandler handles DELETE /api/v1/tools/:id
func (h *ToolsHandler) DeleteToolHandler(c *gin.Context) {
	tenantID, ok := h.getTenantID(c)
	if !ok {
		return
	}

	id := c.Param("id")
	if id == "" {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "tool id is required")
		return
	}

	if err := h.registry.DeleteTool(c.Request.Context(), tenantID, id); err != nil {
		if errors.Is(err, toolgateway.ErrToolNotFound) {
			h.writeErr(c.Writer, http.StatusNotFound, "NOT_FOUND", "tool not found")
			return
		}
		h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	c.Status(http.StatusNoContent)
}

// ListAuditEventsHandler handles GET /api/v1/tools/audit
func (h *ToolsHandler) ListAuditEventsHandler(c *gin.Context) {
	tenantID, ok := h.getTenantID(c)
	if !ok {
		return
	}

	limit := 50
	if lStr := c.Query("limit"); lStr != "" {
		if parsed, err := strconv.Atoi(lStr); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	events, err := h.store.ListAuditEvents(c.Request.Context(), tenantID, limit)
	if err != nil {
		h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	if events == nil {
		events = []toolgateway.AuditEvent{}
	}

	c.JSON(http.StatusOK, events)
}
