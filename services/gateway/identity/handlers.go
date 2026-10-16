package identity

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Handler handles HTTP requests for identity, authentication, and tenant membership operations.
type Handler struct {
	svc      *Service
	writeErr func(w http.ResponseWriter, status int, code, message string)
}

// NewHandler creates a new identity Handler.
func NewHandler(svc *Service, writeErr func(w http.ResponseWriter, status int, code, message string)) *Handler {
	return &Handler{
		svc:      svc,
		writeErr: writeErr,
	}
}

func (h *Handler) getIdentity(c *gin.Context) (auth.Identity, bool) {
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok || identity.UserID == "" {
		h.writeErr(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return auth.Identity{}, false
	}
	return identity, true
}

// RegisterHandler handles POST /api/v1/auth/register
func (h *Handler) RegisterHandler(c *gin.Context) {
	var req RegisterRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
		return
	}

	orgName := req.TenantName
	if orgName == "" {
		orgName = req.OrganizationName
	}

	if orgName != "" {
		res, err := h.svc.RegisterTenant(c.Request.Context(), RegisterTenantRequest{
			CompanyName: orgName,
			Email:       req.Email,
			Password:    req.Password,
			DisplayName: req.DisplayName,
		})
		if err != nil {
			if errors.Is(err, ErrUserAlreadyExists) {
				h.writeErr(c.Writer, http.StatusConflict, "USER_ALREADY_EXISTS", "user with this email already exists")
				return
			}
			if errors.Is(err, ErrInvalidEmail) || errors.Is(err, ErrWeakPassword) {
				h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
				return
			}
			h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
			return
		}
		c.JSON(http.StatusCreated, res)
		return
	}

	user, err := h.svc.Register(c.Request.Context(), req)
	if err != nil {
		if errors.Is(err, ErrUserAlreadyExists) {
			h.writeErr(c.Writer, http.StatusConflict, "USER_ALREADY_EXISTS", "user with this email already exists")
			return
		}
		if errors.Is(err, ErrInvalidEmail) || errors.Is(err, ErrWeakPassword) {
			h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
			return
		}
		h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create user account")
		return
	}

	c.JSON(http.StatusCreated, user)
}

// RegisterTenantHandler handles POST /api/v1/tenants/register
func (h *Handler) RegisterTenantHandler(c *gin.Context) {
	var req RegisterTenantRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
		return
	}

	res, err := h.svc.RegisterTenant(c.Request.Context(), req)
	if err != nil {
		if errors.Is(err, ErrUserAlreadyExists) {
			h.writeErr(c.Writer, http.StatusConflict, "USER_ALREADY_EXISTS", "user with this email already exists")
			return
		}
		if errors.Is(err, ErrInvalidEmail) || errors.Is(err, ErrWeakPassword) {
			h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
			return
		}
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}

	c.JSON(http.StatusCreated, res)
}

// LoginHandler handles POST /api/v1/auth/login
func (h *Handler) LoginHandler(c *gin.Context) {
	var req LoginRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
		return
	}

	res, err := h.svc.Login(c.Request.Context(), req)
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			h.writeErr(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid email or password")
			return
		}
		if errors.Is(err, ErrAccountDisabled) {
			h.writeErr(c.Writer, http.StatusForbidden, "ACCOUNT_DISABLED", "user account is disabled")
			return
		}
		if errors.Is(err, ErrNoActiveMembership) {
			h.writeErr(c.Writer, http.StatusForbidden, "NO_ACTIVE_MEMBERSHIP", "user has no active tenant memberships")
			return
		}
		if errors.Is(err, ErrUnauthorizedTenant) {
			h.writeErr(c.Writer, http.StatusForbidden, "UNAUTHORIZED_TENANT", "unauthorized or inactive membership for requested tenant")
			return
		}
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}

	c.JSON(http.StatusOK, res)
}

// GetCurrentUserHandler handles GET /api/v1/auth/me
func (h *Handler) GetCurrentUserHandler(c *gin.Context) {
	identity, ok := h.getIdentity(c)
	if !ok {
		return
	}

	res, err := h.svc.GetCurrentUser(c.Request.Context(), identity)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			h.writeErr(c.Writer, http.StatusNotFound, "NOT_FOUND", "user account not found")
			return
		}
		h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to retrieve user profile")
		return
	}

	c.JSON(http.StatusOK, res)
}

