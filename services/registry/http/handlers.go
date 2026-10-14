package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/aswinsreeraj/arda/services/registry/service"
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
func (h *Handler) CreateAgentHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantIDFromContext(r.Context())
	if !ok {
		writeErrorResponse(w, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	var req service.CreateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, domain.NewValidationError("invalid json request body", err.Error()))
		return
	}

	detail, err := h.svc.CreateAgent(r.Context(), tenantID, req)
	if err != nil {
		writeErrorResponse(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, detail)
}

// GetAgentHandler handles GET /api/v1/agents/{id}
func (h *Handler) GetAgentHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantIDFromContext(r.Context())
	if !ok {
		writeErrorResponse(w, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	idStr := r.PathValue("id")
	agentID, err := uuid.Parse(idStr)
	if err != nil {
		writeErrorResponse(w, domain.NewValidationError("invalid agent id: must be a valid uuid"))
		return
	}

	detail, err := h.svc.GetAgent(r.Context(), tenantID, agentID)
	if err != nil {
		writeErrorResponse(w, err)
		return
	}

	writeJSON(w, http.StatusOK, detail)
}

// CreateVersionHandler handles POST /api/v1/agents/{id}/versions
func (h *Handler) CreateVersionHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantIDFromContext(r.Context())
	if !ok {
		writeErrorResponse(w, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	idStr := r.PathValue("id")
	agentID, err := uuid.Parse(idStr)
	if err != nil {
		writeErrorResponse(w, domain.NewValidationError("invalid agent id: must be a valid uuid"))
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

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErrorResponse(w, domain.NewValidationError("invalid json request body", err.Error()))
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

	version, err := h.svc.CreateDraftVersion(r.Context(), tenantID, agentID, cfg)
	if err != nil {
		writeErrorResponse(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, version)
}

// PublishVersionHandler handles POST /api/v1/agents/{id}/versions/{versionId}/publish
func (h *Handler) PublishVersionHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantIDFromContext(r.Context())
	if !ok {
		writeErrorResponse(w, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	agentIDStr := r.PathValue("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		writeErrorResponse(w, domain.NewValidationError("invalid agent id: must be a valid uuid"))
		return
	}

	versionIDStr := r.PathValue("versionId")
	versionID, err := uuid.Parse(versionIDStr)
	if err != nil {
		writeErrorResponse(w, domain.NewValidationError("invalid version id: must be a valid uuid"))
		return
	}

	version, err := h.svc.PublishVersion(r.Context(), tenantID, agentID, versionID)
	if err != nil {
		writeErrorResponse(w, err)
		return
	}

	writeJSON(w, http.StatusOK, version)
}

// GetLatestPublishedVersionHandler handles GET /api/v1/agents/{id}/versions/latest
func (h *Handler) GetLatestPublishedVersionHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantIDFromContext(r.Context())
	if !ok {
		writeErrorResponse(w, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	agentIDStr := r.PathValue("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		writeErrorResponse(w, domain.NewValidationError("invalid agent id: must be a valid uuid"))
		return
	}

	version, err := h.svc.GetLatestPublishedVersion(r.Context(), tenantID, agentID)
	if err != nil {
		writeErrorResponse(w, err)
		return
	}

	writeJSON(w, http.StatusOK, version)
}

// GetVersionHandler handles GET /api/v1/agents/{id}/versions/{versionId}
func (h *Handler) GetVersionHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantIDFromContext(r.Context())
	if !ok {
		writeErrorResponse(w, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	agentIDStr := r.PathValue("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		writeErrorResponse(w, domain.NewValidationError("invalid agent id: must be a valid uuid"))
		return
	}

	versionIDStr := r.PathValue("versionId")
	versionID, err := uuid.Parse(versionIDStr)
	if err != nil {
		writeErrorResponse(w, domain.NewValidationError("invalid version id: must be a valid uuid"))
		return
	}

	version, err := h.svc.GetVersion(r.Context(), tenantID, agentID, versionID)
	if err != nil {
		writeErrorResponse(w, err)
		return
	}

	writeJSON(w, http.StatusOK, version)
}

// UpdateVersionHandler handles PUT /api/v1/agents/{id}/versions/{versionId}
// Rejects modification if the version is published.
func (h *Handler) UpdateVersionHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantIDFromContext(r.Context())
	if !ok {
		writeErrorResponse(w, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	agentIDStr := r.PathValue("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		writeErrorResponse(w, domain.NewValidationError("invalid agent id: must be a valid uuid"))
		return
	}

	versionIDStr := r.PathValue("versionId")
	versionID, err := uuid.Parse(versionIDStr)
	if err != nil {
		writeErrorResponse(w, domain.NewValidationError("invalid version id: must be a valid uuid"))
		return
	}

	var cfg domain.Configuration
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeErrorResponse(w, domain.NewValidationError("invalid json request body", err.Error()))
		return
	}

	ver, err := h.svc.UpdateDraftVersion(r.Context(), tenantID, agentID, versionID, cfg)
	if err != nil {
		writeErrorResponse(w, err)
		return
	}

	writeJSON(w, http.StatusOK, ver)
}

// DeleteVersionHandler handles DELETE /api/v1/agents/{id}/versions/{versionId}
// Rejects deletion if the version is published.
func (h *Handler) DeleteVersionHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantIDFromContext(r.Context())
	if !ok {
		writeErrorResponse(w, domain.NewUnauthenticatedError("missing tenant context"))
		return
	}

	agentIDStr := r.PathValue("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		writeErrorResponse(w, domain.NewValidationError("invalid agent id: must be a valid uuid"))
		return
	}

	versionIDStr := r.PathValue("versionId")
	versionID, err := uuid.Parse(versionIDStr)
	if err != nil {
		writeErrorResponse(w, domain.NewValidationError("invalid version id: must be a valid uuid"))
		return
	}

	if err := h.svc.DeleteDraftVersion(r.Context(), tenantID, agentID, versionID); err != nil {
		writeErrorResponse(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Response helpers

type errorPayload struct {
	Error domain.DomainError `json:"error"`
}

func writeErrorResponse(w http.ResponseWriter, err error) {
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

	writeJSON(w, statusCode, errorPayload{Error: *domErr})
}

func writeJSON(w http.ResponseWriter, statusCode int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(data)
}
