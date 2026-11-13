package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	reviewgrpc "github.com/aswinsreeraj/arda/services/review/grpc"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func handleListReviews(c *gin.Context, client ardav1.ReviewServiceClient) {
	if client == nil {
		WriteErrorResponse(c.Writer, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "review service client is not available")
		return
	}

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

	statusFilter := c.Query("status")
	if statusFilter == "" {
		statusFilter = "OPEN"
	}

	agentIDStr := c.Query("agent_id")
	if agentIDStr != "" {
		if _, err := uuid.Parse(agentIDStr); err != nil {
			WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid agent_id parameter: must be a valid uuid")
			return
		}
	}

	escalationReason := c.Query("escalation_reason")
	pageToken := c.Query("page_token")

	ctx := withInternalAuth(c.Request.Context(), identity)
	req := &ardav1.ListReviewCasesRequest{
		TenantId:         identity.TenantID.String(),
		Status:           statusFilter,
		AgentId:          agentIDStr,
		EscalationReason: escalationReason,
		Pagination: &ardav1.PaginationRequest{
			PageSize:  pageSize,
			PageToken: pageToken,
		},
	}

	resp, err := client.ListReviewCases(ctx, req)
	if err != nil {
		status, code, msg := reviewgrpc.MapReviewGRPCErrorToHTTP(err)
		WriteErrorResponse(c.Writer, status, code, msg)
		return
	}

	cases := make([]map[string]any, 0, len(resp.GetCases()))
	for _, cs := range resp.GetCases() {
		item := map[string]any{
			"case_id":            cs.GetCaseId(),
			"execution_id":       cs.GetExecutionId(),
			"agent_id":           cs.GetAgentId(),
			"agent_name":         cs.GetAgentName(),
			"proposed_decision":  cs.GetProposedDecision(),
			"confidence":         cs.GetConfidence(),
			"escalation_reasons": cs.GetEscalationReasons(),
			"status":             cs.GetStatus(),
			"created_at":         cs.GetCreatedAt().AsTime().Format("2006-01-02T15:04:05Z07:00"),
		}
		cases = append(cases, item)
	}

	var nextPageToken string
	var totalCount int32
	if p := resp.GetPagination(); p != nil {
		nextPageToken = p.GetNextPageToken()
		totalCount = p.GetTotalCount()
	}

	WriteJSON(c.Writer, http.StatusOK, gin.H{
		"cases":           cases,
		"next_page_token": nextPageToken,
		"total_count":     totalCount,
	})
}

