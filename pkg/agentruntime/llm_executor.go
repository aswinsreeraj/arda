package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/aswinsreeraj/arda/services/registry/domain"
)

// LLMExecutorOption configures optional behavior on an LLMExecutor.
type LLMExecutorOption func(*LLMExecutor)

// WithDefaultModel specifies a fallback model identifier when the agent configuration does not set one.
func WithDefaultModel(model string) LLMExecutorOption {
	return func(e *LLMExecutor) {
		e.defaultModel = model
	}
}

// WithDefaultExecutorProvider specifies a fallback provider identifier when the agent configuration does not set one.
func WithDefaultExecutorProvider(provider string) LLMExecutorOption {
	return func(e *LLMExecutor) {
		e.defaultProvider = strings.ToLower(strings.TrimSpace(provider))
	}
}

// WithPromptBuilder injects a custom PromptBuilder instance into the executor.
func WithPromptBuilder(b *PromptBuilder) LLMExecutorOption {
	return func(e *LLMExecutor) {
		if b != nil {
			e.promptBuilder = b
		}
	}
}

// LLMExecutor executes an agent run by fetching its pinned version configuration,
// rendering the prompt template against the execution payload, invoking the provider-independent
// LLMClient, and packaging the generated completion into a validated ExecutionResult.
type LLMExecutor struct {
	llmClient       LLMClient
	configProvider  AgentConfigProvider
	promptBuilder   *PromptBuilder
	defaultModel    string
	defaultProvider string
}

// NewLLMExecutor creates an LLMExecutor with mandatory LLMClient and AgentConfigProvider dependencies.
func NewLLMExecutor(llmClient LLMClient, configProvider AgentConfigProvider, opts ...LLMExecutorOption) (*LLMExecutor, error) {
	if llmClient == nil {
		return nil, ErrNilLLMClient
	}
	if configProvider == nil {
		return nil, ErrNilConfigProvider
	}

	exec := &LLMExecutor{
		llmClient:      llmClient,
		configProvider: configProvider,
		promptBuilder:  NewPromptBuilder(),
	}

	for _, opt := range opts {
		opt(exec)
	}

	return exec, nil
}

// Execute fulfills the Executor interface.
// It retrieves the exact pinned version configuration, constructs the prompt, generates text via the LLM,
// and returns the structured ExecutionResult.
func (e *LLMExecutor) Execute(ctx context.Context, input ExecutionInput) (ExecutionResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}

	if e == nil || e.llmClient == nil {
		return ExecutionResult{}, ErrNilLLMClient
	}
	if e.configProvider == nil {
		return ExecutionResult{}, ErrNilConfigProvider
	}

	if err := input.Validate(); err != nil {
		return ExecutionResult{}, err
	}

	// Retrieve exact pinned agent version configuration
	cfg, err := e.configProvider.GetAgentConfig(ctx, input.TenantID, input.AgentID, input.AgentVersionID)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ExecutionResult{}, err
		}
		if errors.Is(err, ErrConfigurationNotFound) {
			return ExecutionResult{}, err
		}
		return ExecutionResult{}, fmt.Errorf("%w: %v", ErrConfigurationNotFound, err)
	}

	// Validate configuration integrity
	if err := domain.ValidateConfiguration(cfg); err != nil {
		return ExecutionResult{}, fmt.Errorf("%w: %v", ErrInvalidConfiguration, err)
	}

	// Build prompt from template and input payload
	builder := e.promptBuilder
	if builder == nil {
		builder = NewPromptBuilder()
	}
	prompt, err := builder.BuildFromConfig(cfg, input.Payload)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("failed to build prompt: %w", err)
	}

	// Construct and validate LLM request
	req := e.buildRequest(input, cfg, prompt)
	if err := req.Validate(); err != nil {
		return ExecutionResult{}, err
	}

	// Propagate tenant identity into context for downstream Vault/auth resolution
	reqCtx := vault.WithTenantID(ctx, input.TenantID)

	// Invoke the provider-independent LLMClient
	resp, err := e.llmClient.Generate(reqCtx, req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ExecutionResult{}, err
		}
		return ExecutionResult{}, fmt.Errorf("llm generation failed: %w", err)
	}

	// Construct result without logging sensitive prompts, payloads, or outputs
	result := ExecutionResult{
		Status: StatusSuccess,
		Output: map[string]any{
			"content":  resp.Content,
			"response": resp.Content,
		},
	}
	if resp.Model != "" {
		result.Output["model"] = resp.Model
	}
	if resp.Usage != nil {
		result.Output["usage"] = map[string]any{
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
			"total_tokens":      resp.Usage.TotalTokens,
		}
	}

	if err := result.Validate(); err != nil {
		return ExecutionResult{}, err
	}

	return result, nil
}

// Run fulfills the AgentRuntime interface, allowing LLMExecutor to be used directly as an AgentRuntime.
func (e *LLMExecutor) Run(ctx context.Context, input ExecutionInput) (ExecutionResult, error) {
	return e.Execute(ctx, input)
}

