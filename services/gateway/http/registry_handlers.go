package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	registrygrpc "github.com/aswinsreeraj/arda/services/registry/grpc"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
)

// withInternalAuth appends the verified caller identity to the outgoing gRPC context.
func withInternalAuth(ctx context.Context, id auth.Identity) context.Context {
	return metadata.AppendToOutgoingContext(ctx,
		"x-tenant-id", id.TenantID.String(),
		"x-user-id", id.UserID,
		"x-role", string(id.Role),
	)
}

func handleCreateAgentGRPC(c *gin.Context, client ardav1.AgentRegistryServiceClient) {
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		WriteErrorResponse(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	var body struct {
		Name          string                `json:"name"`
		Configuration *domain.Configuration `json:"configuration"`
		// Flat fields
		PromptTemplate       string               `json:"prompt_template"`
		Tools                []string             `json:"tools"`
		ReliabilityThreshold *float64             `json:"reliability_threshold"`
		ExecutionMode        domain.ExecutionMode `json:"execution_mode"`
		Guardrails           map[string]any       `json:"guardrails"`
		Budgets              map[string]any       `json:"budgets"`
	}

	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
		return
	}

	var protoCfg *ardav1.AgentConfiguration
	if body.Configuration != nil {
		var err error
		protoCfg, err = registrygrpc.DomainConfigToProto(*body.Configuration)
		if err != nil {
			WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
			return
		}
	} else if body.PromptTemplate != "" || len(body.Tools) > 0 || body.ReliabilityThreshold != nil || body.ExecutionMode != "" {
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
		cfg := domain.Configuration{
			PromptTemplate:       body.PromptTemplate,
			Tools:                tools,
			ReliabilityThreshold: threshold,
			ExecutionMode:        mode,
			Guardrails:           body.Guardrails,
			Budgets:              body.Budgets,
		}
		var err error
		protoCfg, err = registrygrpc.DomainConfigToProto(cfg)
		if err != nil {
			WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
			return
		}
	}

	ctx := withInternalAuth(c.Request.Context(), identity)
	req := &ardav1.CreateAgentRequest{
		TenantId:      identity.TenantID.String(),
		Name:          body.Name,
		Configuration: protoCfg,
	}

	resp, err := client.CreateAgent(ctx, req)
	if err != nil {
		status, code, msg := registrygrpc.MapGRPCErrorToHTTP(err)
		WriteErrorResponse(c.Writer, status, code, msg)
		return
	}

	detail := registrygrpc.ProtoToDetail(resp.GetAgent())
	WriteJSON(c.Writer, http.StatusCreated, detail)
}

func handleGetAgentGRPC(c *gin.Context, client ardav1.AgentRegistryServiceClient) {
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		WriteErrorResponse(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	agentIDStr := c.Param("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil || agentID == uuid.Nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid agent id: must be a valid uuid")
		return
	}

	ctx := withInternalAuth(c.Request.Context(), identity)
	req := &ardav1.GetAgentRequest{
		TenantId: identity.TenantID.String(),
		AgentId:  agentID.String(),
	}

	resp, err := client.GetAgent(ctx, req)
	if err != nil {
		status, code, msg := registrygrpc.MapGRPCErrorToHTTP(err)
		WriteErrorResponse(c.Writer, status, code, msg)
		return
	}

	detail := registrygrpc.ProtoToDetail(resp.GetAgent())
	WriteJSON(c.Writer, http.StatusOK, detail)
}

func handleListAgentsGRPC(c *gin.Context, client ardav1.AgentRegistryServiceClient) {
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		WriteErrorResponse(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	var pageSize int32
	if psStr := c.Query("page_size"); psStr != "" {
		if ps, err := strconv.Atoi(psStr); err == nil && ps > 0 {
			pageSize = int32(ps)
		}
	}

	pageToken := c.Query("page_token")

	ctx := withInternalAuth(c.Request.Context(), identity)
	req := &ardav1.ListAgentsRequest{
		TenantId: identity.TenantID.String(),
		Pagination: &ardav1.PaginationRequest{
			PageSize:  pageSize,
			PageToken: pageToken,
		},
	}

	resp, err := client.ListAgents(ctx, req)
	if err != nil {
		status, code, msg := registrygrpc.MapGRPCErrorToHTTP(err)
		WriteErrorResponse(c.Writer, status, code, msg)
		return
	}

	agents := make([]*domain.AgentDetail, 0, len(resp.GetAgents()))
	for _, a := range resp.GetAgents() {
		agents = append(agents, registrygrpc.ProtoToDetail(a))
	}

	var nextPageToken string
	var totalCount int32
	if p := resp.GetPagination(); p != nil {
		nextPageToken = p.GetNextPageToken()
		totalCount = p.GetTotalCount()
	}

	WriteJSON(c.Writer, http.StatusOK, gin.H{
		"agents":          agents,
		"next_page_token": nextPageToken,
		"total_count":     totalCount,
	})
}

func handleCreateVersionGRPC(c *gin.Context, client ardav1.AgentRegistryServiceClient) {
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		WriteErrorResponse(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	agentIDStr := c.Param("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil || agentID == uuid.Nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid agent id: must be a valid uuid")
		return
	}

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

	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
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

	protoCfg, err := registrygrpc.DomainConfigToProto(cfg)
	if err != nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}

	ctx := withInternalAuth(c.Request.Context(), identity)
	req := &ardav1.CreateAgentVersionRequest{
		TenantId:      identity.TenantID.String(),
		AgentId:       agentID.String(),
		Configuration: protoCfg,
	}

	resp, err := client.CreateAgentVersion(ctx, req)
	if err != nil {
		status, code, msg := registrygrpc.MapGRPCErrorToHTTP(err)
		WriteErrorResponse(c.Writer, status, code, msg)
		return
	}

	ver := registrygrpc.ProtoToVersion(resp.GetVersion())
	WriteJSON(c.Writer, http.StatusCreated, ver)
}

