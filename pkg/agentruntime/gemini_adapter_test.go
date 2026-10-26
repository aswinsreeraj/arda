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

func TestGeminiAdapter_Success(t *testing.T) {
	fakeKey := "AIzaSyFakeKeySecret123456789012345"
	var capturedAuth string
	var capturedPath string
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("x-goog-api-key")
		capturedPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"candidates": [
				{
					"content": {
						"parts": [
							{"text": "Hello from Gemini 1.5 Pro!"}
						],
						"role": "model"
					},
					"finishReason": "STOP"
				}
			],
			"usageMetadata": {
				"promptTokenCount": 16,
				"candidatesTokenCount": 24,
				"totalTokenCount": 40
			}
		}`))
	}))
	defer server.Close()

	adapter := agentruntime.NewGeminiAdapter(
		agentruntime.WithGeminiBaseURL(server.URL),
		agentruntime.WithGeminiHTTPClient(server.Client()),
	)

	if adapter.ProviderID() != "google" {
		t.Fatalf("expected provider ID 'google', got %q", adapter.ProviderID())
	}

	resp, err := adapter.GenerateWithCredential(context.Background(), fakeKey, agentruntime.LLMRequest{
		Prompt: "Hello Gemini",
		Model:  "gemini-1.5-pro",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedAuth != fakeKey {
		t.Fatalf("expected x-goog-api-key %q, got %q", fakeKey, capturedAuth)
	}
	if !strings.Contains(capturedPath, "/models/gemini-1.5-pro:generateContent") {
		t.Fatalf("expected path to contain generateContent endpoint, got %q", capturedPath)
	}
	if resp.Content != "Hello from Gemini 1.5 Pro!" {
		t.Fatalf("unexpected content: %q", resp.Content)
	}
	if resp.Usage.PromptTokens != 16 || resp.Usage.CompletionTokens != 24 {
		t.Fatalf("unexpected token usage: %+v", resp.Usage)
	}
}

func TestGeminiAdapter_ModelValidation(t *testing.T) {
	adapter := agentruntime.NewGeminiAdapter()

	if !adapter.IsModelSupported("gemini-1.5-pro") {
		t.Error("expected gemini-1.5-pro to be supported")
	}
	if !adapter.IsModelSupported("gemini-1.5-flash") {
		t.Error("expected gemini-1.5-flash to be supported")
	}
	if !adapter.IsModelSupported("gemini-2.0-flash-exp") {
		t.Error("expected gemini-2.0-flash-exp to be supported")
	}
	if adapter.IsModelSupported("gpt-4o") {
		t.Error("expected gpt-4o to be unsupported on Gemini adapter")
	}
}

func TestGeminiAdapter_SecretRedaction(t *testing.T) {
	fakeSecret := "AIzaSyFakeSecretKeyNeverLeak1234567"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "API key not valid: AIzaSyFakeSecretKeyNeverLeak1234567", "status": "INVALID_ARGUMENT"}}`))
	}))
	defer server.Close()

	adapter := agentruntime.NewGeminiAdapter(
		agentruntime.WithGeminiBaseURL(server.URL),
		agentruntime.WithGeminiHTTPClient(server.Client()),
	)

	_, err := adapter.GenerateWithCredential(context.Background(), fakeSecret, agentruntime.LLMRequest{
		Prompt: "test",
		Model:  "gemini-1.5-pro",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errMsg := err.Error()
	if strings.Contains(errMsg, fakeSecret) {
		t.Fatalf("CRITICAL SECURITY VIOLATION: leaked fakeSecret in error: %s", errMsg)
	}
}
