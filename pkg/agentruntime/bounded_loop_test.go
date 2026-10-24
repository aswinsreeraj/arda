package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
)

// Helper to construct a standard agent configuration for testing
func setupAgentConfig(tools []string, schema map[string]any, mode domain.ExecutionMode, threshold float64) (uuid.UUID, uuid.UUID, uuid.UUID, domain.Configuration) {
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	cfg := domain.Configuration{
		PromptTemplate:       "Answer the user prompt: {{input.query}}",
		Tools:                tools,
		ReliabilityThreshold: threshold,
		ExecutionMode:        mode,
	}
	if schema != nil {
		cfg.OutputSchema = &domain.OutputSchema{
			Name:   "DecisionOutput",
			Schema: schema,
			Strict: true,
		}
	}
	return tenantID, agentID, versionID, cfg
}

var standardDecisionSchema = map[string]any{
	"$schema":  "http://json-schema.org/draft-07/schema#",
	"type":     "object",
	"required": []any{"sentiment", "confidence"},
	"properties": map[string]any{
		"sentiment":  map[string]any{"type": "string"},
		"confidence": map[string]any{"type": "number"},
	},
	"additionalProperties": false,
}

// 1. One-step successful structured decision
func TestBoundedLoop_OneStepStructuredDecision(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig(nil, standardDecisionSchema, domain.ExecutionModeAutonomous, 0.8)
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	llmClient := NewMockLLMClient()
	llmClient.Response = LLMResponse{
		Content: `{"sentiment": "positive", "confidence": 0.95}`,
		Usage:   &TokenUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
	}

	exec, err := NewLLMExecutor(llmClient, configProvider)
	if err != nil {
		t.Fatalf("failed to create executor: %v", err)
	}

	input := ExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		Payload:        map[string]any{"query": "Hello"},
	}

	res, err := exec.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if res.Status != StatusSuccess {
		t.Errorf("expected status %s, got %s", StatusSuccess, res.Status)
	}
	if res.Outcome != OutcomeTypeDecision {
		t.Errorf("expected outcome %s, got %s", OutcomeTypeDecision, res.Outcome)
	}
	if !res.IsDecision() {
		t.Errorf("expected IsDecision to be true")
	}
	if res.StructuredOutput == nil {
		t.Fatalf("expected non-nil StructuredOutput")
	}
	if res.Metadata == nil || res.Metadata.Iterations != 1 || res.Metadata.ToolInvocations != 0 {
		t.Errorf("unexpected metadata: %+v", res.Metadata)
	}
}

// 2. Multi-step execution involving a tool request
func TestBoundedLoop_MultiStepToolExecution(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig([]string{"search_kb"}, standardDecisionSchema, domain.ExecutionModeAutonomous, 0.8)
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	callCount := 0
	llmClient := &MockLLMClient{
		GenerateFunc: func(ctx context.Context, request LLMRequest) (LLMResponse, error) {
			callCount++
			if callCount == 1 {
				// Step 1: Request tool call
				return LLMResponse{
					Content: `{"action": "tool_call", "tool_name": "search_kb", "arguments": {"query": "auth"}}`,
					Usage:   &TokenUsage{PromptTokens: 25, CompletionTokens: 15, TotalTokens: 40},
				}, nil
			}
			// Step 2: Final decision
			if !strings.Contains(request.Prompt, "[Tool Result for search_kb]") {
				return LLMResponse{}, fmt.Errorf("expected tool result in step 2 prompt")
			}
			return LLMResponse{
				Content: `{"sentiment": "positive", "confidence": 0.99}`,
				Usage:   &TokenUsage{PromptTokens: 50, CompletionTokens: 20, TotalTokens: 70},
			}, nil
		},
	}

	toolGateway := &MockToolGateway{
		ExecuteFunc: func(ctx context.Context, req ToolExecutionRequest) (ToolExecutionResponse, error) {
			if req.ToolName != "search_kb" {
				return ToolExecutionResponse{Allowed: false, Error: "unknown tool"}, nil
			}
			return ToolExecutionResponse{
				Allowed: true,
				Result:  map[string]any{"articles": []any{"auth_guide_v1"}},
				Output:  `{"articles":["auth_guide_v1"]}`,
			}, nil
		},
	}

	exec, err := NewLLMExecutor(llmClient, configProvider, WithToolGateway(toolGateway))
	if err != nil {
		t.Fatalf("failed to create executor: %v", err)
	}

	input := ExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		Payload:        map[string]any{"query": "How do I authenticate?"},
	}

	res, err := exec.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if res.Status != StatusSuccess || res.Outcome != OutcomeTypeDecision {
		t.Errorf("expected success decision, got status=%s outcome=%s", res.Status, res.Outcome)
	}
	if res.Metadata == nil {
		t.Fatalf("expected metadata")
	}
	if res.Metadata.Iterations != 2 {
		t.Errorf("expected 2 iterations, got %d", res.Metadata.Iterations)
	}
	if res.Metadata.ToolInvocations != 1 {
		t.Errorf("expected 1 tool invocation, got %d", res.Metadata.ToolInvocations)
	}
}