func (e *LLMExecutor) buildRequest(input ExecutionInput, cfg domain.Configuration, prompt string) LLMRequest {
	model := e.extractModel(cfg)
	provider := e.extractProvider(cfg)
	credRef := e.extractCredentialRef(cfg)
	params := e.extractParams(cfg)

	return LLMRequest{
		Prompt:        prompt,
		Model:         model,
		Provider:      provider,
		TenantID:      input.TenantID,
		CredentialRef: credRef,
		Parameters:    params,
	}
}

func (e *LLMExecutor) extractProvider(cfg domain.Configuration) string {
	if cfg.LLMConfig != nil && strings.TrimSpace(cfg.LLMConfig.Provider) != "" {
		return strings.ToLower(strings.TrimSpace(cfg.LLMConfig.Provider))
	}
	if cfg.Guardrails != nil {
		if p, ok := cfg.Guardrails["provider"].(string); ok && strings.TrimSpace(p) != "" {
			return strings.ToLower(strings.TrimSpace(p))
		}
	}
	return e.defaultProvider
}

func (e *LLMExecutor) extractCredentialRef(cfg domain.Configuration) string {
	if cfg.LLMConfig != nil && strings.TrimSpace(cfg.LLMConfig.CredentialRef) != "" {
		return strings.TrimSpace(cfg.LLMConfig.CredentialRef)
	}
	if cfg.Guardrails != nil {
		if r, ok := cfg.Guardrails["credential_ref"].(string); ok && strings.TrimSpace(r) != "" {
			return strings.TrimSpace(r)
		}
		if r, ok := cfg.Guardrails["secret_ref"].(string); ok && strings.TrimSpace(r) != "" {
			return strings.TrimSpace(r)
		}
	}
	return ""
}

func (e *LLMExecutor) extractModel(cfg domain.Configuration) string {
	if cfg.LLMConfig != nil && strings.TrimSpace(cfg.LLMConfig.Model) != "" {
		return strings.TrimSpace(cfg.LLMConfig.Model)
	}
	if cfg.Guardrails != nil {
		if m, ok := cfg.Guardrails["model"].(string); ok && m != "" {
			return m
		}
		if m, ok := cfg.Guardrails["model_name"].(string); ok && m != "" {
			return m
		}
	}
	if cfg.Budgets != nil {
		if m, ok := cfg.Budgets["model"].(string); ok && m != "" {
			return m
		}
	}
	return e.defaultModel
}

func (e *LLMExecutor) extractParams(cfg domain.Configuration) *GenerationParams {
	var (
		tempVal   *float64
		maxTokVal *int
		topPVal   *float64
		stopList  []string
	)

	if cfg.LLMConfig != nil && cfg.LLMConfig.Parameters != nil {
		if t, ok := extractFloat(cfg.LLMConfig.Parameters["temperature"]); ok {
			tempVal = &t
		}
		if mt, ok := extractInt(cfg.LLMConfig.Parameters["max_tokens"]); ok {
			maxTokVal = &mt
		}
		if tp, ok := extractFloat(cfg.LLMConfig.Parameters["top_p"]); ok {
			topPVal = &tp
		}
		if st, ok := extractStringSlice(cfg.LLMConfig.Parameters["stop"]); ok {
			stopList = st
		}
	}

	if cfg.Guardrails != nil {
		if tempVal == nil {
			if t, ok := extractFloat(cfg.Guardrails["temperature"]); ok {
				tempVal = &t
			}
		}
		if maxTokVal == nil {
			if mt, ok := extractInt(cfg.Guardrails["max_tokens"]); ok {
				maxTokVal = &mt
			}
		}
		if topPVal == nil {
			if tp, ok := extractFloat(cfg.Guardrails["top_p"]); ok {
				topPVal = &tp
			}
		}
		if len(stopList) == 0 {
			if st, ok := extractStringSlice(cfg.Guardrails["stop"]); ok {
				stopList = st
			}
		}
	}

	if maxTokVal == nil && cfg.Budgets != nil {
		if mt, ok := extractInt(cfg.Budgets["max_tokens"]); ok {
			maxTokVal = &mt
		}
	}

	if tempVal == nil && maxTokVal == nil && topPVal == nil && len(stopList) == 0 {
		return nil
	}

	return &GenerationParams{
		Temperature: tempVal,
		MaxTokens:   maxTokVal,
		TopP:        topPVal,
		Stop:        stopList,
	}
}

func extractInt(val any) (int, bool) {
	if val == nil {
		return 0, false
	}
	switch v := val.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case int32:
		return int(v), true
	case float64:
		return int(v), true
	case float32:
		return int(v), true
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i), true
		}
		if f, err := v.Float64(); err == nil {
			return int(f), true
		}
	}
	return 0, false
}

func extractFloat(val any) (float64, bool) {
	if val == nil {
		return 0, false
	}
	switch v := val.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

func extractStringSlice(val any) ([]string, bool) {
	if val == nil {
		return nil, false
	}
	switch v := val.(type) {
	case []string:
		return v, true
	case []any:
		res := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				res = append(res, s)
			}
		}
		return res, true
	}
	return nil, false
}
