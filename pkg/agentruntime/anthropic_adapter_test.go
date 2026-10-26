package agentruntime_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
)

func TestAnthropicAdapter_Success(t *testing.T) {
	fakeKey := "sk-ant-test-api-key-secret-12345"
	var capturedAuth string
	var capturedVersion string
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("x-api-key")
		capturedVersion = r.Header.Get("anthropic-version")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "msg_01X9test",
			"type": "message",
			"role": "assistant",
			"model": "claude-3-5-sonnet-20241022",
			"content": [
				{
					"type": "text",
					"text": "Hello! I am Claude 3.5 Sonnet."
				}
			],
			"stop_reason": "end_turn",
			"usage": {
				"input_tokens": 15,
				"output_tokens": 28
			}
		}`))
	}))
	defer server.Close()

	adapter := agentruntime.NewAnthropicAdapter(
		agentruntime.WithAnthropicBaseURL(server.URL),
		agentruntime.WithAnthropicHTTPClient(server.Client()),
	)

	if adapter.ProviderID() != "anthropic" {
		t.Fatalf("expected provider ID 'anthropic', got %q", adapter.ProviderID())
	}

	temp := 0.5
	maxTokens := 2048
	resp, err := adapter.GenerateWithCredential(context.Background(), fakeKey, agentruntime.LLMRequest{
		Prompt: "What is ARDA?",
		Model:  "claude-3-5-sonnet-20241022",
		Parameters: &agentruntime.GenerationParams{
			Temperature: &temp,
			MaxTokens:   &maxTokens,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedAuth != fakeKey {
		t.Fatalf("expected x-api-key %q, got %q", fakeKey, capturedAuth)
	}
	if capturedVersion != "2023-06-01" {
		t.Fatalf("expected anthropic-version 2023-06-01, got %q", capturedVersion)
	}
	if resp.Content != "Hello! I am Claude 3.5 Sonnet." {
		t.Fatalf("unexpected response content: %q", resp.Content)
	}
	if resp.Usage.PromptTokens != 15 || resp.Usage.CompletionTokens != 28 {
		t.Fatalf("unexpected token usage: %+v", resp.Usage)
	}
}

func TestAnthropicAdapter_ModelValidation(t *testing.T) {
	adapter := agentruntime.NewAnthropicAdapter()

	if !adapter.IsModelSupported("claude-3-5-sonnet-20241022") {
		t.Error("expected claude-3-5-sonnet-20241022 to be supported")
	}
	if !adapter.IsModelSupported("claude-3-5-haiku-20241022") {
		t.Error("expected claude-3-5-haiku-20241022 to be supported")
	}
	if !adapter.IsModelSupported("claude-3-opus-latest") {
		t.Error("expected claude-3-opus-latest to be supported")
	}
	if adapter.IsModelSupported("gpt-4o") {
		t.Error("expected gpt-4o to be unsupported on Anthropic adapter")
	}
}

func TestAnthropicAdapter_SecretRedaction(t *testing.T) {
	fakeSecret := "sk-ant-fake-secret-99999"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type": "error", "error": {"type": "invalid_request_error", "message": "Invalid key sk-ant-fake-secret-99999"}}`))
	}))
	defer server.Close()

	adapter := agentruntime.NewAnthropicAdapter(
		agentruntime.WithAnthropicBaseURL(server.URL),
		agentruntime.WithAnthropicHTTPClient(server.Client()),
	)

	_, err := adapter.GenerateWithCredential(context.Background(), fakeSecret, agentruntime.LLMRequest{
		Prompt: "test",
		Model:  "claude-3-5-sonnet-20241022",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errMsg := err.Error()
	if strings.Contains(errMsg, fakeSecret) {
		t.Fatalf("CRITICAL SECURITY VIOLATION: leaked fakeSecret in error: %s", errMsg)
	}
}