// 3. Tool calls routed only through the Tool Gateway
func TestBoundedLoop_ToolCallsRoutedOnlyThroughGateway(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig([]string{"calculator"}, standardDecisionSchema, domain.ExecutionModeAutonomous, 0.8)
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	step := 0
	llmClient := &MockLLMClient{
		GenerateFunc: func(ctx context.Context, request LLMRequest) (LLMResponse, error) {
			step++
			if step == 1 {
				return LLMResponse{
					Content: `{"action": "tool_call", "tool_name": "calculator", "arguments": {"expr": "2+2"}}`,
				}, nil
			}
			return LLMResponse{
				Content: `{"sentiment": "neutral", "confidence": 0.88}`,
			}, nil
		},
	}

	toolGateway := &MockToolGateway{}
	exec, err := NewLLMExecutor(llmClient, configProvider, WithToolGateway(toolGateway))
	if err != nil {
		t.Fatalf("failed to create executor: %v", err)
	}

	input := ExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		Payload:        map[string]any{"query": "calc"},
	}

	_, err = exec.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if len(toolGateway.Calls) != 1 {
		t.Fatalf("expected 1 tool call routed through gateway, got %d", len(toolGateway.Calls))
	}
	call := toolGateway.Calls[0]
	if call.ToolName != "calculator" {
		t.Errorf("expected calculator tool, got %s", call.ToolName)
	}
	if call.TenantID != tenantID || call.AgentID != agentID || call.AgentVersionID != versionID {
		t.Errorf("call context did not match trusted execution context: %+v", call)
	}
}

// 4. Unauthorized and guardrail-blocked tool calls
func TestBoundedLoop_UnauthorizedAndGuardrailBlockedToolCalls(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig([]string{"allowed_tool"}, standardDecisionSchema, domain.ExecutionModeAutonomous, 0.8)
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	t.Run("unauthorized tool not declared in agent config", func(t *testing.T) {
		llmClient := &MockLLMClient{
			Response: LLMResponse{
				Content: `{"action": "tool_call", "tool_name": "unauthorized_tool", "arguments": {}}`,
			},
		}
		toolGateway := &MockToolGateway{}
		exec, _ := NewLLMExecutor(llmClient, configProvider, WithToolGateway(toolGateway))

		input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "run"}}
		_, err := exec.Execute(context.Background(), input)
		if err == nil {
			t.Fatalf("expected error for unauthorized tool call")
		}
		if !errors.Is(err, ErrToolDenied) {
			t.Errorf("expected ErrToolDenied, got %v", err)
		}
		if len(toolGateway.Calls) != 0 {
			t.Errorf("unauthorized tool call should NOT be dispatched to tool gateway")
		}
	})

	t.Run("guardrail blocked by tool gateway", func(t *testing.T) {
		llmClient := &MockLLMClient{
			Response: LLMResponse{
				Content: `{"action": "tool_call", "tool_name": "allowed_tool", "arguments": {"action": "drop_table"}}`,
			},
		}
		toolGateway := &MockToolGateway{
			ExecuteFunc: func(ctx context.Context, req ToolExecutionRequest) (ToolExecutionResponse, error) {
				return ToolExecutionResponse{
					Allowed:    false,
					Error:      "action violates destructive action guardrail",
					DenialCode: "GUARDRAIL_BLOCKED",
				}, nil
			},
		}
		exec, _ := NewLLMExecutor(llmClient, configProvider, WithToolGateway(toolGateway))

		input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "run"}}
		_, err := exec.Execute(context.Background(), input)
		if err == nil {
			t.Fatalf("expected error for guardrail blocked tool call")
		}
		if !errors.Is(err, ErrToolDenied) {
			t.Errorf("expected ErrToolDenied, got %v", err)
		}
	})
}

