package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/aswinsreeraj/arda/services/gateway/credentials"
	"github.com/aswinsreeraj/arda/services/gateway/identity"
	"github.com/aswinsreeraj/arda/services/gateway/proxy"
	"github.com/aswinsreeraj/arda/services/gateway/ratelimit"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	registrygrpc "github.com/aswinsreeraj/arda/services/registry/grpc"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
)

// RegistryService defines the interface needed by API Gateway to query agent versions.
type RegistryService interface {
	GetLatestPublishedVersion(ctx context.Context, tenantID, agentID uuid.UUID) (*domain.AgentVersion, error)
}

// WorkflowDispatcher defines the interface needed by API Gateway to dispatch execution workflows.
type WorkflowDispatcher interface {
	StartAgentExecutionWorkflow(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error)
}

// GatewayConfig holds dependencies and configuration for API Gateway v1.
type GatewayConfig struct {
	JWTValidator       *auth.TokenValidator
	RateLimiter        *ratelimit.Limiter
	RegistryClient     ardav1.AgentRegistryServiceClient
	RegistryHandler    http.Handler
	RegistryService    RegistryService
	ReliabilityService proxy.ReliabilityService
	TemporalClient     WorkflowDispatcher
	CredentialHandler  *credentials.Handler
	IdentityHandler    *identity.Handler
}

