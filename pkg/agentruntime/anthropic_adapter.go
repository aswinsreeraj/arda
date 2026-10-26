package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ProviderAnthropic is the canonical provider identifier for Anthropic (Claude).
const ProviderAnthropic = "anthropic"

// DefaultAnthropicModels lists standard Anthropic Claude models.
var DefaultAnthropicModels = []string{
	"claude-3-5-sonnet-20241022",
	"claude-3-5-sonnet-latest",
	"claude-3-5-haiku-20241022",
	"claude-3-5-haiku-latest",
	"claude-3-opus-20240229",
	"claude-3-opus-latest",
	"claude-3-sonnet-20240229",
	"claude-3-haiku-20240307",
}

// AnthropicOption configures optional settings on an AnthropicAdapter.
type AnthropicOption func(*AnthropicAdapter)

// WithAnthropicBaseURL configures an internal base URL (for testing with httptest.Server only).
func WithAnthropicBaseURL(baseURL string) AnthropicOption {
	return func(a *AnthropicAdapter) {
		trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if trimmed != "" {
			a.baseURL = trimmed
		}
	}
}

// WithAnthropicHTTPClient sets a custom HTTPDoer.
func WithAnthropicHTTPClient(client HTTPDoer) AnthropicOption {
	return func(a *AnthropicAdapter) {
		if client != nil {
			a.httpClient = client
		}
	}
}

// WithAnthropicTimeout configures the per-request timeout.
func WithAnthropicTimeout(timeout time.Duration) AnthropicOption {
	return func(a *AnthropicAdapter) {
		if timeout > 0 {
			a.timeout = timeout
		}
	}
}

// AnthropicAdapter implements ProviderAdapter for Anthropic's Messages API.
type AnthropicAdapter struct {
	baseURL         string
	httpClient      HTTPDoer
	timeout         time.Duration
	supportedModels map[string]struct{}
}

// NewAnthropicAdapter creates a new AnthropicAdapter instance with safe defaults.
func NewAnthropicAdapter(opts ...AnthropicOption) *AnthropicAdapter {
	adapter := &AnthropicAdapter{
		baseURL: "https://api.anthropic.com/v1",
		timeout: 30 * time.Second,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        50,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 5 * time.Second,
			},
		},
		supportedModels: make(map[string]struct{}),
	}

	for _, m := range DefaultAnthropicModels {
		adapter.supportedModels[m] = struct{}{}
	}

	for _, opt := range opts {
		opt(adapter)
	}

	return adapter
}

func (a *AnthropicAdapter) ProviderID() string {
	return ProviderAnthropic
}

func (a *AnthropicAdapter) SupportedModels() []string {
	models := make([]string, 0, len(a.supportedModels))
	for m := range a.supportedModels {
		models = append(models, m)
	}
	sort.Strings(models)
	return models
}

func (a *AnthropicAdapter) IsModelSupported(model string) bool {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return true // defaults to claude-3-5-sonnet-20241022
	}
	if _, ok := a.supportedModels[trimmed]; ok {
		return true
	}
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "claude-")
}

type anthropicChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicMessagesRequest struct {
	Model         string                 `json:"model"`
	Messages      []anthropicChatMessage `json:"messages"`
	MaxTokens     int                    `json:"max_tokens"`
	Temperature   *float64               `json:"temperature,omitempty"`
	TopP          *float64               `json:"top_p,omitempty"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
}

type anthropicMessagesResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Generate fulfills LLMClient by reading the API key from context (if present) and delegating to GenerateWithCredential.
func (a *AnthropicAdapter) Generate(ctx context.Context, req LLMRequest) (LLMResponse, error) {
	apiKey, _ := APIKeyFromContext(ctx)
	return a.GenerateWithCredential(ctx, apiKey, req)
}

// GenerateWithCredential fulfills ProviderAdapter by invoking the Anthropic Messages API.
func (a *AnthropicAdapter) GenerateWithCredential(ctx context.Context, apiKey string, req LLMRequest) (LLMResponse, error) {
	if err := ctx.Err(); err != nil {
		return LLMResponse{}, err
	}
	if err := req.Validate(); err != nil {
		return LLMResponse{}, err
	}

	cleanKey := strings.TrimSpace(apiKey)
	if cleanKey == "" {
		return LLMResponse{}, NewPermanentError("missing required API key for anthropic provider", ErrMissingCredential)
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = "claude-3-5-sonnet-20241022"
	}
	if !a.IsModelSupported(model) {
		return LLMResponse{}, fmt.Errorf("%w: unsupported anthropic model %q", ErrUnsupportedModel, model)
	}

	// Anthropic strictly mandates max_tokens in the request
	maxTokens := 4096
	if req.Parameters != nil && req.Parameters.MaxTokens != nil {
		if *req.Parameters.MaxTokens <= 0 {
			return LLMResponse{}, NewPermanentError("max_tokens must be greater than 0", ErrPermanent)
		}
		maxTokens = *req.Parameters.MaxTokens
	}

	chatReq := anthropicMessagesRequest{
		Model: model,
		Messages: []anthropicChatMessage{
			{Role: "user", Content: req.Prompt},
		},
		MaxTokens: maxTokens,
	}

	if req.Parameters != nil {
		if req.Parameters.Temperature != nil {
			if *req.Parameters.Temperature < 0.0 || *req.Parameters.Temperature > 1.0 {
				return LLMResponse{}, NewPermanentError("temperature must be between 0.0 and 1.0 for anthropic", ErrPermanent)
			}
			chatReq.Temperature = req.Parameters.Temperature
		}
		if req.Parameters.TopP != nil {
			if *req.Parameters.TopP <= 0.0 || *req.Parameters.TopP > 1.0 {
				return LLMResponse{}, NewPermanentError("top_p must be between 0.0 and 1.0 for anthropic", ErrPermanent)
			}
			chatReq.TopP = req.Parameters.TopP
		}
		if len(req.Parameters.Stop) > 0 {
			chatReq.StopSequences = req.Parameters.Stop
		}
	}

	bodyBytes, err := json.Marshal(chatReq)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("%w: failed to marshal anthropic request: %v", ErrPermanent, err)
	}

	reqCtx := ctx
	if a.timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}

	endpoint := a.baseURL + "/messages"
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return LLMResponse{}, fmt.Errorf("%w: failed to create anthropic http request: %v", ErrPermanent, err)
	}

	httpReq.Header.Set("x-api-key", cleanKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(reqCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return LLMResponse{}, context.Canceled
		}
		if errors.Is(reqCtx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return LLMResponse{}, context.DeadlineExceeded
		}
		safeErr := redactString(err.Error(), cleanKey)
		return LLMResponse{}, NewTransientError(fmt.Sprintf("anthropic request failed due to connection error: %s", safeErr), ErrTransient)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB limit
	if err != nil {
		return LLMResponse{}, NewTransientError("failed to read anthropic response body", ErrTransient)
	}

	contentType := resp.Header.Get("Content-Type")
	var msgResp anthropicMessagesResponse
	unmarshalErr := json.Unmarshal(respBytes, &msgResp)

	switch resp.StatusCode {
	case http.StatusOK:
		if !strings.Contains(strings.ToLower(contentType), "application/json") {
			return LLMResponse{}, NewTransientError(fmt.Sprintf("unexpected content-type from anthropic: %q", contentType), ErrTransient)
		}
		if unmarshalErr != nil {
			return LLMResponse{}, NewTransientError(fmt.Sprintf("failed to parse anthropic json response: %v", unmarshalErr), ErrTransient)
		}

		var sb strings.Builder
		for _, part := range msgResp.Content {
			if part.Type == "text" {
				sb.WriteString(part.Text)
			}
		}

		content := sb.String()
		if content == "" && len(msgResp.Content) == 0 {
			return LLMResponse{}, NewPermanentError("anthropic returned response with empty content", ErrPermanent)
		}

		llmResp := LLMResponse{
			Content: content,
			Model:   msgResp.Model,
			Usage: &TokenUsage{
				PromptTokens:     msgResp.Usage.InputTokens,
				CompletionTokens: msgResp.Usage.OutputTokens,
				TotalTokens:      msgResp.Usage.InputTokens + msgResp.Usage.OutputTokens,
			},
		}
		return llmResp, nil

	case http.StatusUnauthorized, http.StatusForbidden:
		return LLMResponse{}, NewPermanentError("anthropic authentication failed: invalid or unauthorized api key", ErrPermanent)

	case http.StatusTooManyRequests:
		return LLMResponse{}, NewTransientError("anthropic rate limit exceeded", ErrTransient)

	case http.StatusBadRequest:
		errMsg := "bad request"
		if msgResp.Error != nil && msgResp.Error.Message != "" {
			errMsg = sanitizeErrorMessage(msgResp.Error.Message, cleanKey)
		}
		return LLMResponse{}, NewPermanentError(fmt.Sprintf("anthropic rejected request (HTTP 400): %s", errMsg), ErrPermanent)

	default:
		if resp.StatusCode >= 500 {
			return LLMResponse{}, NewTransientError(fmt.Sprintf("anthropic upstream service error (HTTP %d)", resp.StatusCode), ErrTransient)
		}
		return LLMResponse{}, NewPermanentError(fmt.Sprintf("anthropic request failed with HTTP %d", resp.StatusCode), ErrPermanent)
	}
}