// 5. Reliability Service escalation
func TestBoundedLoop_ReliabilityServiceEscalation(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig(nil, standardDecisionSchema, domain.ExecutionModeGated, 0.9)
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	llmClient := NewMockLLMClient()
	llmClient.Response = LLMResponse{
		Content: `{"sentiment": "negative", "confidence": 0.65}`,
	}

	relService := &MockReliabilityService{
		EvaluateFunc: func(ctx context.Context, req ReliabilityRequest) (ReliabilityEvaluation, error) {
			return ReliabilityEvaluation{
				Decision:   ReliabilityEscalate,
				Confidence: 0.65,
				ReasonCode: "CONFIDENCE_BELOW_THRESHOLD",
				Reason:     "decision confidence 0.65 below required 0.90",
			}, nil
		},
	}

	exec, err := NewLLMExecutor(llmClient, configProvider, WithReliabilityService(relService))
	if err != nil {
		t.Fatalf("failed to create executor: %v", err)
	}

	input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "evaluate"}}
	res, err := exec.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("execution returned unexpected error: %v", err)
	}

	if res.Status != StatusEscalated {
		t.Errorf("expected StatusEscalated, got %s", res.Status)
	}
	if res.Outcome != OutcomeTypeEscalation {
		t.Errorf("expected OutcomeTypeEscalation, got %s", res.Outcome)
	}
	if !res.IsEscalation() {
		t.Errorf("expected IsEscalation to be true")
	}
	if res.Escalation == nil || !strings.Contains(res.Escalation.Reason, "below required 0.90") {
		t.Errorf("expected escalation details, got %+v", res.Escalation)
	}
}

// 6. Reliability Service rejection
func TestBoundedLoop_ReliabilityServiceRejection(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig(nil, standardDecisionSchema, domain.ExecutionModeGated, 0.9)
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	llmClient := NewMockLLMClient()
	llmClient.Response = LLMResponse{
		Content: `{"sentiment": "positive", "confidence": 0.95}`,
	}

	relService := &MockReliabilityService{
		EvaluateFunc: func(ctx context.Context, req ReliabilityRequest) (ReliabilityEvaluation, error) {
			return ReliabilityEvaluation{
				Decision:   ReliabilityReject,
				ReasonCode: "POLICY_VIOLATION",
				Reason:     "action violates safety policy",
			}, nil
		},
	}

	exec, _ := NewLLMExecutor(llmClient, configProvider, WithReliabilityService(relService))
	input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "evaluate"}}

	_, err := exec.Execute(context.Background(), input)
	if err == nil {
		t.Fatalf("expected error on reliability rejection")
	}
	if !errors.Is(err, ErrReliabilityRejected) {
		t.Errorf("expected ErrReliabilityRejected, got %v", err)
	}
}

