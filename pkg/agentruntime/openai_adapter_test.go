package agentruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
)

func TestOpenAIAdapter_Success(t *testing.T) {
	var capturedAuth string
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		capturedAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-test-123",
			"object": "chat.completion",
			"model": "gpt-4o",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "Hello! I am OpenAI assistant."
				},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 12,
				"completion_tokens": 8,
				"total_tokens": 20
			}
		}`))
	}))
	defer server.Close()

	adapter := agentruntime.NewOpenAIAdapter(
		agentruntime.WithOpenAIBaseURL(server.URL),
		agentruntime.WithOpenAIHTTPClient(server.Client()),
	)

	temp := 0.2
	maxTok := 256
	req := agentruntime.LLMRequest{
		Prompt: "Test prompt for OpenAI",
		Model:  "gpt-4o",
		Parameters: &agentruntime.GenerationParams{
			Temperature: &temp,
			MaxTokens:   &maxTok,
		},
	}

	ctx := agentruntime.WithAPIKey(context.Background(), "sk-valid-openai-test-key")
	resp, err := adapter.Generate(ctx, req)
	if err != nil {
		t.Fatalf("unexpected generate error: %v", err)
	}

	if resp.Content != "Hello! I am OpenAI assistant." {
		t.Fatalf("unexpected response content: %q", resp.Content)
	}
	if resp.Model != "gpt-4o" {
		t.Fatalf("expected model gpt-4o, got %q", resp.Model)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 20 {
		t.Fatalf("unexpected token usage: %+v", resp.Usage)
	}

	// Verify headers and body sent
	if capturedAuth != "Bearer sk-valid-openai-test-key" {
		t.Fatalf("unexpected auth header: %q", capturedAuth)
	}
	if capturedBody["model"] != "gpt-4o" {
		t.Fatalf("unexpected model in request body: %v", capturedBody["model"])
	}
}

func TestOpenAIAdapter_MissingAPIKey(t *testing.T) {
	adapter := agentruntime.NewOpenAIAdapter()

	// Context without API key
	req := agentruntime.LLMRequest{Prompt: "hello"}
	_, err := adapter.Generate(context.Background(), req)
	if err == nil || !errors.Is(err, agentruntime.ErrMissingCredential) {
		t.Fatalf("expected ErrMissingCredential, got %v", err)
	}
	if agentruntime.ClassifyError(err) != agentruntime.CategoryPermanent {
		t.Fatalf("expected permanent category, got %s", agentruntime.ClassifyError(err))
	}
}

func TestOpenAIAdapter_ErrorMapping(t *testing.T) {
	testCases := []struct {
		name             string
		statusCode       int
		responseBody     string
		expectedCategory agentruntime.ErrorCategory
	}{
		{
			name:             "401 Unauthorized",
			statusCode:       http.StatusUnauthorized,
			responseBody:     `{"error": {"message": "Incorrect API key provided"}}`,
			expectedCategory: agentruntime.CategoryPermanent,
		},
		{
			name:             "403 Forbidden",
			statusCode:       http.StatusForbidden,
			responseBody:     `{"error": {"message": "Country not supported"}}`,
			expectedCategory: agentruntime.CategoryPermanent,
		},
		{
			name:             "429 Rate Limited",
			statusCode:       http.StatusTooManyRequests,
			responseBody:     `{"error": {"message": "Rate limit reached"}}`,
			expectedCategory: agentruntime.CategoryTransient,
		},
		{
			name:             "500 Internal Error",
			statusCode:       http.StatusInternalServerError,
			responseBody:     `{"error": {"message": "Internal error"}}`,
			expectedCategory: agentruntime.CategoryTransient,
		},
		{
			name:             "503 Service Unavailable",
			statusCode:       http.StatusServiceUnavailable,
			responseBody:     `Service Unavailable`,
			expectedCategory: agentruntime.CategoryTransient,
		},
		{
			name:             "400 Bad Request",
			statusCode:       http.StatusBadRequest,
			responseBody:     `{"error": {"message": "Invalid temperature value"}}`,
			expectedCategory: agentruntime.CategoryPermanent,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.responseBody))
			}))
			defer server.Close()

			adapter := agentruntime.NewOpenAIAdapter(
				agentruntime.WithOpenAIBaseURL(server.URL),
				agentruntime.WithOpenAIHTTPClient(server.Client()),
			)

			ctx := agentruntime.WithAPIKey(context.Background(), "sk-key")
			_, err := adapter.Generate(ctx, agentruntime.LLMRequest{Prompt: "test"})
			if err == nil {
				t.Fatalf("expected error for status %d, got nil", tc.statusCode)
			}

			cat := agentruntime.ClassifyError(err)
			if cat != tc.expectedCategory {
				t.Fatalf("status %d: expected category %s, got %s (err: %v)", tc.statusCode, tc.expectedCategory, cat, err)
			}
		})
	}
}

func TestOpenAIAdapter_Redaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "Invalid key sk-leak-test-12345 in parameter"}}`))
	}))
	defer server.Close()

	adapter := agentruntime.NewOpenAIAdapter(
		agentruntime.WithOpenAIBaseURL(server.URL),
		agentruntime.WithOpenAIHTTPClient(server.Client()),
	)

	ctx := agentruntime.WithAPIKey(context.Background(), "sk-leak-test-12345")
	_, err := adapter.Generate(ctx, agentruntime.LLMRequest{Prompt: "test"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errStr := err.Error()
	if strings.Contains(errStr, "sk-leak-test-12345") {
		t.Fatalf("upstream error leaked secret token: %s", errStr)
	}
}

func TestOpenAIAdapter_TimeoutAndCancellation(t *testing.T) {
	// 1. Timeout
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	adapter := agentruntime.NewOpenAIAdapter(
		agentruntime.WithOpenAIBaseURL(server.URL),
		agentruntime.WithOpenAIHTTPClient(server.Client()),
		agentruntime.WithOpenAITimeout(20*time.Millisecond),
	)

	ctx := agentruntime.WithAPIKey(context.Background(), "sk-key")
	_, err := adapter.Generate(ctx, agentruntime.LLMRequest{Prompt: "test"})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}

	// 2. Cancellation
	ctxCancel, cancel := context.WithCancel(context.Background())
	cancel()
	ctxCancel = agentruntime.WithAPIKey(ctxCancel, "sk-key")
	_, err = adapter.Generate(ctxCancel, agentruntime.LLMRequest{Prompt: "test"})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestOpenAIAdapter_ReasoningModelRestrictions(t *testing.T) {
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"model": "o1-mini",
			"choices": [{"message": {"content": "Reasoning model completion"}}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 15, "total_tokens": 25}
		}`))
	}))
	defer server.Close()

	adapter := agentruntime.NewOpenAIAdapter(
		agentruntime.WithOpenAIBaseURL(server.URL),
		agentruntime.WithOpenAIHTTPClient(server.Client()),
	)

	// 1. o1 rejects custom temperature != 1.0
	customTemp := 0.7
	_, err := adapter.GenerateWithCredential(context.Background(), "sk-test-key", agentruntime.LLMRequest{
		Prompt: "Solve math problem",
		Model:  "o1",
		Parameters: &agentruntime.GenerationParams{
			Temperature: &customTemp,
		},
	})
	if err == nil {
		t.Fatal("expected permanent error for o1 with custom temperature, got nil")
	}
	if agentruntime.ClassifyError(err) != agentruntime.CategoryPermanent {
		t.Fatalf("expected permanent category, got %s", agentruntime.ClassifyError(err))
	}
	if !strings.Contains(err.Error(), "temperature") {
		t.Fatalf("expected error mentioning temperature, got: %v", err)
	}

	// 2. o1 rejects top_p
	topP := 0.95
	_, err = adapter.GenerateWithCredential(context.Background(), "sk-test-key", agentruntime.LLMRequest{
		Prompt: "Solve logic puzzle",
		Model:  "o1-mini",
		Parameters: &agentruntime.GenerationParams{
			TopP: &topP,
		},
	})
	if err == nil {
		t.Fatal("expected permanent error for o1-mini with top_p, got nil")
	}
	if agentruntime.ClassifyError(err) != agentruntime.CategoryPermanent {
		t.Fatalf("expected permanent category, got %s", agentruntime.ClassifyError(err))
	}

	// 3. o1 maps max_tokens to max_completion_tokens
	maxTok := 500
	tempDefault := 1.0
	resp, err := adapter.GenerateWithCredential(context.Background(), "sk-test-key", agentruntime.LLMRequest{
		Prompt: "Explain general relativity",
		Model:  "o1-mini",
		Parameters: &agentruntime.GenerationParams{
			Temperature: &tempDefault,
			MaxTokens:   &maxTok,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error for valid o1 request: %v", err)
	}
	if resp.Content != "Reasoning model completion" {
		t.Fatalf("unexpected response content: %s", resp.Content)
	}

	// Verify request JSON body contained max_completion_tokens, not max_tokens
	if _, ok := capturedBody["max_tokens"]; ok {
		t.Fatalf("o1 request must NOT have max_tokens in body: %v", capturedBody)
	}
	if maxComp, ok := capturedBody["max_completion_tokens"]; !ok || int(maxComp.(float64)) != 500 {
		t.Fatalf("expected max_completion_tokens=500 in o1 request body, got: %v", capturedBody)
	}
}

func TestOpenAIAdapter_ParameterBoundsValidation(t *testing.T) {
	adapter := agentruntime.NewOpenAIAdapter()

	testCases := []struct {
		name   string
		params *agentruntime.GenerationParams
		errSub string
	}{
		{
			name: "Temperature too high (>2.0)",
			params: func() *agentruntime.GenerationParams {
				v := 2.5
				return &agentruntime.GenerationParams{Temperature: &v}
			}(),
			errSub: "temperature must be between 0.0 and 2.0",
		},
		{
			name: "Temperature negative (<0.0)",
			params: func() *agentruntime.GenerationParams {
				v := -0.5
				return &agentruntime.GenerationParams{Temperature: &v}
			}(),
			errSub: "temperature must be between 0.0 and 2.0",
		},
		{
			name: "TopP too high (>1.0)",
			params: func() *agentruntime.GenerationParams {
				v := 1.2
				return &agentruntime.GenerationParams{TopP: &v}
			}(),
			errSub: "top_p must be between 0.0 and 1.0",
		},
		{
			name: "TopP negative (<0.0)",
			params: func() *agentruntime.GenerationParams {
				v := -0.1
				return &agentruntime.GenerationParams{TopP: &v}
			}(),
			errSub: "top_p must be between 0.0 and 1.0",
		},
		{
			name: "MaxTokens zero or negative",
			params: func() *agentruntime.GenerationParams {
				v := 0
				return &agentruntime.GenerationParams{MaxTokens: &v}
			}(),
			errSub: "max_tokens must be greater than 0",
		},
		{
			name: "Stop sequences > 4 items",
			params: &agentruntime.GenerationParams{
				Stop: []string{"s1", "s2", "s3", "s4", "s5"},
			},
			errSub: "stop sequence list cannot exceed 4 items",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adapter.GenerateWithCredential(context.Background(), "sk-fake-key", agentruntime.LLMRequest{
				Prompt:     "test",
				Model:      "gpt-4o",
				Parameters: tc.params,
			})
			if err == nil {
				t.Fatalf("expected parameter validation error, got nil")
			}
			if agentruntime.ClassifyError(err) != agentruntime.CategoryPermanent {
				t.Fatalf("expected permanent category, got %s", agentruntime.ClassifyError(err))
			}
			if !strings.Contains(err.Error(), tc.errSub) {
				t.Fatalf("expected error containing %q, got: %v", tc.errSub, err)
			}
		})
	}
}

func TestOpenAIAdapter_UnexpectedContentTypeAndMalformedJSON(t *testing.T) {
	// 1. 200 OK with HTML content-type
	htmlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><body>502 Bad Gateway Proxy Error</body></html>`))
	}))
	defer htmlServer.Close()

	adapterHTML := agentruntime.NewOpenAIAdapter(
		agentruntime.WithOpenAIBaseURL(htmlServer.URL),
		agentruntime.WithOpenAIHTTPClient(htmlServer.Client()),
	)

	_, err := adapterHTML.GenerateWithCredential(context.Background(), "sk-test-key", agentruntime.LLMRequest{
		Prompt: "test",
		Model:  "gpt-4o",
	})
	if err == nil {
		t.Fatal("expected error for HTML content type, got nil")
	}
	if agentruntime.ClassifyError(err) != agentruntime.CategoryTransient {
		t.Fatalf("expected transient error for unexpected content type, got %s (err: %v)", agentruntime.ClassifyError(err), err)
	}

	// 2. 200 OK with malformed JSON
	badJSONServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"role": "assista`)) // Truncated
	}))
	defer badJSONServer.Close()

	adapterJSON := agentruntime.NewOpenAIAdapter(
		agentruntime.WithOpenAIBaseURL(badJSONServer.URL),
		agentruntime.WithOpenAIHTTPClient(badJSONServer.Client()),
	)

	_, err = adapterJSON.GenerateWithCredential(context.Background(), "sk-test-key", agentruntime.LLMRequest{
		Prompt: "test",
		Model:  "gpt-4o",
	})
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if agentruntime.ClassifyError(err) != agentruntime.CategoryTransient {
		t.Fatalf("expected transient error for malformed JSON, got %s (err: %v)", agentruntime.ClassifyError(err), err)
	}
}

func TestOpenAIAdapter_TransportSecretRedaction(t *testing.T) {
	fakeSecret := "sk-fake-secret-never-expose-99999"

	// Mock server that returns 400 with the fake secret in error message
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "Invalid token sk-fake-secret-never-expose-99999 provided for organization"}}`))
	}))
	defer server.Close()

	adapter := agentruntime.NewOpenAIAdapter(
		agentruntime.WithOpenAIBaseURL(server.URL),
		agentruntime.WithOpenAIHTTPClient(server.Client()),
	)

	_, err := adapter.GenerateWithCredential(context.Background(), fakeSecret, agentruntime.LLMRequest{
		Prompt: "test",
		Model:  "gpt-4o",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errMsg := err.Error()
	if strings.Contains(errMsg, fakeSecret) {
		t.Fatalf("CRITICAL SECURITY VIOLATION: error leaked fakeSecret! Error: %s", errMsg)
	}
}
