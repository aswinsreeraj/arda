package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ProviderOpenAI is the canonical provider identifier for OpenAI.
const ProviderOpenAI = "openai"

// DefaultOpenAIModels lists standard OpenAI models supported by the adapter.
var DefaultOpenAIModels = []string{
	"gpt-4o",
	"gpt-4o-mini",
	"gpt-4-turbo",
	"gpt-4",
	"gpt-3.5-turbo",
	"o1",
	"o1-mini",
	"o1-preview",
	"o3-mini",
}

// HTTPDoer defines the minimal interface for dispatching HTTP requests.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// OpenAIOption configures optional settings on an OpenAIAdapter.
type OpenAIOption func(*OpenAIAdapter)

// WithOpenAIBaseURL configures an internal base URL (for testing with httptest.Server only).
// Tenant configuration cannot supply an arbitrary URL.
func WithOpenAIBaseURL(baseURL string) OpenAIOption {
	return func(a *OpenAIAdapter) {
		trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if trimmed != "" {
			a.baseURL = trimmed
		}
	}
}

// WithOpenAIHTTPClient sets a custom HTTPDoer.
func WithOpenAIHTTPClient(client HTTPDoer) OpenAIOption {
	return func(a *OpenAIAdapter) {
		if client != nil {
			a.httpClient = client
		}
	}
}

// WithOpenAITimeout configures the per-request timeout.
func WithOpenAITimeout(timeout time.Duration) OpenAIOption {
	return func(a *OpenAIAdapter) {
		if timeout > 0 {
			a.timeout = timeout
		}
	}
}

// OpenAIAdapter implements ProviderAdapter for OpenAI's Chat Completions API.
type OpenAIAdapter struct {
	baseURL         string
	httpClient      HTTPDoer
	timeout         time.Duration
	supportedModels map[string]struct{}
}

// NewOpenAIAdapter creates a new OpenAIAdapter instance with safe defaults.
func NewOpenAIAdapter(opts ...OpenAIOption) *OpenAIAdapter {
	adapter := &OpenAIAdapter{
		baseURL: "https://api.openai.com/v1",
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

	for _, m := range DefaultOpenAIModels {
		adapter.supportedModels[m] = struct{}{}
	}

	for _, opt := range opts {
		opt(adapter)
	}

	return adapter
}

func (a *OpenAIAdapter) ProviderID() string {
	return ProviderOpenAI
}

func (a *OpenAIAdapter) SupportedModels() []string {
	models := make([]string, 0, len(a.supportedModels))
	for m := range a.supportedModels {
		models = append(models, m)
	}
	sort.Strings(models)
	return models
}

func (a *OpenAIAdapter) IsModelSupported(model string) bool {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return true // defaults to gpt-4o
	}
	if _, ok := a.supportedModels[trimmed]; ok {
		return true
	}
	// Also accept versioned or standard OpenAI prefix models
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "gpt-") || strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3") {
		return true
	}
	return false
}

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model               string              `json:"model"`
	Messages            []openAIChatMessage `json:"messages"`
	Temperature         *float64            `json:"temperature,omitempty"`
	MaxTokens           *int                `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                `json:"max_completion_tokens,omitempty"`
	TopP                *float64            `json:"top_p,omitempty"`
	Stop                []string            `json:"stop,omitempty"`
}

type openAIChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error,omitempty"`
}

func isReasoningModel(model string) bool {
	lower := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3")
}

// Generate fulfills LLMClient by reading the API key from context (if present) and delegating to GenerateWithCredential.
func (a *OpenAIAdapter) Generate(ctx context.Context, req LLMRequest) (LLMResponse, error) {
	apiKey, _ := APIKeyFromContext(ctx)
	return a.GenerateWithCredential(ctx, apiKey, req)
}