// CreateTenantHandler handles POST /api/v1/tenants
func (h *Handler) CreateTenantHandler(c *gin.Context) {
	identity, ok := h.getIdentity(c)
	if !ok {
		return
	}

	var req CreateTenantRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
		return
	}

	tenant, membership, err := h.svc.CreateTenant(c.Request.Context(), identity, req)
	if err != nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"tenant":     tenant,
		"membership": membership,
	})
}

// AddTenantMemberHandler handles POST /api/v1/tenants/:tenantId/members
func (h *Handler) AddTenantMemberHandler(c *gin.Context) {
	identity, ok := h.getIdentity(c)
	if !ok {
		return
	}

	tenantIDStr := c.Param("tenantId")
	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil || tenantID == uuid.Nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid tenantId parameter: must be a valid UUID")
		return
	}

	var req AddMemberRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
		return
	}

	membership, err := h.svc.AddTenantMember(c.Request.Context(), identity, tenantID, req)
	if err != nil {
		if errors.Is(err, ErrPermissionDenied) {
			h.writeErr(c.Writer, http.StatusForbidden, "PERMISSION_DENIED", "only tenant administrators can manage members")
			return
		}
		if errors.Is(err, ErrMembershipAlreadyExists) {
			h.writeErr(c.Writer, http.StatusConflict, "MEMBERSHIP_EXISTS", "user is already a member of this tenant")
			return
		}
		if errors.Is(err, ErrUserNotFound) || errors.Is(err, ErrTenantNotFound) {
			h.writeErr(c.Writer, http.StatusNotFound, "NOT_FOUND", err.Error())
			return
		}
		if errors.Is(err, ErrCannotAssignSuperAdmin) || errors.Is(err, ErrInvalidRole) {
			h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
			return
		}
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}

	c.JSON(http.StatusCreated, membership)
}

// ListTenantMembersHandler handles GET /api/v1/tenants/:tenantId/members
func (h *Handler) ListTenantMembersHandler(c *gin.Context) {
	identity, ok := h.getIdentity(c)
	if !ok {
		return
	}

	tenantIDStr := c.Param("tenantId")
	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil || tenantID == uuid.Nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid tenantId parameter: must be a valid UUID")
		return
	}

	members, err := h.svc.ListTenantMembers(c.Request.Context(), identity, tenantID)
	if err != nil {
		if errors.Is(err, ErrPermissionDenied) {
			h.writeErr(c.Writer, http.StatusForbidden, "PERMISSION_DENIED", "access denied to tenant members")
			return
		}
		h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list tenant members")
		return
	}

	c.JSON(http.StatusOK, gin.H{"members": members})
}

// UpdateTenantMemberHandler handles PATCH /api/v1/tenants/:tenantId/members/:userId
func (h *Handler) UpdateTenantMemberHandler(c *gin.Context) {
	identity, ok := h.getIdentity(c)
	if !ok {
		return
	}

	tenantIDStr := c.Param("tenantId")
	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil || tenantID == uuid.Nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid tenantId parameter")
		return
	}

	userIDStr := c.Param("userId")
	userID, err := uuid.Parse(userIDStr)
	if err != nil || userID == uuid.Nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid userId parameter")
		return
	}

	var req UpdateMemberRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
		return
	}

	membership, err := h.svc.UpdateTenantMember(c.Request.Context(), identity, tenantID, userID, req)
	if err != nil {
		if errors.Is(err, ErrPermissionDenied) {
			h.writeErr(c.Writer, http.StatusForbidden, "PERMISSION_DENIED", "only tenant administrators can manage members")
			return
		}
		if errors.Is(err, ErrMembershipNotFound) {
			h.writeErr(c.Writer, http.StatusNotFound, "NOT_FOUND", "membership not found")
			return
		}
		if errors.Is(err, ErrCannotRemoveLastAdmin) {
			h.writeErr(c.Writer, http.StatusConflict, "CANNOT_REMOVE_LAST_ADMIN", err.Error())
			return
		}
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}

	c.JSON(http.StatusOK, membership)
}

