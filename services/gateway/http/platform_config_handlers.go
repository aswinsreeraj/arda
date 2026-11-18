package http

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/aswinsreeraj/arda/pkg/platformconfig"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/aswinsreeraj/arda/services/gateway/identity"
	"github.com/aswinsreeraj/arda/services/gateway/ratelimit"
	"github.com/gin-gonic/gin"
)

// RetentionUpdater updates the Temporal namespace workflow execution retention TTL.
type RetentionUpdater interface {
	UpdateRetention(ctx context.Context, days int) error
}

// PlatformConfigHandler serves Super Admin platform configuration & audit activity endpoints
// and applies changes live to running infrastructure (RateLimiter, Temporal retention, etc.).
type PlatformConfigHandler struct {
	store            platformconfig.Store
	rateLimiter      *ratelimit.Limiter
	retentionUpdater RetentionUpdater
	identityService  *identity.Service
}

// NewPlatformConfigHandler constructs a PlatformConfigHandler.
func NewPlatformConfigHandler(
	store platformconfig.Store,
	rateLimiter *ratelimit.Limiter,
	retentionUpdater RetentionUpdater,
	identityService *identity.Service,
) *PlatformConfigHandler {
	return &PlatformConfigHandler{
		store:            store,
		rateLimiter:      rateLimiter,
		retentionUpdater: retentionUpdater,
		identityService:  identityService,
	}
}

// GetPlatformConfigHandler handles GET /api/v1/platform/config
func (h *PlatformConfigHandler) GetPlatformConfigHandler(c *gin.Context) {
	if h == nil || h.store == nil {
		WriteErrorResponse(c.Writer, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "platform config store is not available")
		return
	}

	items, err := h.store.List(c.Request.Context())
	if err != nil {
		WriteErrorResponse(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load platform configuration")
		return
	}
	snap := h.store.GetSnapshot(c.Request.Context())

	WriteJSON(c.Writer, http.StatusOK, gin.H{
		"configs":  items,
		"snapshot": snap,
	})
}

// UpdatePlatformConfigHandler handles PUT /api/v1/platform/config
func (h *PlatformConfigHandler) UpdatePlatformConfigHandler(c *gin.Context) {
	if h == nil || h.store == nil {
		WriteErrorResponse(c.Writer, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "platform config store is not available")
		return
	}

	caller, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		WriteErrorResponse(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	var req struct {
		Configs []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"configs"`
		Updates map[string]string `json:"updates"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid JSON payload")
		return
	}

	updates := make(map[string]string)
	for k, v := range req.Updates {
		updates[strings.TrimSpace(k)] = v
	}
	for _, item := range req.Configs {
		if k := strings.TrimSpace(item.Key); k != "" {
			updates[k] = item.Value
		}
	}
	if len(updates) == 0 {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "no configuration updates provided")
		return
	}

	operator := caller.UserID
	if h.identityService != nil {
		if me, err := h.identityService.GetCurrentUser(c.Request.Context(), caller); err == nil && me != nil && strings.TrimSpace(me.User.Email) != "" {
			operator = me.User.Email
		}
	}

	items, changes, err := h.store.Update(c.Request.Context(), updates, operator)
	if err != nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}

	snap := h.store.GetSnapshot(c.Request.Context())
	var applied []string
	var warnings []string

	for _, ch := range changes {
		switch ch.Key {
		case platformconfig.KeyGatewayRateLimitDefault:
			if h.rateLimiter != nil {
				h.rateLimiter.UpdateRate(snap.RateLimitRPS, snap.RateLimitBurst)
				log.Printf("[PlatformConfig] Live rate limit updated: %d req/min (%.2f RPS, burst %d)", snap.RateLimitPerMinute, snap.RateLimitRPS, snap.RateLimitBurst)
				applied = append(applied, ch.Key)
			}
		case platformconfig.KeyRuntimeCheckpointRetention:
			if h.retentionUpdater != nil {
				tCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
				err := h.retentionUpdater.UpdateRetention(tCtx, snap.RetentionDays)
				cancel()
				if err != nil {
					log.Printf("[PlatformConfig] WARN: failed to update Temporal namespace retention: %v", err)
					warnings = append(warnings, "Saved retention policy to PostgreSQL, but Temporal namespace update failed: "+err.Error())
				} else {
					log.Printf("[PlatformConfig] Temporal namespace retention updated to %dd", snap.RetentionDays)
					applied = append(applied, ch.Key)
				}
			} else {
				warnings = append(warnings, "Saved retention policy to PostgreSQL (Temporal client is not attached).")
			}
		default:
			// calibration.sampling_interval, reliability.default_threshold, retrieval.top_k
			// are picked up dynamically by Gateway & Worker via platformconfig.Store.GetSnapshot().
			log.Printf("[PlatformConfig] Live setting updated: %s = %s", ch.Key, ch.NewVal)
			applied = append(applied, ch.Key)
		}
	}

	WriteJSON(c.Writer, http.StatusOK, gin.H{
		"configs":  items,
		"snapshot": snap,
		"applied":  applied,
		"warnings": warnings,
	})
}

// ListPlatformActivityHandler handles GET /api/v1/platform/activity
func (h *PlatformConfigHandler) ListPlatformActivityHandler(c *gin.Context) {
	if h == nil || h.store == nil {
		WriteErrorResponse(c.Writer, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "platform config store is not available")
		return
	}

	events, err := h.store.ListAuditEvents(c.Request.Context(), 100)
	if err != nil {
		WriteErrorResponse(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load platform activity")
		return
	}

	WriteJSON(c.Writer, http.StatusOK, gin.H{
		"events": events,
	})
}

// GetPlatformDefaultsHandler handles GET /api/v1/platform/defaults for authenticated tenant users
// so Agent Registry creation UIs can reflect the live platform default reliability threshold.
func (h *PlatformConfigHandler) GetPlatformDefaultsHandler(c *gin.Context) {
	if h == nil || h.store == nil {
		snap := platformconfig.DefaultSnapshot()
		WriteJSON(c.Writer, http.StatusOK, gin.H{
			"reliability_default_threshold": snap.DefaultReliabilityThreshold,
			"calibration_sampling_interval": snap.CalibrationSamplingInterval,
			"retrieval_top_k":               snap.RetrievalTopK,
		})
		return
	}

	snap := h.store.GetSnapshot(c.Request.Context())
	WriteJSON(c.Writer, http.StatusOK, gin.H{
		"reliability_default_threshold": snap.DefaultReliabilityThreshold,
		"calibration_sampling_interval": snap.CalibrationSamplingInterval,
		"retrieval_top_k":               snap.RetrievalTopK,
	})
}