// 7. Reliability Service unavailability and fail-closed behavior
func TestBoundedLoop_ReliabilityServiceUnavailabilityAndFailClosed(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig(nil, standardDecisionSchema, domain.ExecutionModeGated, 0.85)
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	llmClient := NewMockLLMClient()
	llmClient.Response = LLMResponse{
		Content: `{"sentiment": "positive", "confidence": 0.95}`,
	}

	t.Run("service returns error fails closed", func(t *testing.T) {
		relService := &MockReliabilityService{
			EvaluateFunc: func(ctx context.Context, req ReliabilityRequest) (ReliabilityEvaluation, error) {
				return ReliabilityEvaluation{}, errors.New("upstream gRPC connection refused")
			},
		}
		exec, _ := NewLLMExecutor(llmClient, configProvider, WithReliabilityService(relService))
		input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "evaluate"}}

		_, err := exec.Execute(context.Background(), input)
		if err == nil {
			t.Fatalf("expected error on reliability service failure")
		}
		if !errors.Is(err, ErrReliabilityServiceUnavailable) {
			t.Errorf("expected ErrReliabilityServiceUnavailable, got %v", err)
		}
	})

	t.Run("gated execution mode without reliability service fails closed", func(t *testing.T) {
		exec, _ := NewLLMExecutor(llmClient, configProvider) // No ReliabilityService injected
		input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "evaluate"}}

		_, err := exec.Execute(context.Background(), input)
		if err == nil {
			t.Fatalf("expected error for unconfigured reliability service in gated mode")
		}
		if !errors.Is(err, ErrNilReliabilityService) {
			t.Errorf("expected ErrNilReliabilityService, got %v", err)
		}
	})
}

// 8. Invalid structured output
func TestBoundedLoop_InvalidStructuredOutput(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig(nil, standardDecisionSchema, domain.ExecutionModeAutonomous, 0.8)
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	llmClient := NewMockLLMClient()
	llmClient.Response = LLMResponse{
		Content: `{"sentiment": "positive"}`, // Missing required field "confidence"
	}

	exec, _ := NewLLMExecutor(llmClient, configProvider)
	input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "test"}}

	res, err := exec.Execute(context.Background(), input)
	if err == nil {
		t.Fatalf("expected schema violation error, got nil")
	}
	if !errors.Is(err, ErrSchemaViolation) {
		t.Errorf("expected ErrSchemaViolation, got %v", err)
	}
	if res.Status == StatusSuccess || res.Outcome == OutcomeTypeDecision {
		t.Errorf("decision must never be returned after validation failure: %+v", res)
	}
}

// 9. Maximum iterations and tool-call limits
func TestBoundedLoop_MaximumIterationsAndToolCallLimits(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig([]string{"step_tool"}, standardDecisionSchema, domain.ExecutionModeAutonomous, 0.8)
	cfg.Budgets = map[string]any{
		"max_iterations":       3,
		"max_tool_invocations": 2,
	}
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	t.Run("exceeds max iterations", func(t *testing.T) {
		step := 0
		llmClient := &MockLLMClient{
			GenerateFunc: func(ctx context.Context, request LLMRequest) (LLMResponse, error) {
				step++
				return LLMResponse{
					Content: fmt.Sprintf(`{"action": "tool_call", "tool_name": "step_tool", "arguments": {"step": %d}}`, step),
				}, nil
			},
		}
		toolGateway := &MockToolGateway{}
		customBudget := DefaultBudgetConfig()
		customBudget.MaxIterations = 2
		customBudget.MaxToolInvocations = 10
		exec, _ := NewLLMExecutor(llmClient, configProvider, WithToolGateway(toolGateway), WithBudgetConfig(customBudget))

		input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "loop"}}
		_, err := exec.Execute(context.Background(), input)
		if err == nil {
			t.Fatalf("expected budget error for iterations")
		}
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Errorf("expected ErrBudgetExceeded, got %v", err)
		}
	})

	t.Run("exceeds max tool invocations", func(t *testing.T) {
		step := 0
		llmClient := &MockLLMClient{
			GenerateFunc: func(ctx context.Context, request LLMRequest) (LLMResponse, error) {
				step++
				return LLMResponse{
					Content: fmt.Sprintf(`{"action": "tool_call", "tool_name": "step_tool", "arguments": {"step": %d}}`, step),
				}, nil
			},
		}
		toolGateway := &MockToolGateway{}
		customBudget := DefaultBudgetConfig()
		customBudget.MaxIterations = 10
		customBudget.MaxToolInvocations = 2
		exec, _ := NewLLMExecutor(llmClient, configProvider, WithToolGateway(toolGateway), WithBudgetConfig(customBudget))

		input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "loop"}}
		_, err := exec.Execute(context.Background(), input)
		if err == nil {
			t.Fatalf("expected budget error for tool invocations")
		}
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Errorf("expected ErrBudgetExceeded, got %v", err)
		}
	})
}

