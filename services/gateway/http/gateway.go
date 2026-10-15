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
	"github.com/aswinsreeraj/arda/services/gateway/proxy"
	"github.com/aswinsreeraj/arda/services/gateway/ratelimit"
	"github.com/aswinsreeraj/arda/services/registry/domain"
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
	RegistryHandler    http.Handler
	RegistryService    RegistryService
	ReliabilityService proxy.ReliabilityService
	TemporalClient     WorkflowDispatcher
}

// NewGateway creates and configures the API Gateway root http.Handler.
func NewGateway(cfg GatewayConfig) http.Handler {
	router := gin.New()
	router.Use(LoggingMiddleware())

	// Public health check route
	router.GET("/healthz", func(c *gin.Context) {
		WriteJSON(c.Writer, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Protected API route group
	api := router.Group("/api")
	api.Use(ratelimit.Middleware(cfg.RateLimiter, WriteErrorResponse))
	api.Use(auth.AuthenticateMiddleware(cfg.JWTValidator, WriteErrorResponse))

	// 1. Reliability Service Route
	api.POST("/v1/reliability/gate",
		auth.RequireActionMiddleware(auth.ActionReliabilityGate, WriteErrorResponse),
		func(c *gin.Context) {
			handleReliabilityGate(c, cfg.ReliabilityService)
		},
	)

	// 2. Agent Registry Routes (if registry handler is provided)
	if cfg.RegistryHandler != nil {
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
		wrapRoute("GET", "/v1/agents/:id", auth.ActionAgentRead)
		wrapRoute("POST", "/v1/agents/:id/versions", auth.ActionVersionCreate)
		wrapRoute("POST", "/v1/agents/:id/versions/:versionId/publish", auth.ActionVersionPublish)
		wrapRoute("GET", "/v1/agents/:id/versions/latest", auth.ActionAgentRead)
		wrapRoute("GET", "/v1/agents/:id/versions/published/latest", auth.ActionAgentRead)
		wrapRoute("GET", "/v1/agents/:id/versions/:versionId", auth.ActionAgentRead)
		wrapRoute("PUT", "/v1/agents/:id/versions/:versionId", auth.ActionVersionUpdate)
		wrapRoute("DELETE", "/v1/agents/:id/versions/:versionId", auth.ActionVersionDelete)
	}

	// 3. Agent Execution Route
	api.POST("/v1/agents/:id/execute",
		auth.RequireActionMiddleware(auth.ActionAgentExecute, WriteErrorResponse),
		func(c *gin.Context) {
			handleExecuteAgent(c, cfg.RegistryService, cfg.TemporalClient)
		},
	)

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
	Input map[string]any `json:"input,omitempty"`
}

// ExecuteAgentResponse represents the 202 Accepted response payload returned by agent execution dispatch.
type ExecuteAgentResponse struct {
	WorkflowID     string    `json:"workflow_id"`
	RunID          string    `json:"run_id"`
	AgentID        uuid.UUID `json:"agent_id"`
	AgentVersionID uuid.UUID `json:"agent_version_id"`
	Status         string    `json:"status"`
}

func handleExecuteAgent(c *gin.Context, regSvc RegistryService, temporalClient WorkflowDispatcher) {
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

	if regSvc == nil {
		WriteErrorResponse(c.Writer, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "registry service is not configured")
		return
	}

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

	if temporalClient == nil {
		WriteErrorResponse(c.Writer, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "temporal workflow client is not configured")
		return
	}

	wfInput := temporal.AgentExecutionInput{
		TenantID:       identity.TenantID,
		AgentID:        agentID,
		AgentVersionID: ver.ID,
		ExecutionInput: body.Input,
	}

	workflowID := fmt.Sprintf("agent-exec-%s-%s", agentID, uuid.New().String())
	run, err := temporalClient.StartAgentExecutionWorkflow(c.Request.Context(), workflowID, wfInput)
	if err != nil {
		WriteErrorResponse(c.Writer, http.StatusBadGateway, "UPSTREAM_ERROR", "failed to dispatch execution workflow to temporal")
		return
	}

	resp := ExecuteAgentResponse{
		WorkflowID:     run.GetID(),
		RunID:          run.GetRunID(),
		AgentID:        agentID,
		AgentVersionID: ver.ID,
		Status:         "ACCEPTED",
	}

	WriteJSON(c.Writer, http.StatusAccepted, resp)
}
