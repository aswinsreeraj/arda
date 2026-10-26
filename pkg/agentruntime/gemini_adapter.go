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

// ProviderGoogle is the canonical provider identifier for Google (Gemini).
const ProviderGoogle = "google"

// ProviderGemini is an alias identifier for Google Gemini.
const ProviderGemini = "gemini"

// DefaultGeminiModels lists standard Google Gemini models.
var DefaultGeminiModels = []string{
	"gemini-1.5-pro",
	"gemini-1.5-pro-latest",
	"gemini-1.5-flash",
	"gemini-1.5-flash-latest",
	"gemini-1.5-flash-8b",
	"gemini-2.0-flash-exp",
	"gemini-1.0-pro",
}

// GeminiOption configures optional settings on a GeminiAdapter.
type GeminiOption func(*GeminiAdapter)

// WithGeminiBaseURL configures an internal base URL (for testing with httptest.Server only).
func WithGeminiBaseURL(baseURL string) GeminiOption {
	return func(a *GeminiAdapter) {
		trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if trimmed != "" {
			a.baseURL = trimmed
		}
	}
}

// WithGeminiHTTPClient sets a custom HTTPDoer.
func WithGeminiHTTPClient(client HTTPDoer) GeminiOption {
	return func(a *GeminiAdapter) {
		if client != nil {
			a.httpClient = client
		}
	}
}

// WithGeminiTimeout configures the per-request timeout.
func WithGeminiTimeout(timeout time.Duration) GeminiOption {
	return func(a *GeminiAdapter) {
		if timeout > 0 {
			a.timeout = timeout
		}
	}
}

// GeminiAdapter implements ProviderAdapter for Google's Gemini generateContent REST API.
type GeminiAdapter struct {
	baseURL         string
	httpClient      HTTPDoer
	timeout         time.Duration
	supportedModels map[string]struct{}
}

// NewGeminiAdapter creates a new GeminiAdapter instance with safe defaults.
func NewGeminiAdapter(opts ...GeminiOption) *GeminiAdapter {
	adapter := &GeminiAdapter{
		baseURL: "https://generativelanguage.googleapis.com/v1beta",
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

	for _, m := range DefaultGeminiModels {
		adapter.supportedModels[m] = struct{}{}
	}

	for _, opt := range opts {
		opt(adapter)
	}

	return adapter
}

func (a *GeminiAdapter) ProviderID() string {
	return ProviderGoogle
}

func (a *GeminiAdapter) SupportedModels() []string {
	models := make([]string, 0, len(a.supportedModels))
	for m := range a.supportedModels {
		models = append(models, m)
	}
	sort.Strings(models)
	return models
}

func (a *GeminiAdapter) IsModelSupported(model string) bool {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return true // defaults to gemini-1.5-pro
	}
	if _, ok := a.supportedModels[trimmed]; ok {
		return true
	}
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "gemini")
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiGenConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

