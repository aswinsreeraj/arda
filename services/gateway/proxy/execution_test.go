package proxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/services/execution"
	"github.com/aswinsreeraj/arda/services/gateway/proxy"
	"github.com/google/uuid"
)

func TestHTTPExecutionClient_Success(t *testing.T) {
	agentID := uuid.New()
	versionID := uuid.New()
	tenantID := uuid.New()

	var receivedAuthHeader string
	var receivedTenantHeader string
	var receivedAgentIDInPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		receivedTenantHeader = r.Header.Get("X-Tenant-ID")
		receivedAgentIDInPath = r.URL.Path

		resp := execution.SubmitExecutionResponse{
			WorkflowID:     "agent-exec-" + agentID.String() + "-123",
			RunID:          "run-uuid-456",
			AgentID:        agentID,
			AgentVersionID: versionID,
			Status:         "ACCEPTED",
			Mode:           "async",
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := proxy.NewExecutionClient(server.URL)
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}
	defer client.Close()

	req := execution.SubmitExecutionRequest{
		AgentID:        agentID,
		AgentVersionID: versionID,
		Input:          map[string]any{"prompt": "hello"},
		Mode:           "async",
		ExecutionID:    "123",
	}

	ctx := context.Background()
	authHeader := "Bearer test-jwt-token"
	resp, err := client.SubmitExecution(ctx, authHeader, req)
	if err != nil {
		t.Fatalf("unexpected error submitting execution: %v", err)
	}

	if resp.Status != "ACCEPTED" {
		t.Errorf("expected status ACCEPTED, got %s", resp.Status)
	}
	if resp.WorkflowID != "agent-exec-"+agentID.String()+"-123" {
		t.Errorf("expected workflow ID with execution ID, got %s", resp.WorkflowID)
	}
	if receivedAuthHeader != authHeader {
		t.Errorf("expected auth header %q, got %q", authHeader, receivedAuthHeader)
	}
	if receivedTenantHeader != "" {
		t.Errorf("expected X-Tenant-ID to be stripped/empty, got %q", receivedTenantHeader)
	}
	expectedPath := "/v1/agents/" + agentID.String() + "/execute"
	if receivedAgentIDInPath != expectedPath {
		t.Errorf("expected path %q, got %q", expectedPath, receivedAgentIDInPath)
	}

	_ = tenantID
}

func TestHTTPExecutionClient_ErrorMapping(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
		errorCode  string
		errMsg     string
	}{
		{"validation failure", http.StatusBadRequest, "VALIDATION_FAILED", "invalid payload"},
		{"unauthenticated", http.StatusUnauthorized, "UNAUTHENTICATED", "token expired"},
		{"forbidden", http.StatusForbidden, "FORBIDDEN", "role not authorized"},
		{"not found", http.StatusNotFound, "NOT_FOUND", "version not found"},
		{"conflict", http.StatusConflict, "CONFLICT", "workflow already in progress"},
		{"upstream error", http.StatusBadGateway, "UPSTREAM_ERROR", "temporal unavailable"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"code":    tc.errorCode,
						"message": tc.errMsg,
					},
				})
			}))
			defer server.Close()

			client, err := proxy.NewExecutionClient(server.URL)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			defer client.Close()

			req := execution.SubmitExecutionRequest{
				AgentID:        uuid.New(),
				AgentVersionID: uuid.New(),
			}

			_, err = client.SubmitExecution(context.Background(), "Bearer tok", req)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}

			var execErr *proxy.ExecutionError
			if !errors.As(err, &execErr) {
				t.Fatalf("expected *proxy.ExecutionError, got %T: %v", err, err)
			}
			if execErr.StatusCode != tc.statusCode {
				t.Errorf("expected status code %d, got %d", tc.statusCode, execErr.StatusCode)
			}
			if execErr.Code != tc.errorCode {
				t.Errorf("expected error code %s, got %s", tc.errorCode, execErr.Code)
			}
			if execErr.Message != tc.errMsg {
				t.Errorf("expected message %s, got %s", tc.errMsg, execErr.Message)
			}
		})
	}
}

func TestHTTPExecutionClient_ContextCancellationAndTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := proxy.NewExecutionClient(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	req := execution.SubmitExecutionRequest{
		AgentID:        uuid.New(),
		AgentVersionID: uuid.New(),
	}

	_, err = client.SubmitExecution(ctx, "Bearer tok", req)
	if err == nil {
		t.Fatalf("expected deadline exceeded or canceled error, got nil")
	}
}

func TestHTTPExecutionClient_Unconfigured(t *testing.T) {
	_, err := proxy.NewExecutionClient("")
	if err != proxy.ErrExecutionUnconfigured {
		t.Errorf("expected ErrExecutionUnconfigured, got %v", err)
	}

	var nilClient *proxy.HTTPExecutionClient
	_, err = nilClient.SubmitExecution(context.Background(), "", execution.SubmitExecutionRequest{})
	if err != proxy.ErrExecutionUnconfigured {
		t.Errorf("expected ErrExecutionUnconfigured for nil client, got %v", err)
	}
}
