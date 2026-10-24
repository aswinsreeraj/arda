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