// DeleteTenantMemberHandler handles DELETE /api/v1/tenants/:tenantId/members/:userId
func (h *Handler) DeleteTenantMemberHandler(c *gin.Context) {
	identity, ok := h.getIdentity(c)
	if !ok {
		return
	}

	tenantIDStr := c.Param("tenantId")
	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil || tenantID == uuid.Nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid tenantId parameter")
		return
	}

	userIDStr := c.Param("userId")
	userID, err := uuid.Parse(userIDStr)
	if err != nil || userID == uuid.Nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid userId parameter")
		return
	}

	if err := h.svc.DeleteTenantMember(c.Request.Context(), identity, tenantID, userID); err != nil {
		if errors.Is(err, ErrPermissionDenied) {
			h.writeErr(c.Writer, http.StatusForbidden, "PERMISSION_DENIED", "only tenant administrators can manage members")
			return
		}
		if errors.Is(err, ErrMembershipNotFound) {
			h.writeErr(c.Writer, http.StatusNotFound, "NOT_FOUND", "membership not found")
			return
		}
		if errors.Is(err, ErrCannotRemoveLastAdmin) {
			h.writeErr(c.Writer, http.StatusConflict, "CANNOT_REMOVE_LAST_ADMIN", err.Error())
			return
		}
		h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to delete tenant member")
		return
	}

	c.Status(http.StatusNoContent)
}

// ListUsersHandler handles GET /api/v1/users
// RBAC:
//   company_admin: full (own tenant)
//   agent_developer / reviewer: self only
//   super_admin: - (denied)
func (h *Handler) ListUsersHandler(c *gin.Context) {
	identity, ok := h.getIdentity(c)
	if !ok {
		return
	}

	if identity.Role == auth.RoleCompanyAdmin {
		members, err := h.svc.ListTenantMembers(c.Request.Context(), identity, identity.TenantID)
		if err != nil {
			h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list tenant users")
			return
		}
		c.JSON(http.StatusOK, gin.H{"users": members})
		return
	}

	if identity.Role == auth.RoleAgentDeveloper || identity.Role == auth.RoleReviewer {
		me, err := h.svc.GetCurrentUser(c.Request.Context(), identity)
		if err != nil {
			h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to retrieve self profile")
			return
		}
		c.JSON(http.StatusOK, gin.H{"users": []SafeUser{me.User}})
		return
	}

	h.writeErr(c.Writer, http.StatusForbidden, "PERMISSION_DENIED", "super_admin is not authorized to list tenant users")
}

// GetUserHandler handles GET /api/v1/users/:userId
// RBAC:
//   company_admin: can view any member in own tenant
//   agent_developer / reviewer: self only ("me" or matching identity.UserID)
func (h *Handler) GetUserHandler(c *gin.Context) {
	identity, ok := h.getIdentity(c)
	if !ok {
		return
	}

	targetUserIDStr := c.Param("userId")
	if targetUserIDStr == "me" || targetUserIDStr == identity.UserID {
		me, err := h.svc.GetCurrentUser(c.Request.Context(), identity)
		if err != nil {
			h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to retrieve self profile")
			return
		}
		c.JSON(http.StatusOK, me)
		return
	}

	if identity.Role != auth.RoleCompanyAdmin {
		h.writeErr(c.Writer, http.StatusForbidden, "PERMISSION_DENIED", "role is restricted to self only for users endpoint")
		return
	}

	targetUserID, err := uuid.Parse(targetUserIDStr)
	if err != nil || targetUserID == uuid.Nil {
		h.writeErr(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid userId parameter")
		return
	}

	member, err := h.svc.GetTenantMember(c.Request.Context(), identity, identity.TenantID, targetUserID)
	if err != nil {
		if errors.Is(err, ErrMembershipNotFound) || errors.Is(err, ErrUserNotFound) {
			h.writeErr(c.Writer, http.StatusNotFound, "NOT_FOUND", "user not found in tenant")
			return
		}
		h.writeErr(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to retrieve user")
		return
	}

	c.JSON(http.StatusOK, member)
}

// CreateUserHandler handles POST /api/v1/users
func (h *Handler) CreateUserHandler(c *gin.Context) {
	identity, ok := h.getIdentity(c)
	if !ok {
		return
	}
	c.Params = append(c.Params, gin.Param{Key: "tenantId", Value: identity.TenantID.String()})
	h.AddTenantMemberHandler(c)
}

// UpdateUserHandler handles PATCH /api/v1/users/:userId
func (h *Handler) UpdateUserHandler(c *gin.Context) {
	identity, ok := h.getIdentity(c)
	if !ok {
		return
	}
	c.Params = append(c.Params, gin.Param{Key: "tenantId", Value: identity.TenantID.String()})
	h.UpdateTenantMemberHandler(c)
}

// DeleteUserHandler handles DELETE /api/v1/users/:userId
func (h *Handler) DeleteUserHandler(c *gin.Context) {
	identity, ok := h.getIdentity(c)
	if !ok {
		return
	}
	c.Params = append(c.Params, gin.Param{Key: "tenantId", Value: identity.TenantID.String()})
	h.DeleteTenantMemberHandler(c)
}

