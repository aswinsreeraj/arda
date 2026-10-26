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

// ProviderGroq is the canonical provider identifier for Groq Cloud.
const ProviderGroq = "groq"

// ProviderOpenRouter is the canonical provider identifier for OpenRouter.
const ProviderOpenRouter = "openrouter"

// ProviderGrok is the canonical provider identifier for xAI Grok.
const ProviderGrok = "grok"

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

// DefaultGroqModels lists common Groq-hosted open source models.
var DefaultGroqModels = []string{
	"llama-3.3-70b-versatile",
	"llama-3.1-8b-instant",
	"llama-3.1-70b-versatile",
	"llama-guard-3-8b",
	"llama3-70b-8192",
	"llama3-8b-8192",
	"mixtral-8x7b-32768",
	"gemma2-9b-it",
}

// DefaultOpenRouterModels lists popular multi-provider models on OpenRouter.
var DefaultOpenRouterModels = []string{
	"anthropic/claude-3.5-sonnet",
	"meta-llama/llama-3.3-70b-instruct",
	"openai/gpt-4o",
	"openai/gpt-4o-mini",
	"google/gemini-pro-1.5",
	"deepseek/deepseek-chat",
}

// DefaultGrokModels lists common xAI Grok models.
var DefaultGrokModels = []string{
	"grok-2",
	"grok-2-1212",
	"grok-2-mini",
	"grok-2-vision-1212",
	"grok-beta",
	"grok-vision-beta",
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

// OpenAIAdapter implements ProviderAdapter for OpenAI and OpenAI-compatible Chat Completions APIs (e.g. Groq).
type OpenAIAdapter struct {
	providerID      string
	baseURL         string
	httpClient      HTTPDoer
	timeout         time.Duration
	supportedModels map[string]struct{}
}

// NewOpenAIAdapter creates a new OpenAIAdapter instance with safe defaults.
func NewOpenAIAdapter(opts ...OpenAIOption) *OpenAIAdapter {
	adapter := &OpenAIAdapter{
		providerID: ProviderOpenAI,
		baseURL:    "https://api.openai.com/v1",
		timeout:    30 * time.Second,
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

// NewGroqAdapter creates a new ProviderAdapter preconfigured for Groq Cloud.
// It leverages Groq's high-speed OpenAI-compatible API at https://api.groq.com/openai/v1.
func NewGroqAdapter(opts ...OpenAIOption) *OpenAIAdapter {
	adapter := &OpenAIAdapter{
		providerID: ProviderGroq,
		baseURL:    "https://api.groq.com/openai/v1",
		timeout:    30 * time.Second,
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

	for _, m := range DefaultGroqModels {
		adapter.supportedModels[m] = struct{}{}
	}

	for _, opt := range opts {
		opt(adapter)
	}

	return adapter
}

// NewOpenRouterAdapter creates a new ProviderAdapter preconfigured for OpenRouter.
// OpenRouter provides a unified OpenAI-compatible endpoint routing to hundreds of LLMs.
func NewOpenRouterAdapter(opts ...OpenAIOption) *OpenAIAdapter {
	adapter := &OpenAIAdapter{
		providerID: ProviderOpenRouter,
		baseURL:    "https://openrouter.ai/api/v1",
		timeout:    30 * time.Second,
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

	for _, m := range DefaultOpenRouterModels {
		adapter.supportedModels[m] = struct{}{}
	}

	for _, opt := range opts {
		opt(adapter)
	}

	return adapter
}

// NewGrokAdapter creates a new ProviderAdapter preconfigured for xAI Grok.
// It leverages xAI's high-speed OpenAI-compatible API at https://api.x.ai/v1.
func NewGrokAdapter(opts ...OpenAIOption) *OpenAIAdapter {
	adapter := &OpenAIAdapter{
		providerID: ProviderGrok,
		baseURL:    "https://api.x.ai/v1",
		timeout:    30 * time.Second,
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

	for _, m := range DefaultGrokModels {
		adapter.supportedModels[m] = struct{}{}
	}

	for _, opt := range opts {
		opt(adapter)
	}

	return adapter
}

func (a *OpenAIAdapter) ProviderID() string {
	if a.providerID != "" {
		return a.providerID
	}
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
		return true // defaults to provider default model
	}
	if _, ok := a.supportedModels[trimmed]; ok {
		return true
	}
	lower := strings.ToLower(trimmed)
	switch a.ProviderID() {
	case ProviderOpenRouter:
		// OpenRouter dynamically routes to all provider models (e.g. "anthropic/claude-3.5-sonnet")
		return true
	case ProviderGrok:
		if strings.HasPrefix(lower, "grok") {
			return true
		}
		return false
	case ProviderGroq:
		// Accept common open-source model families available on Groq
		if strings.HasPrefix(lower, "llama") || strings.HasPrefix(lower, "mixtral") || strings.HasPrefix(lower, "gemma") || strings.HasPrefix(lower, "qwen") || strings.HasPrefix(lower, "deepseek") {
			return true
		}
		return false
	default:
		// Also accept versioned or standard OpenAI prefix models
		if strings.HasPrefix(lower, "gpt-") || strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3") {
			return true
		}
		return false
	}
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
		return LLMResponse{}, NewPermanentError(fmt.Sprintf("missing required API key for %s provider", a.ProviderID()), ErrMissingCredential)
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		switch a.ProviderID() {
		case ProviderGroq:
			model = "llama-3.3-70b-versatile"
		case ProviderOpenRouter:
			model = "anthropic/claude-3.5-sonnet"
		case ProviderGrok:
			model = "grok-2"
		default:
			model = "gpt-4o"
		}
	}
	if !a.IsModelSupported(model) {
		return LLMResponse{}, fmt.Errorf("%w: unsupported %s model %q", ErrUnsupportedModel, a.ProviderID(), model)
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
			// Standard chat models (e.g. gpt-4o, gpt-4-turbo, gpt-3.5-turbo, Groq llama/mixtral)
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
		return LLMResponse{}, fmt.Errorf("%w: failed to marshal %s request: %v", ErrPermanent, a.ProviderID(), err)
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
		return LLMResponse{}, fmt.Errorf("%w: failed to create %s http request: %v", ErrPermanent, a.ProviderID(), err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+cleanKey)
	httpReq.Header.Set("Content-Type", "application/json")
	if a.ProviderID() == ProviderOpenRouter {
		httpReq.Header.Set("HTTP-Referer", "https://arda.dev")
		httpReq.Header.Set("X-Title", "ARDA Platform")
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(reqCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return LLMResponse{}, context.Canceled
		}
		if errors.Is(reqCtx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return LLMResponse{}, context.DeadlineExceeded
		}
		safeErr := redactString(err.Error(), cleanKey)
		return LLMResponse{}, NewTransientError(fmt.Sprintf("%s request failed due to connection error: %s", a.ProviderID(), safeErr), ErrTransient)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB limit
	if err != nil {
		return LLMResponse{}, NewTransientError(fmt.Sprintf("failed to read %s response body", a.ProviderID()), ErrTransient)
	}

	contentType := resp.Header.Get("Content-Type")
	var chatResp openAIChatResponse
	unmarshalErr := json.Unmarshal(respBytes, &chatResp)

	switch resp.StatusCode {
	case http.StatusOK:
		if !strings.Contains(strings.ToLower(contentType), "application/json") {
			return LLMResponse{}, NewTransientError(fmt.Sprintf("unexpected content-type from %s: %q", a.ProviderID(), contentType), ErrTransient)
		}
		if unmarshalErr != nil {
			return LLMResponse{}, NewTransientError(fmt.Sprintf("failed to parse %s json response: %v", a.ProviderID(), unmarshalErr), ErrTransient)
		}
		if len(chatResp.Choices) == 0 {
			return LLMResponse{}, NewPermanentError(fmt.Sprintf("%s returned response with no choices", a.ProviderID()), ErrPermanent)
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
		return LLMResponse{}, NewPermanentError(fmt.Sprintf("%s authentication failed: invalid or unauthorized api key", a.ProviderID()), ErrPermanent)

	case http.StatusTooManyRequests:
		return LLMResponse{}, NewTransientError(fmt.Sprintf("%s rate limit exceeded", a.ProviderID()), ErrTransient)

	case http.StatusBadRequest:
		errMsg := "bad request"
		if chatResp.Error != nil && chatResp.Error.Message != "" {
			errMsg = sanitizeErrorMessage(chatResp.Error.Message, cleanKey)
		}
		return LLMResponse{}, NewPermanentError(fmt.Sprintf("%s rejected request (HTTP 400): %s", a.ProviderID(), errMsg), ErrPermanent)

	default:
		if resp.StatusCode >= 500 {
			return LLMResponse{}, NewTransientError(fmt.Sprintf("%s upstream service error (HTTP %d)", a.ProviderID(), resp.StatusCode), ErrTransient)
		}
		return LLMResponse{}, NewPermanentError(fmt.Sprintf("%s request failed with HTTP %d", a.ProviderID(), resp.StatusCode), ErrPermanent)
	}
}

// sanitizeErrorMessage strips any sensitive credential strings from upstream error messages.
func sanitizeErrorMessage(msg string, apiKey string) string {
	cleaned := redactString(msg, apiKey)
	if strings.Contains(cleaned, "sk-") || strings.Contains(cleaned, "gsk_") || strings.Contains(cleaned, "xai-") {
		return "request validation error"
	}
	return cleaned
}

var tokenPattern = regexp.MustCompile(`(sk-ant-[A-Za-z0-9_-]{8,}|sk-or-v1-[A-Za-z0-9_-]{8,}|xai-[A-Za-z0-9_-]{8,}|sk-[A-Za-z0-9_-]{8,}|gsk_[A-Za-z0-9_-]{8,}|AIza[A-Za-z0-9_-]{30,}|Bearer\s+[A-Za-z0-9._-]+)`)

// redactString redacts known secrets and token patterns from messages.
func redactString(str string, secret string) string {
	if secret != "" {
		str = strings.ReplaceAll(str, secret, "[REDACTED]")
	}
	str = tokenPattern.ReplaceAllString(str, "[REDACTED]")
	return str
}
