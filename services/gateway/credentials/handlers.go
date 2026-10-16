package credentials

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Handler handles HTTP requests for credential management.
type Handler struct {
	svc      *Service
	writeErr func(w http.ResponseWriter, status int, code, message string)
}

// NewHandler creates a new credentials HTTP handler.
func NewHandler(svc *Service, writeErr func(w http.ResponseWriter, status int, code, message string)) *Handler {
	return &Handler{
		svc:      svc,
		writeErr: writeErr,
	}
}

func (h *Handler) getTenantID(c *gin.Context) (uuid.UUID, bool) {
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok || identity.TenantID == uuid.Nil {
		h.writeErr(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return uuid.Nil, false
	}
	return identity.TenantID, true
}

// CreateCredentialHandler handles POST /api/v1/credentials
func (h *Handler) CreateCredentialHandler(c *gin.Context) {
	tenantID, ok := h.getTenantID(c)
	if !ok {
		return
	}

	var req SaveCredentialRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
		return
	}

	cred, err := h.svc.SaveCredential(c.Request.Context(), tenantID, req)
	if err != nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}

	c.JSON(http.StatusCreated, cred)
}

// ListCredentialsHandler handles GET /api/v1/credentials
func (h *Handler) ListCredentialsHandler(c *gin.Context) {
	tenantID, ok := h.getTenantID(c)
	if !ok {
		return
	}

	creds, err := h.svc.ListCredentials(c.Request.Context(), tenantID)
	if err != nil {
		h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list credentials")
		return
	}

	c.JSON(http.StatusOK, gin.H{"credentials": creds})
}

// GetCredentialHandler handles GET /api/v1/credentials/:id
func (h *Handler) GetCredentialHandler(c *gin.Context) {
	tenantID, ok := h.getTenantID(c)
	if !ok {
		return
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid credential id: must be a valid uuid")
		return
	}

	cred, err := h.svc.GetCredential(c.Request.Context(), tenantID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			h.writeErr(c.Writer, http.StatusNotFound, "NOT_FOUND", "credential not found")
			return
		}
		h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get credential")
		return
	}

	c.JSON(http.StatusOK, cred)
}

// DeleteCredentialHandler handles DELETE /api/v1/credentials/:id
func (h *Handler) DeleteCredentialHandler(c *gin.Context) {
	tenantID, ok := h.getTenantID(c)
	if !ok {
		return
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid credential id: must be a valid uuid")
		return
	}

	if err := h.svc.DeleteCredential(c.Request.Context(), tenantID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			h.writeErr(c.Writer, http.StatusNotFound, "NOT_FOUND", "credential not found")
			return
		}
		h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to delete credential")
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// VerifyCredentialHandler handles POST /api/v1/credentials/:id/verify
func (h *Handler) VerifyCredentialHandler(c *gin.Context) {
	tenantID, ok := h.getTenantID(c)
	if !ok {
		return
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid credential id: must be a valid uuid")
		return
	}

	resp, err := h.svc.VerifyCredential(c.Request.Context(), tenantID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			h.writeErr(c.Writer, http.StatusNotFound, "NOT_FOUND", "credential not found")
			return
		}
		h.writeErr(c.Writer, http.StatusBadGateway, "UPSTREAM_ERROR", err.Error())
		return
	}

	c.JSON(http.StatusOK, resp)
}