// GenerateWithCredential fulfills ProviderAdapter by invoking the OpenAI Chat Completions API with the explicitly
// passed API key, strictly avoiding context credential pollution and ensuring credential isolation.
func (a *OpenAIAdapter) GenerateWithCredential(ctx context.Context, apiKey string, req LLMRequest) (LLMResponse, error) {
	if err := ctx.Err(); err != nil {
		return LLMResponse{}, err
	}
	if err := req.Validate(); err != nil {
		return LLMResponse{}, err
	}

	cleanKey := strings.TrimSpace(apiKey)
	if cleanKey == "" {
		return LLMResponse{}, NewPermanentError("missing required API key for OpenAI provider", ErrMissingCredential)
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = "gpt-4o"
	}
	if !a.IsModelSupported(model) {
		return LLMResponse{}, fmt.Errorf("%w: unsupported openai model %q", ErrUnsupportedModel, model)
	}

	chatReq := openAIChatRequest{
		Model: model,
		Messages: []openAIChatMessage{
			{Role: "user", Content: req.Prompt},
		},
	}

	isReasoning := isReasoningModel(model)

	if req.Parameters != nil {
		// Stop sequences validation across all models
		if len(req.Parameters.Stop) > 4 {
			return LLMResponse{}, NewPermanentError("stop sequence list cannot exceed 4 items", ErrPermanent)
		}

		if isReasoning {
			// Reasoning models (o1, o3 series) parameter restrictions
			if req.Parameters.Temperature != nil && *req.Parameters.Temperature != 1.0 {
				return LLMResponse{}, NewPermanentError(fmt.Sprintf("openai reasoning model %q does not support custom temperature (only default 1.0 is supported)", model), ErrPermanent)
			}
			if req.Parameters.TopP != nil {
				return LLMResponse{}, NewPermanentError(fmt.Sprintf("openai reasoning model %q does not support top_p parameter", model), ErrPermanent)
			}
			if req.Parameters.MaxTokens != nil {
				if *req.Parameters.MaxTokens <= 0 {
					return LLMResponse{}, NewPermanentError("max_tokens must be greater than 0", ErrPermanent)
				}
				chatReq.MaxCompletionTokens = req.Parameters.MaxTokens
			}
			if len(req.Parameters.Stop) > 0 {
				chatReq.Stop = req.Parameters.Stop
			}
		} else {
			// Standard chat models (e.g. gpt-4o, gpt-4-turbo, gpt-3.5-turbo)
			if req.Parameters.Temperature != nil {
				if *req.Parameters.Temperature < 0.0 || *req.Parameters.Temperature > 2.0 {
					return LLMResponse{}, NewPermanentError("temperature must be between 0.0 and 2.0", ErrPermanent)
				}
				chatReq.Temperature = req.Parameters.Temperature
			}
			if req.Parameters.TopP != nil {
				if *req.Parameters.TopP < 0.0 || *req.Parameters.TopP > 1.0 {
					return LLMResponse{}, NewPermanentError("top_p must be between 0.0 and 1.0", ErrPermanent)
				}
				chatReq.TopP = req.Parameters.TopP
			}
			if req.Parameters.MaxTokens != nil {
				if *req.Parameters.MaxTokens <= 0 {
					return LLMResponse{}, NewPermanentError("max_tokens must be greater than 0", ErrPermanent)
				}
				chatReq.MaxTokens = req.Parameters.MaxTokens
			}
			if len(req.Parameters.Stop) > 0 {
				chatReq.Stop = req.Parameters.Stop
			}
		}
	}

	bodyBytes, err := json.Marshal(chatReq)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("%w: failed to marshal openai request: %v", ErrPermanent, err)
	}

	reqCtx := ctx
	if a.timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}

	endpoint := a.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return LLMResponse{}, fmt.Errorf("%w: failed to create openai http request: %v", ErrPermanent, err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+cleanKey)
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
		return LLMResponse{}, NewTransientError(fmt.Sprintf("openai request failed due to connection error: %s", safeErr), ErrTransient)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB limit
	if err != nil {
		return LLMResponse{}, NewTransientError("failed to read openai response body", ErrTransient)
	}

	contentType := resp.Header.Get("Content-Type")
	var chatResp openAIChatResponse
	unmarshalErr := json.Unmarshal(respBytes, &chatResp)

	switch resp.StatusCode {
	case http.StatusOK:
		if !strings.Contains(strings.ToLower(contentType), "application/json") {
			return LLMResponse{}, NewTransientError(fmt.Sprintf("unexpected content-type from openai: %q", contentType), ErrTransient)
		}
		if unmarshalErr != nil {
			return LLMResponse{}, NewTransientError(fmt.Sprintf("failed to parse openai json response: %v", unmarshalErr), ErrTransient)
		}
		if len(chatResp.Choices) == 0 {
			return LLMResponse{}, NewPermanentError("openai returned response with no choices", ErrPermanent)
		}

		llmResp := LLMResponse{
			Content: chatResp.Choices[0].Message.Content,
			Model:   chatResp.Model,
			Usage: &TokenUsage{
				PromptTokens:     chatResp.Usage.PromptTokens,
				CompletionTokens: chatResp.Usage.CompletionTokens,
				TotalTokens:      chatResp.Usage.TotalTokens,
			},
		}
		return llmResp, nil

	case http.StatusUnauthorized, http.StatusForbidden:
		return LLMResponse{}, NewPermanentError("openai authentication failed: invalid or unauthorized api key", ErrPermanent)

	case http.StatusTooManyRequests:
		return LLMResponse{}, NewTransientError("openai rate limit exceeded", ErrTransient)

	case http.StatusBadRequest:
		errMsg := "bad request"
		if chatResp.Error != nil && chatResp.Error.Message != "" {
			errMsg = sanitizeErrorMessage(chatResp.Error.Message, cleanKey)
		}
		return LLMResponse{}, NewPermanentError(fmt.Sprintf("openai rejected request (HTTP 400): %s", errMsg), ErrPermanent)

	default:
		if resp.StatusCode >= 500 {
			return LLMResponse{}, NewTransientError(fmt.Sprintf("openai upstream service error (HTTP %d)", resp.StatusCode), ErrTransient)
		}
		return LLMResponse{}, NewPermanentError(fmt.Sprintf("openai request failed with HTTP %d", resp.StatusCode), ErrPermanent)
	}
}

// sanitizeErrorMessage strips any sensitive credential strings from upstream error messages.
func sanitizeErrorMessage(msg string, apiKey string) string {
	cleaned := redactString(msg, apiKey)
	if strings.Contains(cleaned, "sk-") {
		return "openai request validation error"
	}
	return cleaned
}

var tokenPattern = regexp.MustCompile(`(sk-[A-Za-z0-9_-]{8,}|Bearer\s+[A-Za-z0-9._-]+)`)

// redactString redacts known secrets and token patterns from messages.
func redactString(str string, secret string) string {
	if secret != "" {
		str = strings.ReplaceAll(str, secret, "[REDACTED]")
	}
	str = tokenPattern.ReplaceAllString(str, "[REDACTED]")
	return str
}