// NewGateway creates and configures the API Gateway root http.Handler.
func NewGateway(cfg GatewayConfig) http.Handler {
	router := gin.New()
	router.Use(LoggingMiddleware())
	router.Use(func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		if origin != "" {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
		} else {
			c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		}
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-Tenant-ID")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	})

	// Public health check route
	router.GET("/healthz", func(c *gin.Context) {
		WriteJSON(c.Writer, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Public authentication endpoints (rate-limited, unauthenticated)
	if cfg.IdentityHandler != nil {
		publicAuth := router.Group("/api/v1/auth")
		publicAuth.Use(ratelimit.Middleware(cfg.RateLimiter, WriteErrorResponse))
		publicAuth.POST("/register", cfg.IdentityHandler.RegisterHandler)
		publicAuth.POST("/login", cfg.IdentityHandler.LoginHandler)

		publicTenants := router.Group("/api/v1/tenants")
		publicTenants.Use(ratelimit.Middleware(cfg.RateLimiter, WriteErrorResponse))
		publicTenants.POST("/register", cfg.IdentityHandler.RegisterTenantHandler)
	}

	// Protected API route group
	api := router.Group("/api")
	api.Use(ratelimit.Middleware(cfg.RateLimiter, WriteErrorResponse))
	api.Use(auth.AuthenticateMiddleware(cfg.JWTValidator, WriteErrorResponse))

	// Reliability Service
	api.POST("/v1/reliability/gate",
		auth.RequireActionMiddleware(auth.ActionReliabilityGate, WriteErrorResponse),
		func(c *gin.Context) {
			handleReliabilityGate(c, cfg.ReliabilityService)
		},
	)

	// Agent Registry Routes (gRPC client; fallback: HTTP handler)
	if cfg.RegistryClient != nil {
		api.POST("/v1/agents",
			auth.RequireActionMiddleware(auth.ActionAgentCreate, WriteErrorResponse),
			func(c *gin.Context) { handleCreateAgentGRPC(c, cfg.RegistryClient) },
		)
		api.GET("/v1/agents",
			auth.RequireActionMiddleware(auth.ActionAgentRead, WriteErrorResponse),
			func(c *gin.Context) { handleListAgentsGRPC(c, cfg.RegistryClient) },
		)
		api.GET("/v1/agents/:id",
			auth.RequireActionMiddleware(auth.ActionAgentRead, WriteErrorResponse),
			func(c *gin.Context) { handleGetAgentGRPC(c, cfg.RegistryClient) },
		)
		api.POST("/v1/agents/:id/versions",
			auth.RequireActionMiddleware(auth.ActionVersionCreate, WriteErrorResponse),
			func(c *gin.Context) { handleCreateVersionGRPC(c, cfg.RegistryClient) },
		)
		api.POST("/v1/agents/:id/versions/:versionId/publish",
			auth.RequireActionMiddleware(auth.ActionVersionPublish, WriteErrorResponse),
			func(c *gin.Context) { handlePublishVersionGRPC(c, cfg.RegistryClient) },
		)

		// Canonical route for latest published version
		api.GET("/v1/agents/:id/versions/latest",
			auth.RequireActionMiddleware(auth.ActionAgentRead, WriteErrorResponse),
			func(c *gin.Context) { handleGetLatestPublishedVersionGRPC(c, cfg.RegistryClient) },
		)
		// Direct aliases for latest published version
		api.GET("/v1/agents/:id/versions/published",
			auth.RequireActionMiddleware(auth.ActionAgentRead, WriteErrorResponse),
			func(c *gin.Context) { handleGetLatestPublishedVersionGRPC(c, cfg.RegistryClient) },
		)
		api.GET("/v1/agents/:id/versions/published/latest",
			auth.RequireActionMiddleware(auth.ActionAgentRead, WriteErrorResponse),
			func(c *gin.Context) { handleGetLatestPublishedVersionGRPC(c, cfg.RegistryClient) },
		)

		// Version retrieval by ID
		api.GET("/v1/agents/:id/versions/:versionId",
			auth.RequireActionMiddleware(auth.ActionAgentRead, WriteErrorResponse),
			func(c *gin.Context) { handleGetVersionGRPC(c, cfg.RegistryClient) },
		)
		api.PUT("/v1/agents/:id/versions/:versionId",
			auth.RequireActionMiddleware(auth.ActionVersionUpdate, WriteErrorResponse),
			func(c *gin.Context) { handleUpdateDraftVersionGRPC(c, cfg.RegistryClient) },
		)
		api.DELETE("/v1/agents/:id/versions/:versionId",
			auth.RequireActionMiddleware(auth.ActionVersionDelete, WriteErrorResponse),
			func(c *gin.Context) { handleDeleteDraftVersionGRPC(c, cfg.RegistryClient) },
		)
	} else if cfg.RegistryHandler != nil {
		forwardToRegistry := func(c *gin.Context) {
			cfg.RegistryHandler.ServeHTTP(c.Writer, c.Request)
		}

		wrapRoute := func(method, path string, action auth.Action) {
			api.Handle(method, path,
				auth.RequireActionMiddleware(action, WriteErrorResponse),
				forwardToRegistry,
			)
		}

		wrapRoute("POST", "/v1/agents", auth.ActionAgentCreate)
		wrapRoute("GET", "/v1/agents", auth.ActionAgentRead)
		wrapRoute("GET", "/v1/agents/:id", auth.ActionAgentRead)
		wrapRoute("POST", "/v1/agents/:id/versions", auth.ActionVersionCreate)
		wrapRoute("POST", "/v1/agents/:id/versions/:versionId/publish", auth.ActionVersionPublish)
		wrapRoute("GET", "/v1/agents/:id/versions/latest", auth.ActionAgentRead)
		wrapRoute("GET", "/v1/agents/:id/versions/published", auth.ActionAgentRead)
		wrapRoute("GET", "/v1/agents/:id/versions/published/latest", auth.ActionAgentRead)
		wrapRoute("GET", "/v1/agents/:id/versions/:versionId", auth.ActionAgentRead)
		wrapRoute("PUT", "/v1/agents/:id/versions/:versionId", auth.ActionVersionUpdate)
		wrapRoute("DELETE", "/v1/agents/:id/versions/:versionId", auth.ActionVersionDelete)
	}

	// Credential Management (Vault KV v2)
	if cfg.CredentialHandler != nil {
		api.POST("/v1/credentials",
			auth.RequireActionMiddleware(auth.ActionCredentialManage, WriteErrorResponse),
			cfg.CredentialHandler.CreateCredentialHandler,
		)
		api.GET("/v1/credentials",
			auth.RequireActionMiddleware(auth.ActionCredentialRead, WriteErrorResponse),
			cfg.CredentialHandler.ListCredentialsHandler,
		)
		api.GET("/v1/credentials/:id",
			auth.RequireActionMiddleware(auth.ActionCredentialRead, WriteErrorResponse),
			cfg.CredentialHandler.GetCredentialHandler,
		)
		api.DELETE("/v1/credentials/:id",
			auth.RequireActionMiddleware(auth.ActionCredentialManage, WriteErrorResponse),
			cfg.CredentialHandler.DeleteCredentialHandler,
		)
		api.POST("/v1/credentials/:id/verify",
			auth.RequireActionMiddleware(auth.ActionCredentialManage, WriteErrorResponse),
			cfg.CredentialHandler.VerifyCredentialHandler,
		)
	}

	// Agent Execution (Temporal)
	api.POST("/v1/agents/:id/execute",
		auth.RequireActionMiddleware(auth.ActionAgentExecute, WriteErrorResponse),
		func(c *gin.Context) {
			handleExecuteAgent(c, cfg.RegistryClient, cfg.RegistryService, cfg.TemporalClient)
		},
	)

	// Identity and Tenant Membership
	if cfg.IdentityHandler != nil {
		api.GET("/v1/auth/me", cfg.IdentityHandler.GetCurrentUserHandler)

		api.POST("/v1/tenants",
			auth.RequireActionMiddleware(auth.ActionTenantCreate, WriteErrorResponse),
			cfg.IdentityHandler.CreateTenantHandler,
		)
		api.POST("/v1/tenants/:tenantId/members",
			auth.RequireActionMiddleware(auth.ActionTenantMemberManage, WriteErrorResponse),
			cfg.IdentityHandler.AddTenantMemberHandler,
		)
		api.GET("/v1/tenants/:tenantId/members",
			auth.RequireActionMiddleware(auth.ActionTenantMemberRead, WriteErrorResponse),
			cfg.IdentityHandler.ListTenantMembersHandler,
		)
		api.PATCH("/v1/tenants/:tenantId/members/:userId",
			auth.RequireActionMiddleware(auth.ActionTenantMemberManage, WriteErrorResponse),
			cfg.IdentityHandler.UpdateTenantMemberHandler,
		)
		api.DELETE("/v1/tenants/:tenantId/members/:userId",
			auth.RequireActionMiddleware(auth.ActionTenantMemberManage, WriteErrorResponse),
			cfg.IdentityHandler.DeleteTenantMemberHandler,
		)

		// Users resource endpoints
		api.GET("/v1/users", cfg.IdentityHandler.ListUsersHandler)
		api.GET("/v1/users/:userId", cfg.IdentityHandler.GetUserHandler)
		api.POST("/v1/users",
			auth.RequireActionMiddleware(auth.ActionTenantMemberManage, WriteErrorResponse),
			cfg.IdentityHandler.CreateUserHandler,
		)
		api.PATCH("/v1/users/:userId",
			auth.RequireActionMiddleware(auth.ActionTenantMemberManage, WriteErrorResponse),
			cfg.IdentityHandler.UpdateUserHandler,
		)
		api.DELETE("/v1/users/:userId",
			auth.RequireActionMiddleware(auth.ActionTenantMemberManage, WriteErrorResponse),
			cfg.IdentityHandler.DeleteUserHandler,
		)
	}

	return router
}

type gateRequestBody struct {
	AgentID              string  `json:"agent_id"`
	CaseDescription      string  `json:"case_description"`
	ProposedDecision     string  `json:"proposed_decision"`
	ReliabilityThreshold float64 `json:"reliability_threshold"`
}

type neighborResponse struct {
	ReferenceExampleID string  `json:"reference_example_id"`
	Label              string  `json:"label"`
	Similarity         float64 `json:"similarity"`
}

type gateResponseBody struct {
	Confidence double             `json:"confidence"`
	Outcome    string             `json:"outcome"`
	ReasonCode string             `json:"reason_code"`
	Neighbors  []neighborResponse `json:"neighbors"`
}

type double = float64

func handleReliabilityGate(c *gin.Context, relSvc proxy.ReliabilityService) {
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		WriteErrorResponse(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	if relSvc == nil {
		WriteErrorResponse(c.Writer, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "reliability service upstream is not configured")
		return
	}

	var reqBody gateRequestBody
	if err := json.NewDecoder(c.Request.Body).Decode(&reqBody); err != nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid request json body")
		return
	}

	// Validate inputs
	if _, err := uuid.Parse(reqBody.AgentID); err != nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "agent_id must be a valid uuid")
		return
	}
	if strings.TrimSpace(reqBody.CaseDescription) == "" {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "case_description cannot be empty")
		return
	}
	if strings.TrimSpace(reqBody.ProposedDecision) == "" {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "proposed_decision cannot be empty")
		return
	}
	if reqBody.ReliabilityThreshold < 0.0 || reqBody.ReliabilityThreshold > 1.0 {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "reliability_threshold must be between 0.0 and 1.0")
		return
	}

	// Build gRPC request - tenant_id is strictly derived from verified JWT identity
	grpcReq := &ardav1.GateRequest{
		TenantId:             identity.TenantID.String(),
		AgentId:              reqBody.AgentID,
		CaseDescription:      reqBody.CaseDescription,
		ProposedDecision:     reqBody.ProposedDecision,
		ReliabilityThreshold: reqBody.ReliabilityThreshold,
	}

	grpcResp, err := relSvc.Gate(c.Request.Context(), grpcReq)
	if err != nil {
		if errors.Is(err, proxy.ErrUnconfigured) {
			WriteErrorResponse(c.Writer, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "reliability service upstream is not configured")
			return
		}
		WriteErrorResponse(c.Writer, http.StatusBadGateway, "UPSTREAM_ERROR", "reliability service evaluation failed")
		return
	}

	outcomeStr := grpcResp.GetOutcome().String()
	neighbors := make([]neighborResponse, 0, len(grpcResp.GetNeighbors()))
	for _, n := range grpcResp.GetNeighbors() {
		neighbors = append(neighbors, neighborResponse{
			ReferenceExampleID: n.GetReferenceExampleId(),
			Label:              n.GetLabel(),
			Similarity:         n.GetSimilarity(),
		})
	}

	WriteJSON(c.Writer, http.StatusOK, gateResponseBody{
		Confidence: grpcResp.GetConfidence(),
		Outcome:    outcomeStr,
		ReasonCode: grpcResp.GetReasonCode(),
		Neighbors:  neighbors,
	})
}

