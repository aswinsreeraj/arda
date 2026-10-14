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
	rootMux := http.NewServeMux()

	// Public health check route
	rootMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Protected API multiplexer
	apiMux := http.NewServeMux()

	// 1. Reliability Service Route
	reliabilityHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleReliabilityGate(w, r, cfg.ReliabilityService)
	})
	apiMux.Handle("POST /api/v1/reliability/gate",
		auth.RequireActionMiddleware(auth.ActionReliabilityGate, WriteErrorResponse)(reliabilityHandler),
	)

	// 2. Agent Registry Routes (if registry handler is provided)
	if cfg.RegistryHandler != nil {
		wrapRoute := func(action auth.Action, pattern string) {
			apiMux.Handle(pattern,
				auth.RequireActionMiddleware(action, WriteErrorResponse)(cfg.RegistryHandler),
			)
		}

		wrapRoute(auth.ActionAgentCreate, "POST /api/v1/agents")
		wrapRoute(auth.ActionAgentRead, "GET /api/v1/agents/{id}")
		wrapRoute(auth.ActionVersionCreate, "POST /api/v1/agents/{id}/versions")
		wrapRoute(auth.ActionVersionPublish, "POST /api/v1/agents/{id}/versions/{versionId}/publish")
		wrapRoute(auth.ActionAgentRead, "GET /api/v1/agents/{id}/versions/latest")
		wrapRoute(auth.ActionAgentRead, "GET /api/v1/agents/{id}/versions/published/latest")
		wrapRoute(auth.ActionAgentRead, "GET /api/v1/agents/{id}/versions/{versionId}")
		wrapRoute(auth.ActionVersionUpdate, "PUT /api/v1/agents/{id}/versions/{versionId}")
		wrapRoute(auth.ActionVersionDelete, "DELETE /api/v1/agents/{id}/versions/{versionId}")
	}

	// 3. Agent Execution Route
	apiMux.Handle("POST /api/v1/agents/{id}/execute",
		auth.RequireActionMiddleware(auth.ActionAgentExecute, WriteErrorResponse)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleExecuteAgent(w, r, cfg.RegistryService, cfg.TemporalClient)
		})),
	)

	// Build middleware chain:
	authMiddleware := auth.AuthenticateMiddleware(cfg.JWTValidator, WriteErrorResponse)
	rateLimitMiddleware := ratelimit.Middleware(cfg.RateLimiter, WriteErrorResponse)

	protectedChain := authMiddleware(apiMux)
	rateLimitedChain := rateLimitMiddleware(protectedChain)

	rootMux.Handle("/api/", rateLimitedChain)

	// Top-level logging for unauthenticated/public endpoints like /healthz
	return LoggingMiddleware(rootMux)
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

func handleReliabilityGate(w http.ResponseWriter, r *http.Request, relSvc proxy.ReliabilityService) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		WriteErrorResponse(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	if relSvc == nil {
		WriteErrorResponse(w, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "reliability service upstream is not configured")
		return
	}

	var reqBody gateRequestBody
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		WriteErrorResponse(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid request json body")
		return
	}

	// Validate inputs
	if _, err := uuid.Parse(reqBody.AgentID); err != nil {
		WriteErrorResponse(w, http.StatusBadRequest, "VALIDATION_FAILED", "agent_id must be a valid uuid")
		return
	}
	if strings.TrimSpace(reqBody.CaseDescription) == "" {
		WriteErrorResponse(w, http.StatusBadRequest, "VALIDATION_FAILED", "case_description cannot be empty")
		return
	}
	if strings.TrimSpace(reqBody.ProposedDecision) == "" {
		WriteErrorResponse(w, http.StatusBadRequest, "VALIDATION_FAILED", "proposed_decision cannot be empty")
		return
	}
	if reqBody.ReliabilityThreshold < 0.0 || reqBody.ReliabilityThreshold > 1.0 {
		WriteErrorResponse(w, http.StatusBadRequest, "VALIDATION_FAILED", "reliability_threshold must be between 0.0 and 1.0")
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

	grpcResp, err := relSvc.Gate(r.Context(), grpcReq)
	if err != nil {
		if errors.Is(err, proxy.ErrUnconfigured) {
			WriteErrorResponse(w, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "reliability service upstream is not configured")
			return
		}
		WriteErrorResponse(w, http.StatusBadGateway, "UPSTREAM_ERROR", "reliability service evaluation failed")
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

	WriteJSON(w, http.StatusOK, gateResponseBody{
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

func handleExecuteAgent(w http.ResponseWriter, r *http.Request, regSvc RegistryService, temporalClient WorkflowDispatcher) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		WriteErrorResponse(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	agentIDStr := r.PathValue("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		WriteErrorResponse(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid agent id: must be a valid uuid")
		return
	}

	var body executeRequestBody
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			WriteErrorResponse(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
			return
		}
	}

	if regSvc == nil {
		WriteErrorResponse(w, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "registry service is not configured")
		return
	}

	ver, err := regSvc.GetLatestPublishedVersion(r.Context(), identity.TenantID, agentID)
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
			WriteErrorResponse(w, statusCode, string(domErr.Code), domErr.Message)
			return
		}
		WriteErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to resolve agent version")
		return
	}

	if ver == nil {
		WriteErrorResponse(w, http.StatusNotFound, "NOT_FOUND", "no published version found for agent")
		return
	}

	if temporalClient == nil {
		WriteErrorResponse(w, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "temporal workflow client is not configured")
		return
	}

	wfInput := temporal.AgentExecutionInput{
		TenantID:       identity.TenantID,
		AgentID:        agentID,
		AgentVersionID: ver.ID,
		ExecutionInput: body.Input,
	}

	workflowID := fmt.Sprintf("agent-exec-%s-%s", agentID, uuid.New().String())
	run, err := temporalClient.StartAgentExecutionWorkflow(r.Context(), workflowID, wfInput)
	if err != nil {
		WriteErrorResponse(w, http.StatusBadGateway, "UPSTREAM_ERROR", "failed to dispatch execution workflow to temporal")
		return
	}

	resp := ExecuteAgentResponse{
		WorkflowID:     run.GetID(),
		RunID:          run.GetRunID(),
		AgentID:        agentID,
		AgentVersionID: ver.ID,
		Status:         "ACCEPTED",
	}

	WriteJSON(w, http.StatusAccepted, resp)
}
