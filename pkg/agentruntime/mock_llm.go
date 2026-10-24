package agentruntime

import (
	"context"
)

// MockLLMClient provides deterministic LLM text generation for unit tests, local development,
// and workflow increment testing without making real network requests or requiring provider credentials.
type MockLLMClient struct {
	Response     LLMResponse
	Err          error
	GenerateFunc func(ctx context.Context, request LLMRequest) (LLMResponse, error)
	LastRequest  LLMRequest
	Calls        int
}

// Generate returns the configured response or error, recording call metrics and respecting context cancellation.
func (m *MockLLMClient) Generate(ctx context.Context, request LLMRequest) (LLMResponse, error) {
	if err := ctx.Err(); err != nil {
		return LLMResponse{}, err
	}

	if err := request.Validate(); err != nil {
		return LLMResponse{}, err
	}

	m.Calls++
	m.LastRequest = request

	if m.GenerateFunc != nil {
		return m.GenerateFunc(ctx, request)
	}

	if m.Err != nil {
		return LLMResponse{}, m.Err
	}

	resp := m.Response
	if resp.Content == "" && resp.Model == "" && resp.Usage == nil {
		modelName := request.Model
		if modelName == "" {
			modelName = "mock-model-v1"
		}
		resp = LLMResponse{
			Content: "deterministic mock llm response",
			Model:   modelName,
			Usage: &TokenUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		}
	}

	return resp, nil
}

// NewMockLLMClient creates a MockLLMClient with standard deterministic response defaults.
func NewMockLLMClient() *MockLLMClient {
	return &MockLLMClient{
		Response: LLMResponse{
			Content: "deterministic mock llm response",
			Model:   "mock-model-v1",
			Usage: &TokenUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		},
	}
}

// ProviderID returns "mock" to fulfill the ProviderAdapter interface.
func (m *MockLLMClient) ProviderID() string {
	return "mock"
}

// SupportedModels returns supported mock models to fulfill the ProviderAdapter interface.
func (m *MockLLMClient) SupportedModels() []string {
	return []string{"mock-model-v1", "mock-model-v2"}
}

// IsModelSupported accepts any model name for mock execution.
func (m *MockLLMClient) IsModelSupported(model string) bool {
	return true
}