// 10. Token, context, and tool-result budgets
func TestBoundedLoop_TokenContextAndResultBudgets(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig([]string{"big_tool"}, standardDecisionSchema, domain.ExecutionModeAutonomous, 0.8)
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	t.Run("token budget exceeded", func(t *testing.T) {
		llmClient := &MockLLMClient{
			Response: LLMResponse{
				Content: `{"sentiment": "positive", "confidence": 0.9}`,
				Usage:   &TokenUsage{PromptTokens: 500, CompletionTokens: 500, TotalTokens: 1000},
			},
		}
		customBudget := DefaultBudgetConfig()
		customBudget.MaxTotalTokens = 500
		exec, _ := NewLLMExecutor(llmClient, configProvider, WithBudgetConfig(customBudget))

		input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "tokens"}}
		_, err := exec.Execute(context.Background(), input)
		if err == nil {
			t.Fatalf("expected budget error for tokens")
		}
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Errorf("expected ErrBudgetExceeded, got %v", err)
		}
	})

	t.Run("tool result size exceeded", func(t *testing.T) {
		llmClient := &MockLLMClient{
			Response: LLMResponse{
				Content: `{"action": "tool_call", "tool_name": "big_tool", "arguments": {}}`,
			},
		}
		toolGateway := &MockToolGateway{
			ExecuteFunc: func(ctx context.Context, req ToolExecutionRequest) (ToolExecutionResponse, error) {
				return ToolExecutionResponse{
					Allowed: true,
					Output:  strings.Repeat("X", 2000), // 2KB
				}, nil
			},
		}
		customBudget := DefaultBudgetConfig()
		customBudget.MaxToolResultBytes = 1000 // 1KB limit
		exec, _ := NewLLMExecutor(llmClient, configProvider, WithToolGateway(toolGateway), WithBudgetConfig(customBudget))

		input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "large_tool"}}
		_, err := exec.Execute(context.Background(), input)
		if err == nil {
			t.Fatalf("expected budget error for tool result size")
		}
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Errorf("expected ErrBudgetExceeded, got %v", err)
		}
	})

	t.Run("context size exceeded", func(t *testing.T) {
		llmClient := NewMockLLMClient()
		customBudget := DefaultBudgetConfig()
		customBudget.MaxContextBytes = 20 // very small limit
		exec, _ := NewLLMExecutor(llmClient, configProvider, WithBudgetConfig(customBudget))

		input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "this is longer than 20 bytes"}}
		_, err := exec.Execute(context.Background(), input)
		if err == nil {
			t.Fatalf("expected budget error for context size")
		}
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Errorf("expected ErrBudgetExceeded, got %v", err)
		}
	})
}