func handlePublishVersionGRPC(c *gin.Context, client ardav1.AgentRegistryServiceClient) {
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		WriteErrorResponse(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	agentIDStr := c.Param("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil || agentID == uuid.Nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid agent id: must be a valid uuid")
		return
	}

	versionIDStr := c.Param("versionId")
	versionID, err := uuid.Parse(versionIDStr)
	if err != nil || versionID == uuid.Nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid version id: must be a valid uuid")
		return
	}

	ctx := withInternalAuth(c.Request.Context(), identity)
	req := &ardav1.PublishVersionRequest{
		TenantId:  identity.TenantID.String(),
		AgentId:   agentID.String(),
		VersionId: versionID.String(),
	}

	resp, err := client.PublishVersion(ctx, req)
	if err != nil {
		status, code, msg := registrygrpc.MapGRPCErrorToHTTP(err)
		WriteErrorResponse(c.Writer, status, code, msg)
		return
	}

	ver := registrygrpc.ProtoToVersion(resp.GetVersion())
	WriteJSON(c.Writer, http.StatusOK, ver)
}

func handleGetLatestPublishedVersionGRPC(c *gin.Context, client ardav1.AgentRegistryServiceClient) {
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		WriteErrorResponse(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	agentIDStr := c.Param("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil || agentID == uuid.Nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid agent id: must be a valid uuid")
		return
	}

	ctx := withInternalAuth(c.Request.Context(), identity)
	req := &ardav1.GetLatestPublishedVersionRequest{
		TenantId: identity.TenantID.String(),
		AgentId:  agentID.String(),
	}

	resp, err := client.GetLatestPublishedVersion(ctx, req)
	if err != nil {
		status, code, msg := registrygrpc.MapGRPCErrorToHTTP(err)
		WriteErrorResponse(c.Writer, status, code, msg)
		return
	}

	ver := registrygrpc.ProtoToVersion(resp.GetVersion())
	WriteJSON(c.Writer, http.StatusOK, ver)
}

func handleGetVersionGRPC(c *gin.Context, client ardav1.AgentRegistryServiceClient) {
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		WriteErrorResponse(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	agentIDStr := c.Param("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil || agentID == uuid.Nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid agent id: must be a valid uuid")
		return
	}

	versionIDStr := c.Param("versionId")
	versionID, err := uuid.Parse(versionIDStr)
	if err != nil || versionID == uuid.Nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid version id: must be a valid uuid")
		return
	}

	ctx := withInternalAuth(c.Request.Context(), identity)
	req := &ardav1.GetAgentVersionRequest{
		TenantId:  identity.TenantID.String(),
		AgentId:   agentID.String(),
		VersionId: versionID.String(),
	}

	resp, err := client.GetAgentVersion(ctx, req)
	if err != nil {
		status, code, msg := registrygrpc.MapGRPCErrorToHTTP(err)
		WriteErrorResponse(c.Writer, status, code, msg)
		return
	}

	ver := registrygrpc.ProtoToVersion(resp.GetVersion())
	WriteJSON(c.Writer, http.StatusOK, ver)
}

func handleUpdateDraftVersionGRPC(c *gin.Context, client ardav1.AgentRegistryServiceClient) {
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		WriteErrorResponse(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	agentIDStr := c.Param("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil || agentID == uuid.Nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid agent id: must be a valid uuid")
		return
	}

	versionIDStr := c.Param("versionId")
	versionID, err := uuid.Parse(versionIDStr)
	if err != nil || versionID == uuid.Nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid version id: must be a valid uuid")
		return
	}

	var cfg domain.Configuration
	if err := json.NewDecoder(c.Request.Body).Decode(&cfg); err != nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
		return
	}

	protoCfg, err := registrygrpc.DomainConfigToProto(cfg)
	if err != nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}

	ctx := withInternalAuth(c.Request.Context(), identity)
	req := &ardav1.UpdateDraftVersionRequest{
		TenantId:      identity.TenantID.String(),
		AgentId:       agentID.String(),
		VersionId:     versionID.String(),
		Configuration: protoCfg,
	}

	resp, err := client.UpdateDraftVersion(ctx, req)
	if err != nil {
		status, code, msg := registrygrpc.MapGRPCErrorToHTTP(err)
		WriteErrorResponse(c.Writer, status, code, msg)
		return
	}

	ver := registrygrpc.ProtoToVersion(resp.GetVersion())
	WriteJSON(c.Writer, http.StatusOK, ver)
}

func handleDeleteDraftVersionGRPC(c *gin.Context, client ardav1.AgentRegistryServiceClient) {
	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		WriteErrorResponse(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	agentIDStr := c.Param("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil || agentID == uuid.Nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid agent id: must be a valid uuid")
		return
	}

	versionIDStr := c.Param("versionId")
	versionID, err := uuid.Parse(versionIDStr)
	if err != nil || versionID == uuid.Nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid version id: must be a valid uuid")
		return
	}

	ctx := withInternalAuth(c.Request.Context(), identity)
	req := &ardav1.DeleteDraftVersionRequest{
		TenantId:  identity.TenantID.String(),
		AgentId:   agentID.String(),
		VersionId: versionID.String(),
	}

	_, err = client.DeleteDraftVersion(ctx, req)
	if err != nil {
		status, code, msg := registrygrpc.MapGRPCErrorToHTTP(err)
		WriteErrorResponse(c.Writer, status, code, msg)
		return
	}

	c.Status(http.StatusNoContent)
}