type executeRequestBody struct {
	AgentVersionID *uuid.UUID     `json:"agent_version_id,omitempty"`
	VersionID      *uuid.UUID     `json:"version_id,omitempty"`
	Input          map[string]any `json:"input,omitempty"`
	ExecutionMode  string         `json:"execution_mode,omitempty"`
	ExecutionID    string         `json:"execution_id,omitempty"`
}

// ExecuteAgentResponse represents the 202 Accepted response payload returned by agent execution dispatch.
type ExecuteAgentResponse struct {
	WorkflowID     string    `json:"workflow_id"`
	RunID          string    `json:"run_id"`
	AgentID        uuid.UUID `json:"agent_id"`
	AgentVersionID uuid.UUID `json:"agent_version_id"`
	Status         string    `json:"status"`
}

func handleExecuteAgent(c *gin.Context, regClient ardav1.AgentRegistryServiceClient, regSvc RegistryService, temporalClient WorkflowDispatcher) {
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		WriteErrorResponse(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	agentIDStr := c.Param("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid agent id: must be a valid uuid")
		return
	}

	var body executeRequestBody
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
			WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
			return
		}
	}

	if regClient == nil && regSvc == nil {
		WriteErrorResponse(c.Writer, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "registry service is not configured")
		return
	}

	var explicitVerID uuid.UUID
	if body.AgentVersionID != nil && *body.AgentVersionID != uuid.Nil {
		explicitVerID = *body.AgentVersionID
	} else if body.VersionID != nil && *body.VersionID != uuid.Nil {
		explicitVerID = *body.VersionID
	}

	var verID uuid.UUID
	if explicitVerID != uuid.Nil {
		verID = explicitVerID
		if regClient != nil {
			ctx := withInternalAuth(c.Request.Context(), identity)
			resp, err := regClient.GetAgentVersion(ctx, &ardav1.GetAgentVersionRequest{
				TenantId:  identity.TenantID.String(),
				AgentId:   agentID.String(),
				VersionId: explicitVerID.String(),
			})
			if err != nil {
				status, code, msg := registrygrpc.MapGRPCErrorToHTTP(err)
				WriteErrorResponse(c.Writer, status, code, msg)
				return
			}
			if resp == nil || resp.GetVersion() == nil {
				WriteErrorResponse(c.Writer, http.StatusNotFound, "NOT_FOUND", "agent version not found")
				return
			}
		} else if vg, ok := regSvc.(interface {
			GetVersion(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (*domain.AgentVersion, error)
		}); ok {
			ver, err := vg.GetVersion(c.Request.Context(), identity.TenantID, agentID, explicitVerID)
			if err != nil {
				var domErr *domain.DomainError
				if errors.As(err, &domErr) {
					statusCode := http.StatusInternalServerError
					switch domErr.Code {
					case domain.CodeNotFound:
						statusCode = http.StatusNotFound
					case domain.CodeValidationFailed:
						statusCode = http.StatusBadRequest
					case domain.CodeUnauthenticated:
						statusCode = http.StatusUnauthorized
					}
					WriteErrorResponse(c.Writer, statusCode, string(domErr.Code), domErr.Message)
					return
				}
				WriteErrorResponse(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to resolve agent version")
				return
			}
			if ver == nil {
				WriteErrorResponse(c.Writer, http.StatusNotFound, "NOT_FOUND", "agent version not found")
				return
			}
		}
	} else if regClient != nil {
		ctx := withInternalAuth(c.Request.Context(), identity)
		resp, err := regClient.GetLatestPublishedVersion(ctx, &ardav1.GetLatestPublishedVersionRequest{
			TenantId: identity.TenantID.String(),
			AgentId:  agentID.String(),
		})
		if err != nil {
			status, code, msg := registrygrpc.MapGRPCErrorToHTTP(err)
			WriteErrorResponse(c.Writer, status, code, msg)
			return
		}
		if resp == nil || resp.GetVersion() == nil {
			WriteErrorResponse(c.Writer, http.StatusNotFound, "NOT_FOUND", "no published version found for agent")
			return
		}
		parsedID, err := uuid.Parse(resp.GetVersion().GetId())
		if err != nil {
			WriteErrorResponse(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "invalid version id from registry")
			return
		}
		verID = parsedID
	} else {
		ver, err := regSvc.GetLatestPublishedVersion(c.Request.Context(), identity.TenantID, agentID)
		if err != nil {
			var domErr *domain.DomainError
			if errors.As(err, &domErr) {
				statusCode := http.StatusInternalServerError
				switch domErr.Code {
				case domain.CodeNotFound:
					statusCode = http.StatusNotFound
				case domain.CodeValidationFailed:
					statusCode = http.StatusBadRequest
				case domain.CodeUnauthenticated:
					statusCode = http.StatusUnauthorized
				}
				WriteErrorResponse(c.Writer, statusCode, string(domErr.Code), domErr.Message)
				return
			}
			WriteErrorResponse(c.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to resolve agent version")
			return
		}

		if ver == nil {
			WriteErrorResponse(c.Writer, http.StatusNotFound, "NOT_FOUND", "no published version found for agent")
			return
		}
		verID = ver.ID
	}

	if temporalClient == nil {
		WriteErrorResponse(c.Writer, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "temporal workflow client is not configured")
		return
	}

	wfInput := temporal.AgentExecutionInput{
		TenantID:       identity.TenantID,
		AgentID:        agentID,
		AgentVersionID: verID,
		ExecutionInput: body.Input,
	}

	workflowID := fmt.Sprintf("agent-exec-%s-%s", agentID, uuid.New().String())
	if strings.TrimSpace(body.ExecutionID) != "" {
		workflowID = fmt.Sprintf("agent-exec-%s-%s", agentID, strings.TrimSpace(body.ExecutionID))
	}
	run, err := temporalClient.StartAgentExecutionWorkflow(c.Request.Context(), workflowID, wfInput)
	if err != nil {
		WriteErrorResponse(c.Writer, http.StatusBadGateway, "UPSTREAM_ERROR", "failed to dispatch execution workflow to temporal")
		return
	}

	resp := ExecuteAgentResponse{
		WorkflowID:     run.GetID(),
		RunID:          run.GetRunID(),
		AgentID:        agentID,
		AgentVersionID: verID,
		Status:         "ACCEPTED",
	}

	WriteJSON(c.Writer, http.StatusAccepted, resp)
}