// 11. Repeated tool-call detection
func TestBoundedLoop_RepeatedToolCallDetection(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig([]string{"calc"}, standardDecisionSchema, domain.ExecutionModeAutonomous, 0.8)
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	t.Run("consecutive identical tool calls rejected", func(t *testing.T) {
		llmClient := &MockLLMClient{
			Response: LLMResponse{
				Content: `{"action": "tool_call", "tool_name": "calc", "arguments": {"x": 10}}`,
			},
		}
		toolGateway := &MockToolGateway{}
		exec, _ := NewLLMExecutor(llmClient, configProvider, WithToolGateway(toolGateway))

		input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "calc"}}
		_, err := exec.Execute(context.Background(), input)
		if err == nil {
			t.Fatalf("expected error for repeated tool call")
		}
		if !errors.Is(err, ErrRepeatedToolCall) {
			t.Errorf("expected ErrRepeatedToolCall, got %v", err)
		}
	})

	t.Run("distinct tool calls with different arguments allowed", func(t *testing.T) {
		step := 0
		llmClient := &MockLLMClient{
			GenerateFunc: func(ctx context.Context, request LLMRequest) (LLMResponse, error) {
				step++
				if step == 1 {
					return LLMResponse{
						Content: `{"action": "tool_call", "tool_name": "calc", "arguments": {"x": 10}}`,
					}, nil
				}
				if step == 2 {
					return LLMResponse{
						Content: `{"action": "tool_call", "tool_name": "calc", "arguments": {"x": 20}}`, // Different args!
					}, nil
				}
				return LLMResponse{
					Content: `{"sentiment": "positive", "confidence": 0.95}`,
				}, nil
			},
		}
		toolGateway := &MockToolGateway{}
		exec, _ := NewLLMExecutor(llmClient, configProvider, WithToolGateway(toolGateway))

		input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "calc"}}
		res, err := exec.Execute(context.Background(), input)
		if err != nil {
			t.Fatalf("distinct tool calls should succeed, got error: %v", err)
		}
		if res.Status != StatusSuccess {
			t.Errorf("expected success, got %s", res.Status)
		}
		if len(toolGateway.Calls) != 2 {
			t.Errorf("expected 2 distinct tool calls, got %d", len(toolGateway.Calls))
		}
	})
}

// 12. Context cancellation and deadline expiry
func TestBoundedLoop_ContextCancellationAndDeadlineExpiry(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig(nil, standardDecisionSchema, domain.ExecutionModeAutonomous, 0.8)
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	llmClient := NewMockLLMClient()
	exec, _ := NewLLMExecutor(llmClient, configProvider)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before execution

	input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "test"}}
	_, err := exec.Execute(ctx, input)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	// Deadline expired
	ctxDeadline, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancelDeadline()
	_, err = exec.Execute(ctxDeadline, input)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
}

// 13. No decision returned after validation or policy failure
func TestBoundedLoop_NoDecisionReturnedAfterValidationOrPolicyFailure(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig([]string{"tool1"}, standardDecisionSchema, domain.ExecutionModeAutonomous, 0.8)
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	// Case A: Model outputs bad JSON
	llmClient := &MockLLMClient{Response: LLMResponse{Content: "not json at all"}}
	exec, _ := NewLLMExecutor(llmClient, configProvider)

	input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "test"}}
	res, err := exec.Execute(context.Background(), input)
	if err == nil {
		t.Fatalf("expected error for non-json")
	}
	if res.IsDecision() || res.Status == StatusSuccess {
		t.Errorf("decision must never be returned on validation error")
	}

	// Case B: Tool Gateway denial
	llmClient2 := &MockLLMClient{
		Response: LLMResponse{
			Content: `{"action": "tool_call", "tool_name": "tool1", "arguments": {}}`,
		},
	}
	tgDenial := &MockToolGateway{
		ExecuteFunc: func(ctx context.Context, req ToolExecutionRequest) (ToolExecutionResponse, error) {
			return ToolExecutionResponse{Allowed: false, Error: "policy denial"}, nil
		},
	}
	exec2, _ := NewLLMExecutor(llmClient2, configProvider, WithToolGateway(tgDenial))
	res2, err2 := exec2.Execute(context.Background(), input)
	if err2 == nil {
		t.Fatalf("expected error for tool denial")
	}
	if res2.IsDecision() || res2.Status == StatusSuccess {
		t.Errorf("decision must never be returned on policy denial")
	}
}

