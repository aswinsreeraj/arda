package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RegisterRoutes builds and returns the *gin.Engine for the Agent Registry.
func RegisterRoutes(h *Handler, resolver TenantResolver) *gin.Engine {
	router := gin.New()

	// Health check endpoint (public, unauthenticated)
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Protected Agent Registry endpoints
	if resolver == nil {
		resolver = HeaderTenantResolver{}
	}

	api := router.Group("/api")
	api.Use(TenantMiddleware(resolver))

	// Agent lifecycle routes
	api.POST("/v1/agents", h.CreateAgentHandler)
	api.GET("/v1/agents/:id", h.GetAgentHandler)

	// Version lifecycle routes
	api.POST("/v1/agents/:id/versions", h.CreateVersionHandler)
	api.POST("/v1/agents/:id/versions/:versionId/publish", h.PublishVersionHandler)
	api.GET("/v1/agents/:id/versions/latest", h.GetLatestPublishedVersionHandler)
	api.GET("/v1/agents/:id/versions/published", h.GetLatestPublishedVersionHandler)
	api.GET("/v1/agents/:id/versions/published/latest", h.GetLatestPublishedVersionHandler)
	api.GET("/v1/agents/:id/versions/:versionId", h.GetVersionHandler)
	api.PUT("/v1/agents/:id/versions/:versionId", h.UpdateVersionHandler)
	api.DELETE("/v1/agents/:id/versions/:versionId", h.DeleteVersionHandler)

	return router
}
