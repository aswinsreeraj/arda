package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aswinsreeraj/arda/services/execution"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
)

// ErrExecutionUnconfigured is returned when the execution service address is empty or not configured.
var ErrExecutionUnconfigured = errors.New("execution service upstream address is not configured")

// ExecutionError represents an error returned by the execution service upstream.
type ExecutionError struct {
	StatusCode int    `json:"status_code"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Err        error  `json:"-"`
}

func (e *ExecutionError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%s] %s: %v (HTTP %d)", e.Code, e.Message, e.Err, e.StatusCode)
	}
	return fmt.Sprintf("[%s] %s (HTTP %d)", e.Code, e.Message, e.StatusCode)
}

func (e *ExecutionError) Unwrap() error {
	return e.Err
}

// ExecutionClient defines the client interface for submitting agent executions to the Execution Service.
type ExecutionClient interface {
	SubmitExecution(ctx context.Context, authHeader string, req execution.SubmitExecutionRequest) (*execution.SubmitExecutionResponse, error)
	Close() error
}

// HTTPExecutionClient connects to the Execution Service over HTTP.
type HTTPExecutionClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewExecutionClient creates an HTTPExecutionClient targeting the given address.
func NewExecutionClient(addr string) (*HTTPExecutionClient, error) {
	if strings.TrimSpace(addr) == "" {
		return nil, ErrExecutionUnconfigured
	}

	url := strings.TrimSpace(addr)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "http://" + url
	}
	url = strings.TrimRight(url, "/")

	return &HTTPExecutionClient{
		baseURL: url,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// SubmitExecution submits an execution request to the upstream Execution Service over HTTP.
func (c *HTTPExecutionClient) SubmitExecution(ctx context.Context, authHeader string, req execution.SubmitExecutionRequest) (*execution.SubmitExecutionResponse, error) {
	if c == nil || c.baseURL == "" {
		return nil, ErrExecutionUnconfigured
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, &ExecutionError{
			StatusCode: http.StatusBadRequest,
			Code:       string(execution.CodeValidationFailed),
			Message:    fmt.Sprintf("failed to encode execution request: %v", err),
		}
	}

	url := fmt.Sprintf("%s/v1/agents/%s/execute", c.baseURL, req.AgentID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, &ExecutionError{
			StatusCode: http.StatusInternalServerError,
			Code:       string(execution.CodeInternalError),
			Message:    fmt.Sprintf("failed to construct execution request: %v", err),
		}
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(authHeader) != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	// Strictly strip and do not forward any client-supplied tenant headers
	httpReq.Header.Del("X-Tenant-ID")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, &ExecutionError{
			StatusCode: http.StatusBadGateway,
			Code:       string(execution.CodeUpstreamError),
			Message:    "failed to communicate with execution service",
			Err:        err,
		}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ExecutionError{
			StatusCode: http.StatusBadGateway,
			Code:       string(execution.CodeUpstreamError),
			Message:    "failed to read execution service response body",
			Err:        err,
		}
	}

	if resp.StatusCode == http.StatusAccepted {
		var execResp execution.SubmitExecutionResponse
		if err := json.Unmarshal(respBody, &execResp); err != nil {
			return nil, &ExecutionError{
				StatusCode: http.StatusBadGateway,
				Code:       string(execution.CodeUpstreamError),
				Message:    "invalid json response from execution service",
				Err:        err,
			}
		}
		return &execResp, nil
	}

	// Parse standardized execution error response
	var errEnvelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &errEnvelope); err == nil && errEnvelope.Error.Code != "" {
		return nil, &ExecutionError{
			StatusCode: resp.StatusCode,
			Code:       errEnvelope.Error.Code,
			Message:    errEnvelope.Error.Message,
		}
	}

	return nil, &ExecutionError{
		StatusCode: resp.StatusCode,
		Code:       string(execution.CodeUpstreamError),
		Message:    fmt.Sprintf("execution service error (HTTP %d): %s", resp.StatusCode, string(respBody)),
	}
}

// Close closes any background idle connections.
func (c *HTTPExecutionClient) Close() error {
	if c != nil && c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	return nil
}

// InProcessExecutionClient adapts an in-process *execution.Service to the ExecutionClient interface.
type InProcessExecutionClient struct {
	svc *execution.Service
}

// NewInProcessExecutionClient constructs an InProcessExecutionClient.
func NewInProcessExecutionClient(svc *execution.Service) *InProcessExecutionClient {
	return &InProcessExecutionClient{svc: svc}
}

// SubmitExecution executes directly against the in-process execution.Service.
func (c *InProcessExecutionClient) SubmitExecution(ctx context.Context, authHeader string, req execution.SubmitExecutionRequest) (*execution.SubmitExecutionResponse, error) {
	if c == nil || c.svc == nil {
		return nil, ErrExecutionUnconfigured
	}

	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return nil, &ExecutionError{
			StatusCode: http.StatusUnauthorized,
			Code:       string(execution.CodeUnauthenticated),
			Message:    "authentication required: missing identity in context",
		}
	}

	resp, err := c.svc.SubmitExecution(ctx, identity, req)
	if err != nil {
		var execErr *execution.ExecutionError
		if errors.As(err, &execErr) {
			return nil, &ExecutionError{
				StatusCode: execErr.HTTPStatus,
				Code:       string(execErr.Code),
				Message:    execErr.Message,
				Err:        execErr.Err,
			}
		}
		return nil, &ExecutionError{
			StatusCode: http.StatusInternalServerError,
			Code:       string(execution.CodeInternalError),
			Message:    err.Error(),
			Err:        err,
		}
	}

	return resp, nil
}

// Close is a no-op for in-process execution clients.
func (c *InProcessExecutionClient) Close() error {
	return nil
}