// 14. Tenant and pinned-version identity preservation
func TestBoundedLoop_TenantAndPinnedVersionIdentityPreservation(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig([]string{"inspect_context"}, standardDecisionSchema, domain.ExecutionModeAutonomous, 0.8)
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	step := 0
	llmClient := &MockLLMClient{
		GenerateFunc: func(ctx context.Context, request LLMRequest) (LLMResponse, error) {
			step++
			if step == 1 {
				// Attacker model tries to inject spoofed tenant_id and agent_version_id into tool arguments
				return LLMResponse{
					Content: `{"action": "tool_call", "tool_name": "inspect_context", "arguments": {"tenant_id": "malicious-tenant", "agent_version_id": "malicious-version", "legit_arg": "value"}}`,
				}, nil
			}
			return LLMResponse{
				Content: `{"sentiment": "positive", "confidence": 0.9}`,
			}, nil
		},
	}

	toolGateway := &MockToolGateway{}
	exec, _ := NewLLMExecutor(llmClient, configProvider, WithToolGateway(toolGateway))

	input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "security"}}
	_, err := exec.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if len(toolGateway.Calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolGateway.Calls))
	}
	req := toolGateway.Calls[0]

	// Security context must strictly be trusted context, never spoofed
	if req.TenantID != tenantID {
		t.Errorf("expected tenantID %s, got %s", tenantID, req.TenantID)
	}
	if req.AgentVersionID != versionID {
		t.Errorf("expected versionID %s, got %s", versionID, req.AgentVersionID)
	}
	if req.AgentID != agentID {
		t.Errorf("expected agentID %s, got %s", agentID, req.AgentID)
	}

	// Model arguments must have had tenant_id and agent_version_id stripped
	if _, ok := req.Arguments["tenant_id"]; ok {
		t.Errorf("model argument tenant_id was not stripped!")
	}
	if _, ok := req.Arguments["agent_version_id"]; ok {
		t.Errorf("model argument agent_version_id was not stripped!")
	}
	if req.Arguments["legit_arg"] != "value" {
		t.Errorf("legit argument was lost")
	}
}

// 15. No silent production fallback to permissive mocks
func TestBoundedLoop_NoSilentProductionFallbackToPermissiveMocks(t *testing.T) {
	tenantID, agentID, versionID, cfg := setupAgentConfig(nil, standardDecisionSchema, domain.ExecutionModeGated, 0.9)
	configProvider := NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	llmClient := NewMockLLMClient()
	llmClient.Response = LLMResponse{Content: `{"sentiment": "positive", "confidence": 0.95}`}

	// Do NOT provide ReliabilityService
	exec, err := NewLLMExecutor(llmClient, configProvider)
	if err != nil {
		t.Fatalf("executor creation failed: %v", err)
	}

	input := ExecutionInput{TenantID: tenantID, AgentID: agentID, AgentVersionID: versionID, Payload: map[string]any{"query": "gated_action"}}
	_, err = exec.Execute(context.Background(), input)
	if err == nil {
		t.Fatalf("expected error: gated mode without reliability service must fail closed, not silently fallback")
	}
	if !errors.Is(err, ErrNilReliabilityService) {
		t.Errorf("expected ErrNilReliabilityService, got %v", err)
	}
}

// 16. Temporal retry classification (errors must be CategoryPermanent)
func TestBoundedLoop_TemporalRetryClassification(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		expected ErrorCategory
	}{
		{"ErrToolDenied", ErrToolDenied, CategoryPermanent},
		{"ErrBudgetExceeded", ErrBudgetExceeded, CategoryPermanent},
		{"ErrRepeatedToolCall", ErrRepeatedToolCall, CategoryPermanent},
		{"ErrReliabilityRejected", ErrReliabilityRejected, CategoryPermanent},
		{"ErrReliabilityServiceUnavailable", ErrReliabilityServiceUnavailable, CategoryPermanent},
		{"ErrNilReliabilityService", ErrNilReliabilityService, CategoryPermanent},
		{"ErrNilToolGateway", ErrNilToolGateway, CategoryPermanent},
		{"ErrSchemaViolation", ErrSchemaViolation, CategoryPermanent},
		{"ErrInvalidOutputJSON", ErrInvalidOutputJSON, CategoryPermanent},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			category := ClassifyError(tc.err)
			if category != tc.expected {
				t.Errorf("expected category %s for %v, got %s", tc.expected, tc.err, category)
			}
		})
	}
}
