package agentruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
)

func TestMockLLMClient_DefaultResponse(t *testing.T) {
	client := agentruntime.NewMockLLMClient()
	req := agentruntime.LLMRequest{
		Prompt: "Summarize this report",
	}

	resp, err := client.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("expected successful generation, got error: %v", err)
	}

	if resp.Content != "deterministic mock llm response" {
		t.Errorf("expected default content 'deterministic mock llm response', got: %s", resp.Content)
	}
	if resp.Model != "mock-model-v1" {
		t.Errorf("expected default model 'mock-model-v1', got: %s", resp.Model)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 15 {
		t.Errorf("expected total tokens 15, got: %+v", resp.Usage)
	}

	if client.Calls != 1 {
		t.Errorf("expected 1 call recorded, got: %d", client.Calls)
	}
	if client.LastRequest.Prompt != req.Prompt {
		t.Errorf("expected last request prompt %q, got %q", req.Prompt, client.LastRequest.Prompt)
	}
}

func TestMockLLMClient_ConfiguredResponse(t *testing.T) {
	temp := 0.2
	maxTokens := 256
	client := agentruntime.NewMockLLMClient()
	client.Response = agentruntime.LLMResponse{
		Content: "Custom generated response text",
		Model:   "claude-3-5-sonnet",
		Usage: &agentruntime.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	req := agentruntime.LLMRequest{
		Prompt: "Draft an escalation response",
		Model:  "claude-3-5-sonnet",
		Parameters: &agentruntime.GenerationParams{
			Temperature: &temp,
			MaxTokens:   &maxTokens,
		},
	}

	resp, err := client.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected generation error: %v", err)
	}

	if resp.Content != "Custom generated response text" {
		t.Errorf("expected content 'Custom generated response text', got: %s", resp.Content)
	}
	if resp.Model != "claude-3-5-sonnet" {
		t.Errorf("expected model 'claude-3-5-sonnet', got: %s", resp.Model)
	}
	if resp.Usage.PromptTokens != 100 || resp.Usage.CompletionTokens != 50 || resp.Usage.TotalTokens != 150 {
		t.Errorf("unexpected token usage: %+v", resp.Usage)
	}
}

func TestMockLLMClient_ConfiguredError(t *testing.T) {
	expectedErr := errors.New("upstream rate limit exceeded (HTTP 429)")
	client := agentruntime.NewMockLLMClient()
	client.Err = expectedErr

	req := agentruntime.LLMRequest{
		Prompt: "Process transaction",
	}

	_, err := client.Generate(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error from mock client, got nil")
	}

	if !errors.Is(err, expectedErr) {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}
}

func TestMockLLMClient_ContextCancellation(t *testing.T) {
	client := agentruntime.NewMockLLMClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel context

	req := agentruntime.LLMRequest{
		Prompt: "Hello world",
	}

	_, err := client.Generate(ctx, req)
	if err == nil {
		t.Fatalf("expected context canceled error, got nil")
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestMockLLMClient_GenerateFunc(t *testing.T) {
	client := agentruntime.NewMockLLMClient()
	customCalled := false

	client.GenerateFunc = func(ctx context.Context, request agentruntime.LLMRequest) (agentruntime.LLMResponse, error) {
		customCalled = true
		return agentruntime.LLMResponse{
			Content: "dynamically generated via GenerateFunc",
			Model:   request.Model,
		}, nil
	}

	req := agentruntime.LLMRequest{
		Prompt: "Testing GenerateFunc hook",
		Model:  "custom-hook-model",
	}

	resp, err := client.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !customCalled {
		t.Errorf("expected GenerateFunc hook to be executed")
	}
	if resp.Content != "dynamically generated via GenerateFunc" {
		t.Errorf("unexpected response content: %s", resp.Content)
	}
	if resp.Model != "custom-hook-model" {
		t.Errorf("unexpected model: %s", resp.Model)
	}
}

func TestLLMRequest_Validation(t *testing.T) {
	tests := []struct {
		name      string
		request   agentruntime.LLMRequest
		expectErr bool
	}{
		{
			name: "valid request",
			request: agentruntime.LLMRequest{
				Prompt: "Translate text to French",
			},
			expectErr: false,
		},
		{
			name: "empty prompt",
			request: agentruntime.LLMRequest{
				Prompt: "",
			},
			expectErr: true,
		},
		{
			name: "whitespace prompt",
			request: agentruntime.LLMRequest{
				Prompt: "    \t\n   ",
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Validate()
			if (err != nil) != tt.expectErr {
				t.Errorf("Validate() error = %v, expectErr = %v", err, tt.expectErr)
			}
			if tt.expectErr && !errors.Is(err, agentruntime.ErrInvalidLLMRequest) {
				t.Errorf("expected ErrInvalidLLMRequest, got: %v", err)
			}
		})
	}
}
