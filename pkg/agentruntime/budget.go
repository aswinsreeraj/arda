package agentruntime

import (
	"encoding/json"
	"fmt"

	"github.com/aswinsreeraj/arda/services/registry/domain"
)

const (
	DefaultMaxIterations                = 10
	DefaultMaxToolInvocations           = 20
	DefaultMaxTotalTokens               = 50000
	DefaultMaxPromptTokens              = 40000
	DefaultMaxCompletionTokens          = 10000
	DefaultMaxToolResultBytes           = 65536  // 64 KB
	DefaultMaxContextBytes              = 262144 // 256 KB
	DefaultMaxConsecutiveIdenticalCalls = 1
)

// BudgetConfig defines deterministic hard limits for an agent execution loop.
type BudgetConfig struct {
	MaxIterations                int `json:"max_iterations"`
	MaxToolInvocations           int `json:"max_tool_invocations"`
	MaxTotalTokens               int `json:"max_total_tokens"`
	MaxPromptTokens              int `json:"max_prompt_tokens"`
	MaxCompletionTokens          int `json:"max_completion_tokens"`
	MaxToolResultBytes           int `json:"max_tool_result_bytes"`
	MaxContextBytes              int `json:"max_context_bytes"`
	MaxConsecutiveIdenticalCalls int `json:"max_consecutive_identical_calls"`
}

// DefaultBudgetConfig returns standard conservative limits for bounded execution.
func DefaultBudgetConfig() BudgetConfig {
	return BudgetConfig{
		MaxIterations:                DefaultMaxIterations,
		MaxToolInvocations:           DefaultMaxToolInvocations,
		MaxTotalTokens:               DefaultMaxTotalTokens,
		MaxPromptTokens:              DefaultMaxPromptTokens,
		MaxCompletionTokens:          DefaultMaxCompletionTokens,
		MaxToolResultBytes:           DefaultMaxToolResultBytes,
		MaxContextBytes:              DefaultMaxContextBytes,
		MaxConsecutiveIdenticalCalls: DefaultMaxConsecutiveIdenticalCalls,
	}
}

// Validate verifies that all budget limits are strictly positive.
func (b BudgetConfig) Validate() error {
	if b.MaxIterations <= 0 {
		return fmt.Errorf("%w: max_iterations must be positive, got %d", ErrInvalidConfiguration, b.MaxIterations)
	}
	if b.MaxToolInvocations <= 0 {
		return fmt.Errorf("%w: max_tool_invocations must be positive, got %d", ErrInvalidConfiguration, b.MaxToolInvocations)
	}
	if b.MaxTotalTokens <= 0 {
		return fmt.Errorf("%w: max_total_tokens must be positive, got %d", ErrInvalidConfiguration, b.MaxTotalTokens)
	}
	if b.MaxPromptTokens <= 0 {
		return fmt.Errorf("%w: max_prompt_tokens must be positive, got %d", ErrInvalidConfiguration, b.MaxPromptTokens)
	}
	if b.MaxCompletionTokens <= 0 {
		return fmt.Errorf("%w: max_completion_tokens must be positive, got %d", ErrInvalidConfiguration, b.MaxCompletionTokens)
	}
	if b.MaxToolResultBytes <= 0 {
		return fmt.Errorf("%w: max_tool_result_bytes must be positive, got %d", ErrInvalidConfiguration, b.MaxToolResultBytes)
	}
	if b.MaxContextBytes <= 0 {
		return fmt.Errorf("%w: max_context_bytes must be positive, got %d", ErrInvalidConfiguration, b.MaxContextBytes)
	}
	if b.MaxConsecutiveIdenticalCalls <= 0 {
		return fmt.Errorf("%w: max_consecutive_identical_calls must be positive, got %d", ErrInvalidConfiguration, b.MaxConsecutiveIdenticalCalls)
	}
	return nil
}