func handleGetReview(c *gin.Context, client ardav1.ReviewServiceClient) {
	if client == nil {
		WriteErrorResponse(c.Writer, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "review service client is not available")
		return
	}

	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		WriteErrorResponse(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	caseID := strings.TrimSpace(c.Param("id"))
	if caseID == "" {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "case_id is required")
		return
	}

	ctx := withInternalAuth(c.Request.Context(), identity)
	req := &ardav1.GetReviewCaseRequest{
		TenantId: identity.TenantID.String(),
		CaseId:   caseID,
	}

	resp, err := client.GetReviewCase(ctx, req)
	if err != nil {
		status, code, msg := reviewgrpc.MapReviewGRPCErrorToHTTP(err)
		WriteErrorResponse(c.Writer, status, code, msg)
		return
	}

	rc := resp.GetCase()
	fe := rc.GetFrozenEvidence()

	var originalInput map[string]any
	if fe != nil && fe.GetOriginalInput() != nil {
		originalInput = fe.GetOriginalInput().AsMap()
	}

	var neighbors []map[string]any
	if fe != nil {
		for _, n := range fe.GetNeighbors() {
			neighbors = append(neighbors, map[string]any{
				"reference_example_id": n.GetReferenceExampleId(),
				"label":                n.GetLabel(),
				"similarity":           n.GetSimilarity(),
			})
		}
	}

	var toolCalls []map[string]any
	if fe != nil {
		for _, tc := range fe.GetToolCalls() {
			tcMap := map[string]any{
				"tool_id":     tc.GetToolId(),
				"tool_name":   tc.GetToolName(),
				"output":      tc.GetOutput(),
				"error":       tc.GetError(),
				"allowed":     tc.GetAllowed(),
				"denial_code": tc.GetDenialCode(),
				"duration_ms": tc.GetDurationMs(),
			}
			if tc.GetArguments() != nil {
				tcMap["arguments"] = tc.GetArguments().AsMap()
			}
			if tc.GetResult() != nil {
				tcMap["result"] = tc.GetResult().AsMap()
			}
			toolCalls = append(toolCalls, tcMap)
		}
	}

	evidenceObj := map[string]any{
		"original_input":        originalInput,
		"agent_reasoning":       fe.GetAgentReasoning(),
		"proposed_decision":     fe.GetProposedDecision(),
		"confidence":            fe.GetConfidence(),
		"reliability_threshold": fe.GetReliabilityThreshold(),
		"escalation_reasons":    fe.GetEscalationReasons(),
		"neighbors":             neighbors,
		"tool_calls":            toolCalls,
		"metadata":              fe.GetMetadata(),
	}

	result := map[string]any{
		"case_id":               rc.GetId(),
		"tenant_id":             rc.GetTenantId(),
		"execution_id":          rc.GetExecutionId(),
		"agent_id":              rc.GetAgentId(),
		"agent_name":            rc.GetAgentName(),
		"agent_version_id":      rc.GetAgentVersionId(),
		"agent_version_number":  rc.GetAgentVersionNumber(),
		"execution_mode":        rc.GetExecutionMode(),
		"status":                rc.GetStatus(),
		"proposed_decision":     rc.GetProposedDecision(),
		"confidence":            rc.GetConfidence(),
		"reliability_threshold": rc.GetReliabilityThreshold(),
		"escalation_reasons":    rc.GetEscalationReasons(),
		"frozen_evidence":       evidenceObj,
		"created_at":            rc.GetCreatedAt().AsTime().Format("2006-01-02T15:04:05Z07:00"),
		"updated_at":            rc.GetUpdatedAt().AsTime().Format("2006-01-02T15:04:05Z07:00"),
	}

	if rc.GetStatus() == "RESOLVED" {
		resObj := map[string]any{
			"decision":         rc.GetDecision(),
			"final_decision":   rc.GetFinalDecision(),
			"resolution_reason": rc.GetResolutionReason(),
			"reviewer_id":      rc.GetReviewerId(),
			"audit_record_id":  rc.GetAuditRecordId(),
		}
		if rc.GetResolvedAt() != nil {
			resObj["resolved_at"] = rc.GetResolvedAt().AsTime().Format("2006-01-02T15:04:05Z07:00")
		}
		result["resolution"] = resObj
	}

	WriteJSON(c.Writer, http.StatusOK, result)
}

func handleResolveReview(c *gin.Context, client ardav1.ReviewServiceClient) {
	if client == nil {
		WriteErrorResponse(c.Writer, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "review service client is not available")
		return
	}

	identity, ok := auth.IdentityFromContext(c.Request.Context())
	if !ok {
		WriteErrorResponse(c.Writer, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}

	caseID := strings.TrimSpace(c.Param("id"))
	if caseID == "" {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "case_id is required")
		return
	}

	var reqBody struct {
		Decision      string `json:"decision"`
		FinalDecision string `json:"final_decision"`
		Reason        string `json:"reason"`
	}

	if err := json.NewDecoder(c.Request.Body).Decode(&reqBody); err != nil {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid json request body")
		return
	}

	decision := strings.ToLower(strings.TrimSpace(reqBody.Decision))
	if decision != "approve" && decision != "reject" && decision != "override" {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "invalid decision: must be 'approve', 'reject', or 'override'")
		return
	}

	reason := strings.TrimSpace(reqBody.Reason)
	if reason == "" {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "resolution reason is mandatory")
		return
	}

	if decision == "override" && strings.TrimSpace(reqBody.FinalDecision) == "" {
		WriteErrorResponse(c.Writer, http.StatusBadRequest, "VALIDATION_FAILED", "final_decision is required when decision is 'override'")
		return
	}

	reviewerID := identity.UserID
	if reviewerID == "" {
		reviewerID = string(identity.Role)
	}

	ctx := withInternalAuth(c.Request.Context(), identity)
	req := &ardav1.ResolveReviewCaseRequest{
		TenantId:      identity.TenantID.String(),
		CaseId:        caseID,
		Decision:      decision,
		FinalDecision: strings.TrimSpace(reqBody.FinalDecision),
		Reason:        reason,
		ReviewerId:    reviewerID,
	}

	resp, err := client.ResolveReviewCase(ctx, req)
	if err != nil {
		status, code, msg := reviewgrpc.MapReviewGRPCErrorToHTTP(err)
		WriteErrorResponse(c.Writer, status, code, msg)
		return
	}

	WriteJSON(c.Writer, http.StatusOK, gin.H{
		"case_id":         resp.GetCaseId(),
		"status":          resp.GetStatus(),
		"audit_record_id": resp.GetAuditRecordId(),
	})
}
