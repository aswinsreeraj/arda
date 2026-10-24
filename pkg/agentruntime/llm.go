package agentruntime

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// ErrInvalidLLMRequest indicates that an LLMRequest failed validation (e.g. empty prompt).
var ErrInvalidLLMRequest = errors.New("agentruntime: invalid llm request")

// GenerationParams holds optional hyperparameters and constraints for text generation.
type GenerationParams struct {
	// Temperature controls randomness in generation (e.g. 0.0 for deterministic output).
	Temperature *float64 `json:"temperature,omitempty"`

	// MaxTokens bounds the number of generated tokens.
	MaxTokens *int `json:"max_tokens,omitempty"`

	// TopP enables nucleus sampling.
	TopP *float64 `json:"top_p,omitempty"`

	// Stop specifies stop sequences that halt generation.
	Stop []string `json:"stop,omitempty"`

	// Extra holds provider-agnostic auxiliary options when needed.
	Extra map[string]any `json:"extra,omitempty"`
}

// LLMRequest carries the input parameters for text generation.
type LLMRequest struct {
	// Prompt is the fully constructed prompt text to send to the model.
	Prompt string `json:"prompt"`

	// Model is an optional model identifier (e.g. "gpt-4o", "claude-3-5-sonnet", "gemini-1.5-pro").
	Model string `json:"model,omitempty"`

	// Parameters contains optional generation parameters.
	Parameters *GenerationParams `json:"parameters,omitempty"`

	// Provider is an optional provider identifier (e.g. "openai", "anthropic", "gemini", "mock").
	Provider string `json:"provider,omitempty"`

	// TenantID identifies the tenant executing this request for scoped credential resolution.
	TenantID uuid.UUID `json:"tenant_id,omitempty"`

	// CredentialRef is an optional secret reference override for the provider credential.
	CredentialRef string `json:"credential_ref,omitempty"`
}

// Validate verifies that the request has non-empty prompt content.
func (r LLMRequest) Validate() error {
	if strings.TrimSpace(r.Prompt) == "" {
		return ErrInvalidLLMRequest
	}
	return nil
}

// TokenUsage captures token consumption statistics for an inference call.
type TokenUsage struct {
	// PromptTokens is the number of tokens in the prompt.
	PromptTokens int `json:"prompt_tokens"`

	// CompletionTokens is the number of tokens in the generated completion.
	CompletionTokens int `json:"completion_tokens"`

	// TotalTokens is the sum of prompt and completion tokens.
	TotalTokens int `json:"total_tokens"`
}

// LLMResponse contains the generated output text and associated metadata.
type LLMResponse struct {
	// Content is the generated text returned by the model.
	Content string `json:"content"`

	// Model is the model identifier that produced the response, when available.
	Model string `json:"model,omitempty"`

	// Usage contains token consumption statistics, when available.
	Usage *TokenUsage `json:"usage,omitempty"`
}

// LLMClient defines the provider-independent contract for generating text using an LLM.
// It keeps external provider SDKs and credentials decoupled from ARDA core execution logic.
type LLMClient interface {
	Generate(ctx context.Context, request LLMRequest) (LLMResponse, error)
}
