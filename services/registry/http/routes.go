package http

import (
	"net/http"
)

// RegisterRoutes builds and returns the http.Handler for the Agent Registry.
func RegisterRoutes(h *Handler, resolver TenantResolver) http.Handler {
	mux := http.NewServeMux()

	// Health check endpoint (public, unauthenticated)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Protected Agent Registry endpoints
	apiMux := http.NewServeMux()

	// Agent lifecycle routes
	apiMux.HandleFunc("POST /api/v1/agents", h.CreateAgentHandler)
	apiMux.HandleFunc("GET /api/v1/agents/{id}", h.GetAgentHandler)

	// Version lifecycle routes
	apiMux.HandleFunc("POST /api/v1/agents/{id}/versions", h.CreateVersionHandler)
	apiMux.HandleFunc("POST /api/v1/agents/{id}/versions/{versionId}/publish", h.PublishVersionHandler)
	apiMux.HandleFunc("GET /api/v1/agents/{id}/versions/latest", h.GetLatestPublishedVersionHandler)
	apiMux.HandleFunc("GET /api/v1/agents/{id}/versions/published/latest", h.GetLatestPublishedVersionHandler)
	apiMux.HandleFunc("PUT /api/v1/agents/{id}/versions/{versionId}", h.UpdateVersionHandler)
	apiMux.HandleFunc("DELETE /api/v1/agents/{id}/versions/{versionId}", h.DeleteVersionHandler)

	// Wrap apiMux with TenantMiddleware
	if resolver == nil {
		resolver = HeaderTenantResolver{}
	}
	protectedAPI := TenantMiddleware(resolver, apiMux)

	// Mount protectedAPI on root mux
	mux.Handle("/api/", protectedAPI)

	return mux
}
