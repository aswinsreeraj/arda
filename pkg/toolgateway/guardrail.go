package toolgateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// GuardrailContext provides contextual execution metadata and arguments to guardrail evaluators.
type GuardrailContext struct {
	TenantID       uuid.UUID
	AgentID        uuid.UUID
	AgentVersionID uuid.UUID
	ExecutionID    string
	Tool           ToolDefinition
	Arguments      map[string]any
}

// GuardrailDenialError represents an explicit, deterministic business rule or policy violation.
type GuardrailDenialError struct {
	Rule       string
	Reason     string
	DenialCode string
}

func (e *GuardrailDenialError) Error() string {
	return fmt.Sprintf("guardrail rule %q denied execution: %s", e.Rule, e.Reason)
}

func (e *GuardrailDenialError) Is(target error) bool {
	return target == ErrGuardrailDenied
}

// Guardrail evaluates deterministic rules before tool dispatch.
type Guardrail interface {
	Evaluate(ctx context.Context, evalCtx GuardrailContext) error
}

// CompositeGuardrail evaluates a slice of guardrails sequentially. Fails closed immediately on violation or error.
type CompositeGuardrail struct {
	guardrails []Guardrail
}

// NewCompositeGuardrail constructs a composite evaluator.
func NewCompositeGuardrail(guardrails ...Guardrail) *CompositeGuardrail {
	return &CompositeGuardrail{guardrails: guardrails}
}

func (c *CompositeGuardrail) Evaluate(ctx context.Context, evalCtx GuardrailContext) error {
	for _, g := range c.guardrails {
		if err := g.Evaluate(ctx, evalCtx); err != nil {
			return err
		}
	}
	return nil
}

// DestructiveOperationGuardrail scans arguments for disallowed destructive patterns in SQL/shell commands.
type DestructiveOperationGuardrail struct {
	disallowedKeywords []string
}

// NewDestructiveOperationGuardrail constructs a guardrail blocking destructive SQL and OS commands.
func NewDestructiveOperationGuardrail() *DestructiveOperationGuardrail {
	return &DestructiveOperationGuardrail{
		disallowedKeywords: []string{
			"DROP TABLE",
			"TRUNCATE TABLE",
			"DROP DATABASE",
			"rm -rf",
			":(){ :|:& };:",
		},
	}
}

func (g *DestructiveOperationGuardrail) Evaluate(ctx context.Context, evalCtx GuardrailContext) error {
	// Recursively inspect string values in arguments
	var checkValue func(v any) error
	checkValue = func(v any) error {
		switch val := v.(type) {
		case string:
			upper := strings.ToUpper(val)
			for _, keyword := range g.disallowedKeywords {
				if strings.Contains(upper, keyword) {
					return &GuardrailDenialError{
						Rule:       "destructive_operation",
						Reason:     fmt.Sprintf("argument contains forbidden destructive pattern: %s", keyword),
						DenialCode: DenialCodeGuardrailDenied,
					}
				}
			}
		case map[string]any:
			for _, subv := range val {
				if err := checkValue(subv); err != nil {
					return err
				}
			}
		case []any:
			for _, subv := range val {
				if err := checkValue(subv); err != nil {
					return err
				}
			}
		}
		return nil
	}

	return checkValue(evalCtx.Arguments)
}

// ParameterBoundsGuardrail validates that numeric arguments do not exceed hard system thresholds.
type ParameterBoundsGuardrail struct {
	MaxAmount float64
}

// NewParameterBoundsGuardrail constructs a parameter bounds guardrail.
func NewParameterBoundsGuardrail(maxAmount float64) *ParameterBoundsGuardrail {
	return &ParameterBoundsGuardrail{MaxAmount: maxAmount}
}

func (g *ParameterBoundsGuardrail) Evaluate(ctx context.Context, evalCtx GuardrailContext) error {
	if g.MaxAmount <= 0 {
		return nil
	}

	for k, v := range evalCtx.Arguments {
		if strings.Contains(strings.ToLower(k), "amount") || strings.Contains(strings.ToLower(k), "cost") {
			var num float64
			switch n := v.(type) {
			case float64:
				num = n
			case int:
				num = float64(n)
			case int64:
				num = float64(n)
			default:
				continue
			}

			if num > g.MaxAmount {
				return &GuardrailDenialError{
					Rule:       "parameter_bounds",
					Reason:     fmt.Sprintf("parameter %q value %.2f exceeds maximum allowed threshold of %.2f", k, num, g.MaxAmount),
					DenialCode: DenialCodeGuardrailDenied,
				}
			}
		}
	}
	return nil
}

// FailClosedGuardrail simulates an unavailable guardrail service to verify fail-closed behavior.
type FailClosedGuardrail struct {
	Err error
}

func (g *FailClosedGuardrail) Evaluate(ctx context.Context, evalCtx GuardrailContext) error {
	if g.Err != nil {
		return g.Err
	}
	return nil
}