// ExtractBudgetConfig constructs a validated BudgetConfig from domain.Configuration.
func ExtractBudgetConfig(cfg domain.Configuration) (BudgetConfig, error) {
	budget := DefaultBudgetConfig()

	if cfg.Budgets != nil {
		getInt := func(key string, target *int) error {
			if v, ok := cfg.Budgets[key]; ok {
				switch val := v.(type) {
				case int:
					*target = val
				case int64:
					*target = int(val)
				case float64:
					*target = int(val)
				case json.Number:
					parsed, err := val.Int64()
					if err != nil {
						return fmt.Errorf("%w: invalid numeric budget %s: %v", ErrInvalidConfiguration, key, err)
					}
					*target = int(parsed)
				default:
					return fmt.Errorf("%w: invalid type for budget %s: %T", ErrInvalidConfiguration, key, v)
				}
			}
			return nil
		}

		if err := getInt("max_iterations", &budget.MaxIterations); err != nil {
			return budget, err
		}
		if err := getInt("max_tool_invocations", &budget.MaxToolInvocations); err != nil {
			return budget, err
		}
		if err := getInt("max_total_tokens", &budget.MaxTotalTokens); err != nil {
			return budget, err
		}
		if err := getInt("max_prompt_tokens", &budget.MaxPromptTokens); err != nil {
			return budget, err
		}
		if err := getInt("max_completion_tokens", &budget.MaxCompletionTokens); err != nil {
			return budget, err
		}
		if err := getInt("max_tool_result_bytes", &budget.MaxToolResultBytes); err != nil {
			return budget, err
		}
		if err := getInt("max_context_bytes", &budget.MaxContextBytes); err != nil {
			return budget, err
		}
		if err := getInt("max_consecutive_identical_calls", &budget.MaxConsecutiveIdenticalCalls); err != nil {
			return budget, err
		}
	}

	if err := budget.Validate(); err != nil {
		return budget, err
	}
	return budget, nil
}

// BudgetTracker enforces limits incrementally throughout execution.
type BudgetTracker struct {
	config           BudgetConfig
	iterations       int
	toolInvocations  int
	promptTokens     int
	completionTokens int
	totalTokens      int
}

// NewBudgetTracker initializes a tracker with validated configuration.
func NewBudgetTracker(config BudgetConfig) *BudgetTracker {
	return &BudgetTracker{
		config: config,
	}
}

// RecordIteration records an inference iteration and enforces the iteration budget.
func (b *BudgetTracker) RecordIteration() error {
	b.iterations++
	if b.iterations > b.config.MaxIterations {
		return fmt.Errorf("%w: iteration limit exceeded (%d/%d)", ErrBudgetExceeded, b.iterations, b.config.MaxIterations)
	}
	return nil
}

// RecordToolInvocation records a tool invocation and enforces the tool budget.
func (b *BudgetTracker) RecordToolInvocation() error {
	b.toolInvocations++
	if b.toolInvocations > b.config.MaxToolInvocations {
		return fmt.Errorf("%w: tool invocation limit exceeded (%d/%d)", ErrBudgetExceeded, b.toolInvocations, b.config.MaxToolInvocations)
	}
	return nil
}

// RecordUsage accumulates token usage and enforces prompt, completion, and total token budgets.
func (b *BudgetTracker) RecordUsage(usage *TokenUsage) error {
	if usage == nil {
		return nil
	}
	b.promptTokens += usage.PromptTokens
	b.completionTokens += usage.CompletionTokens
	if usage.TotalTokens > 0 {
		b.totalTokens += usage.TotalTokens
	} else {
		b.totalTokens += usage.PromptTokens + usage.CompletionTokens
	}

	if b.promptTokens > b.config.MaxPromptTokens {
		return fmt.Errorf("%w: prompt token budget exceeded (%d/%d)", ErrBudgetExceeded, b.promptTokens, b.config.MaxPromptTokens)
	}
	if b.completionTokens > b.config.MaxCompletionTokens {
		return fmt.Errorf("%w: completion token budget exceeded (%d/%d)", ErrBudgetExceeded, b.completionTokens, b.config.MaxCompletionTokens)
	}
	if b.totalTokens > b.config.MaxTotalTokens {
		return fmt.Errorf("%w: total token budget exceeded (%d/%d)", ErrBudgetExceeded, b.totalTokens, b.config.MaxTotalTokens)
	}
	return nil
}

// CheckToolResultSize verifies that a tool result does not exceed the allowed byte limit.
func (b *BudgetTracker) CheckToolResultSize(size int) error {
	if size > b.config.MaxToolResultBytes {
		return fmt.Errorf("%w: tool result size %d bytes exceeds budget limit %d bytes", ErrBudgetExceeded, size, b.config.MaxToolResultBytes)
	}
	return nil
}

// CheckContextSize verifies that the accumulated context does not exceed the allowed byte limit.
func (b *BudgetTracker) CheckContextSize(size int) error {
	if size > b.config.MaxContextBytes {
		return fmt.Errorf("%w: accumulated prompt context size %d bytes exceeds budget limit %d bytes", ErrBudgetExceeded, size, b.config.MaxContextBytes)
	}
	return nil
}

// Iterations returns current count of completed LLM iterations.
func (b *BudgetTracker) Iterations() int {
	return b.iterations
}

// ToolInvocations returns current count of completed tool invocations.
func (b *BudgetTracker) ToolInvocations() int {
	return b.toolInvocations
}

// TokenUsage returns accumulated token usage statistics.
func (b *BudgetTracker) TokenUsage() TokenUsage {
	return TokenUsage{
		PromptTokens:     b.promptTokens,
		CompletionTokens: b.completionTokens,
		TotalTokens:      b.totalTokens,
	}
}