type geminiGenerateRequest struct {
	Contents         []geminiContent  `json:"contents"`
	GenerationConfig *geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiGenerateResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
			Role string `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

// Generate fulfills LLMClient by reading the API key from context (if present) and delegating to GenerateWithCredential.
func (a *GeminiAdapter) Generate(ctx context.Context, req LLMRequest) (LLMResponse, error) {
	apiKey, _ := APIKeyFromContext(ctx)
	return a.GenerateWithCredential(ctx, apiKey, req)
}

// GenerateWithCredential fulfills ProviderAdapter by invoking Google Gemini's generateContent API.
func (a *GeminiAdapter) GenerateWithCredential(ctx context.Context, apiKey string, req LLMRequest) (LLMResponse, error) {
	if err := ctx.Err(); err != nil {
		return LLMResponse{}, err
	}
	if err := req.Validate(); err != nil {
		return LLMResponse{}, err
	}

	cleanKey := strings.TrimSpace(apiKey)
	if cleanKey == "" {
		return LLMResponse{}, NewPermanentError("missing required API key for google gemini provider", ErrMissingCredential)
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = "gemini-1.5-pro"
	}
	if !a.IsModelSupported(model) {
		return LLMResponse{}, fmt.Errorf("%w: unsupported google gemini model %q", ErrUnsupportedModel, model)
	}

	geminiReq := geminiGenerateRequest{
		Contents: []geminiContent{
			{
				Role: "user",
				Parts: []geminiPart{
					{Text: req.Prompt},
				},
			},
		},
	}

	if req.Parameters != nil {
		genCfg := &geminiGenConfig{}
		hasCfg := false

		if req.Parameters.Temperature != nil {
			if *req.Parameters.Temperature < 0.0 || *req.Parameters.Temperature > 2.0 {
				return LLMResponse{}, NewPermanentError("temperature must be between 0.0 and 2.0 for gemini", ErrPermanent)
			}
			genCfg.Temperature = req.Parameters.Temperature
			hasCfg = true
		}
		if req.Parameters.TopP != nil {
			if *req.Parameters.TopP < 0.0 || *req.Parameters.TopP > 1.0 {
				return LLMResponse{}, NewPermanentError("top_p must be between 0.0 and 1.0 for gemini", ErrPermanent)
			}
			genCfg.TopP = req.Parameters.TopP
			hasCfg = true
		}
		if req.Parameters.MaxTokens != nil {
			if *req.Parameters.MaxTokens <= 0 {
				return LLMResponse{}, NewPermanentError("max_tokens must be greater than 0", ErrPermanent)
			}
			genCfg.MaxOutputTokens = req.Parameters.MaxTokens
			hasCfg = true
		}
		if len(req.Parameters.Stop) > 0 {
			genCfg.StopSequences = req.Parameters.Stop
			hasCfg = true
		}

		if hasCfg {
			geminiReq.GenerationConfig = genCfg
		}
	}

	bodyBytes, err := json.Marshal(geminiReq)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("%w: failed to marshal gemini request: %v", ErrPermanent, err)
	}

	reqCtx := ctx
	if a.timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}

	endpoint := fmt.Sprintf("%s/models/%s:generateContent", a.baseURL, model)
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return LLMResponse{}, fmt.Errorf("%w: failed to create gemini http request: %v", ErrPermanent, err)
	}

	httpReq.Header.Set("x-goog-api-key", cleanKey)
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
		return LLMResponse{}, NewTransientError(fmt.Sprintf("google gemini request failed due to connection error: %s", safeErr), ErrTransient)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB limit
	if err != nil {
		return LLMResponse{}, NewTransientError("failed to read google gemini response body", ErrTransient)
	}

	contentType := resp.Header.Get("Content-Type")
	var gResp geminiGenerateResponse
	unmarshalErr := json.Unmarshal(respBytes, &gResp)

	switch resp.StatusCode {
	case http.StatusOK:
		if !strings.Contains(strings.ToLower(contentType), "application/json") {
			return LLMResponse{}, NewTransientError(fmt.Sprintf("unexpected content-type from google gemini: %q", contentType), ErrTransient)
		}
		if unmarshalErr != nil {
			return LLMResponse{}, NewTransientError(fmt.Sprintf("failed to parse google gemini json response: %v", unmarshalErr), ErrTransient)
		}

		if len(gResp.Candidates) == 0 {
			return LLMResponse{}, NewPermanentError("google gemini returned response with no candidates", ErrPermanent)
		}

		var sb strings.Builder
		for _, part := range gResp.Candidates[0].Content.Parts {
			sb.WriteString(part.Text)
		}

		content := sb.String()
		usage := &TokenUsage{}
		if gResp.UsageMetadata != nil {
			usage.PromptTokens = gResp.UsageMetadata.PromptTokenCount
			usage.CompletionTokens = gResp.UsageMetadata.CandidatesTokenCount
			usage.TotalTokens = gResp.UsageMetadata.TotalTokenCount
		}

		return LLMResponse{
			Content: content,
			Model:   model,
			Usage:   usage,
		}, nil

	case http.StatusUnauthorized, http.StatusForbidden:
		return LLMResponse{}, NewPermanentError("google gemini authentication failed: invalid or unauthorized api key", ErrPermanent)

	case http.StatusTooManyRequests:
		return LLMResponse{}, NewTransientError("google gemini rate limit or quota exceeded", ErrTransient)

	case http.StatusBadRequest:
		errMsg := "bad request"
		if gResp.Error != nil && gResp.Error.Message != "" {
			if strings.Contains(strings.ToLower(gResp.Error.Message), "api key not valid") {
				return LLMResponse{}, NewPermanentError("google gemini authentication failed: invalid api key", ErrPermanent)
			}
			errMsg = sanitizeErrorMessage(gResp.Error.Message, cleanKey)
		}
		return LLMResponse{}, NewPermanentError(fmt.Sprintf("google gemini rejected request (HTTP 400): %s", errMsg), ErrPermanent)

	default:
		if resp.StatusCode >= 500 {
			return LLMResponse{}, NewTransientError(fmt.Sprintf("google gemini upstream service error (HTTP %d)", resp.StatusCode), ErrTransient)
		}
		return LLMResponse{}, NewPermanentError(fmt.Sprintf("google gemini request failed with HTTP %d", resp.StatusCode), ErrPermanent)
	}
}
