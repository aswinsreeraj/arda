package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aswinsreeraj/arda/pkg/jsonschema"
	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
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

// WithToolGateway specifies the ToolGateway dependency for authorized tool execution.
func WithToolGateway(tg ToolGateway) LLMExecutorOption {
	return func(e *LLMExecutor) {
		e.toolGateway = tg
	}
}

// WithReliabilityService specifies the ReliabilityService dependency for evaluating actions against reliability gates.
func WithReliabilityService(rs ReliabilityService) LLMExecutorOption {
	return func(e *LLMExecutor) {
		e.reliabilityService = rs
	}
}

// WithBudgetConfig specifies custom execution budgets.
func WithBudgetConfig(b BudgetConfig) LLMExecutorOption {
	return func(e *LLMExecutor) {
		e.budgetConfig = &b
	}
}

// WithFailClosed sets whether reliability evaluation errors fail closed (default: true).
func WithFailClosed(failClosed bool) LLMExecutorOption {
	return func(e *LLMExecutor) {
		e.failClosed = failClosed
	}
}

// LLMExecutor executes an agent run by fetching its pinned version configuration,
// rendering the prompt template against the execution payload, invoking the provider-independent
// LLMClient, dispatching tools through ToolGateway, enforcing budgets, consulting the ReliabilityService,
// and packaging the outcome into a validated ExecutionResult.
type LLMExecutor struct {
	llmClient          LLMClient
	configProvider     AgentConfigProvider
	promptBuilder      *PromptBuilder
	toolGateway        ToolGateway
	reliabilityService ReliabilityService
	budgetConfig       *BudgetConfig
	failClosed         bool
	defaultModel       string
	defaultProvider    string
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
		failClosed:     true,
	}

	for _, opt := range opts {
		opt(exec)
	}

	return exec, nil
}

// Execute fulfills the Executor interface.
// It executes a deterministic bounded loop:
//  1. Loads exact pinned version configuration.
//  2. Initializes prompt, context, budgets, and repeated-call trackers.
//  3. Invokes the LLM and parses the response into a tool call, escalation, or decision.
//  4. If tool requested, verifies authorization in cfg.Tools, executes via ToolGateway, and updates context.
//  5. If escalation, returns structured escalation outcome.
//  6. If decision, validates against output schema, consults ReliabilityService (if gated/configured), and returns decision.
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

	// 1. Retrieve exact pinned agent version configuration
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

	// 2. Validate configuration integrity
	if err := domain.ValidateConfiguration(cfg); err != nil {
		return ExecutionResult{}, fmt.Errorf("%w: %v", ErrInvalidConfiguration, err)
	}

	// 3. Extract and validate execution budgets
	budget, err := ExtractBudgetConfig(cfg)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("%w: %v", ErrInvalidConfiguration, err)
	}
	if e.budgetConfig != nil {
		if err := e.budgetConfig.Validate(); err != nil {
			return ExecutionResult{}, fmt.Errorf("%w: %v", ErrInvalidConfiguration, err)
		}
		budget = *e.budgetConfig
	}

	// 4. Validate output schema early (if configured) so invalid schemas fail closed before running
	outputSchema := e.extractOutputSchema(cfg)
	var compiledSchema *jsonschema.Schema
	if outputSchema != nil {
		cs, err := jsonschema.Compile(outputSchema.Schema)
		if err != nil {
			return ExecutionResult{}, NewPermanentError("invalid agent output schema configuration", fmt.Errorf("%w: %v", ErrInvalidOutputSchema, err))
		}
		compiledSchema = cs
	}

	// 5. Build initial prompt from template and input payload
	builder := e.promptBuilder
	if builder == nil {
		builder = NewPromptBuilder()
	}
	prompt, err := builder.BuildFromConfig(cfg, input.Payload)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("failed to build prompt: %w", err)
	}

	// 6. Setup allowed tools set from pinned agent configuration
	allowedTools := make(map[string]bool, len(cfg.Tools))
	for _, t := range cfg.Tools {
		allowedTools[strings.TrimSpace(t)] = true
	}

	// 7. Initialize loop state, tracker, and context
	tracker := NewBudgetTracker(budget)
	currentPrompt := prompt
	var previousToolFingerprint string
	consecutiveRepeatedCount := 0
	toolCallCounts := make(map[string]int)
	executionID := uuid.New().String()
	reqCtx := vault.WithTenantID(ctx, input.TenantID)

	for {
		if err := ctx.Err(); err != nil {
			return ExecutionResult{}, err
		}

		if err := tracker.CheckContextSize(len(currentPrompt)); err != nil {
			return ExecutionResult{}, NewPermanentError("prompt context size budget exceeded", err)
		}

		if err := tracker.RecordIteration(); err != nil {
			return ExecutionResult{}, NewPermanentError("iteration budget exceeded", err)
		}

		req := e.buildRequest(input, cfg, currentPrompt)
		if err := req.Validate(); err != nil {
			return ExecutionResult{}, err
		}

		resp, err := e.llmClient.Generate(reqCtx, req)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return ExecutionResult{}, err
			}
			return ExecutionResult{}, fmt.Errorf("llm generation failed: %w", err)
		}

		if err := tracker.RecordUsage(resp.Usage); err != nil {
			return ExecutionResult{}, NewPermanentError("token budget exceeded", err)
		}

		if len(resp.Content) > jsonschema.MaxOutputBytes {
			return ExecutionResult{}, NewPermanentError("model output exceeds size limit", ErrOutputTooLarge)
		}

		actionType, toolCall, escalate, decisionPayload, _ := parseModelAction(resp.Content)

		switch actionType {
		case "tool_call":
			if toolCall == nil || strings.TrimSpace(toolCall.Name) == "" {
				return ExecutionResult{}, NewPermanentError("invalid tool request format", ErrInvalidInput)
			}
			toolName := strings.TrimSpace(toolCall.Name)

			// Verify tool is declared in agent's pinned configuration
			if !allowedTools[toolName] {
				return ExecutionResult{}, NewPermanentError(fmt.Sprintf("tool %q is not authorized in agent configuration", toolName), ErrToolDenied)
			}

			// Verify ToolGateway is configured
			if e.toolGateway == nil {
				return ExecutionResult{}, NewPermanentError("tool gateway is not configured for tool execution", ErrNilToolGateway)
			}

			// Enforce tool invocation count budget
			if err := tracker.RecordToolInvocation(); err != nil {
				return ExecutionResult{}, NewPermanentError("tool invocation budget exceeded", err)
			}

			// Repeated tool call detection
			fp := computeToolFingerprint(toolName, toolCall.Arguments)
			if fp == previousToolFingerprint {
				consecutiveRepeatedCount++
				if consecutiveRepeatedCount > budget.MaxConsecutiveIdenticalCalls {
					return ExecutionResult{}, NewPermanentError(fmt.Sprintf("repeated identical tool call %q detected", toolName), ErrRepeatedToolCall)
				}
			} else {
				consecutiveRepeatedCount = 1
				previousToolFingerprint = fp
			}
			toolCallCounts[fp]++
			if toolCallCounts[fp] > 5 {
				return ExecutionResult{}, NewPermanentError(fmt.Sprintf("repeated identical tool call %q exceeded total threshold across execution", toolName), ErrRepeatedToolCall)
			}

			// Sanitize arguments (prevent model arguments from overriding trusted security context)
			toolArgs := sanitizeToolArguments(toolCall.Arguments)

			toolReq := ToolExecutionRequest{
				TenantID:       input.TenantID,
				AgentID:        input.AgentID,
				AgentVersionID: input.AgentVersionID,
				ExecutionID:    executionID,
				ToolName:       toolName,
				Arguments:      toolArgs,
			}
			if err := toolReq.Validate(); err != nil {
				return ExecutionResult{}, err
			}

			// Execute tool via Tool Gateway boundary only
			toolResp, err := e.toolGateway.ExecuteTool(reqCtx, toolReq)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return ExecutionResult{}, err
				}
				return ExecutionResult{}, NewPermanentError("tool execution failed", fmt.Errorf("%w: %v", ErrToolExecutionFailed, err))
			}

			// Gateway denials are treated as hard non-retryable denials
			if !toolResp.Allowed {
				errMsg := toolResp.Error
				if errMsg == "" {
					errMsg = "tool call was blocked by gateway policy or guardrails"
				}
				return ExecutionResult{}, NewPermanentError(fmt.Sprintf("tool execution denied: %s (code: %s)", errMsg, toolResp.DenialCode), ErrToolDenied)
			}

			// Enforce tool result size budget
			resultOutput := toolResp.Output
			if resultOutput == "" && toolResp.Result != nil {
				b, _ := json.Marshal(toolResp.Result)
				resultOutput = string(b)
			}
			if err := tracker.CheckToolResultSize(len(resultOutput)); err != nil {
				return ExecutionResult{}, NewPermanentError("tool result size budget exceeded", err)
			}

			// Append sanitized tool result to model context for next iteration
			currentPrompt = fmt.Sprintf("%s\n\n[Tool Result for %s]:\n%s\n\nBased on this tool result, continue with the next step or return your final decision.", currentPrompt, toolName, resultOutput)

		case "escalate":
			reason := "human review requested by agent"
			target := ""
			if escalate != nil {
				if escalate.Reason != "" {
					reason = escalate.Reason
				}
				target = escalate.Target
			}
			usage := tracker.TokenUsage()
			result := ExecutionResult{
				Status:  StatusEscalated,
				Outcome: OutcomeTypeEscalation,
				Escalation: &EscalationInfo{
					Reason: reason,
					Target: target,
				},
				Output: map[string]any{
					"escalation_reason": reason,
					"escalation_target": target,
				},
				Metadata: &ExecutionMetadata{
					Iterations:       tracker.Iterations(),
					ToolInvocations:  tracker.ToolInvocations(),
					PromptTokens:     usage.PromptTokens,
					CompletionTokens: usage.CompletionTokens,
					TotalTokens:      usage.TotalTokens,
				},
			}
			return result, nil

		default: // Decision
			var parsedJSON any
			if compiledSchema != nil {
				toValidate := resp.Content
				if actionType == "decision_wrapped" && decisionPayload != nil {
					if b, err := json.Marshal(decisionPayload); err == nil {
						toValidate = string(b)
					}
				}

				parsed, err := compiledSchema.ValidateJSON([]byte(toValidate))
				if err != nil {
					if errors.Is(err, jsonschema.ErrOutputTooLarge) {
						return ExecutionResult{}, NewPermanentError("model output exceeds size limit", ErrOutputTooLarge)
					}
					if errors.Is(err, jsonschema.ErrInvalidJSON) {
						return ExecutionResult{}, NewPermanentError("model output is not valid JSON", ErrInvalidOutputJSON)
					}
					if errors.Is(err, jsonschema.ErrSchemaViolation) {
						return ExecutionResult{}, NewPermanentError(fmt.Sprintf("model output violates schema: %v", err), ErrSchemaViolation)
					}
					return ExecutionResult{}, NewPermanentError("internal validation failure", fmt.Errorf("%w: %v", ErrInternalValidation, err))
				}
				parsedJSON = parsed
			}

			// Check Reliability Service when execution mode is gated or ReliabilityService is provided
			relDecisionStr := ""
			if cfg.ExecutionMode == domain.ExecutionModeGated || e.reliabilityService != nil {
				if e.reliabilityService == nil {
					return ExecutionResult{}, NewPermanentError("reliability service is required for gated execution mode but not configured", ErrNilReliabilityService)
				}

				relReq := ReliabilityRequest{
					TenantID:             input.TenantID,
					AgentID:              input.AgentID,
					AgentVersionID:       input.AgentVersionID,
					ExecutionID:          executionID,
					ExecutionMode:        cfg.ExecutionMode,
					ReliabilityThreshold: cfg.ReliabilityThreshold,
					ProposedDecision:     resp.Content,
					ActionType:           "decision",
					ActionPayload:        parsedJSON,
				}
				relEval, err := e.reliabilityService.Evaluate(reqCtx, relReq)
				if err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return ExecutionResult{}, err
					}
					return ExecutionResult{}, NewPermanentError("reliability service evaluation failed", fmt.Errorf("%w: %v", ErrReliabilityServiceUnavailable, err))
				}

				if err := relEval.Validate(); err != nil {
					return ExecutionResult{}, NewPermanentError("invalid reliability evaluation result", fmt.Errorf("%w: %v", ErrInvalidResult, err))
				}

				relDecisionStr = string(relEval.Decision)

				switch relEval.Decision {
				case ReliabilityAllow:
					// Proceed with Decision
				case ReliabilityEscalate:
					usage := tracker.TokenUsage()
					reason := relEval.Reason
					if reason == "" {
						reason = fmt.Sprintf("reliability threshold not met (confidence: %.2f < %.2f)", relEval.Confidence, cfg.ReliabilityThreshold)
					}
					return ExecutionResult{
						Status:  StatusEscalated,
						Outcome: OutcomeTypeEscalation,
						Escalation: &EscalationInfo{
							Reason: reason,
							Target: "reliability_review",
						},
						Output: map[string]any{
							"escalation_reason": reason,
							"confidence":        relEval.Confidence,
							"reason_code":       relEval.ReasonCode,
						},
						Metadata: &ExecutionMetadata{
							Iterations:          tracker.Iterations(),
							ToolInvocations:     tracker.ToolInvocations(),
							PromptTokens:        usage.PromptTokens,
							CompletionTokens:    usage.CompletionTokens,
							TotalTokens:         usage.TotalTokens,
							ReliabilityDecision: relDecisionStr,
						},
					}, nil
				case ReliabilityReject:
					return ExecutionResult{}, NewPermanentError(fmt.Sprintf("reliability gate rejected decision: %s (%s)", relEval.Reason, relEval.ReasonCode), ErrReliabilityRejected)
				}
			}

			// Construct successfully validated Decision result
			usage := tracker.TokenUsage()
			result := ExecutionResult{
				Status:  StatusSuccess,
				Outcome: OutcomeTypeDecision,
				Output: map[string]any{
					"content":  resp.Content,
					"response": resp.Content,
				},
				Metadata: &ExecutionMetadata{
					Iterations:          tracker.Iterations(),
					ToolInvocations:     tracker.ToolInvocations(),
					PromptTokens:        usage.PromptTokens,
					CompletionTokens:    usage.CompletionTokens,
					TotalTokens:         usage.TotalTokens,
					ReliabilityDecision: relDecisionStr,
				},
			}
			if resp.Model != "" {
				result.Output["model"] = resp.Model
			}
			result.Output["usage"] = map[string]any{
				"prompt_tokens":     usage.PromptTokens,
				"completion_tokens": usage.CompletionTokens,
				"total_tokens":      usage.TotalTokens,
			}
			if parsedJSON != nil {
				result.StructuredOutput = parsedJSON
				result.Output["structured_output"] = parsedJSON
			}

			if err := result.Validate(); err != nil {
				return ExecutionResult{}, err
			}

			return result, nil
		}
	}
}

// Run fulfills the AgentRuntime interface, allowing LLMExecutor to be used directly as an AgentRuntime.
func (e *LLMExecutor) Run(ctx context.Context, input ExecutionInput) (ExecutionResult, error) {
	return e.Execute(ctx, input)
}

func (e *LLMExecutor) extractOutputSchema(cfg domain.Configuration) *domain.OutputSchema {
	if cfg.OutputSchema != nil && len(cfg.OutputSchema.Schema) > 0 {
		return cfg.OutputSchema
	}
	if cfg.Guardrails != nil {
		if osRaw, ok := cfg.Guardrails["output_schema"]; ok {
			switch val := osRaw.(type) {
			case *domain.OutputSchema:
				if val != nil && len(val.Schema) > 0 {
					return val
				}
			case domain.OutputSchema:
				if len(val.Schema) > 0 {
					return &val
				}
			case map[string]any:
				if schemaMap, ok := val["schema"].(map[string]any); ok && len(schemaMap) > 0 {
					name, _ := val["name"].(string)
					desc, _ := val["description"].(string)
					strict, _ := val["strict"].(bool)
					return &domain.OutputSchema{
						Name:        name,
						Description: desc,
						Strict:      strict,
						Schema:      schemaMap,
					}
				}
				if len(val) > 0 {
					return &domain.OutputSchema{
						Schema: val,
					}
				}
			}
		}
	}
	return nil
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

type toolCallAction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type escalationAction struct {
	Reason string `json:"reason"`
	Target string `json:"target"`
}

func parseModelAction(content string) (actionType string, toolCall *toolCallAction, escalate *escalationAction, decisionPayload any, err error) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "{") {
		return "decision", nil, nil, content, nil
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return "decision", nil, nil, content, nil
	}

	actionStr, _ := m["action"].(string)
	typeStr, _ := m["type"].(string)

	if strings.EqualFold(actionStr, "tool_call") || strings.EqualFold(typeStr, "tool_call") {
		name, _ := m["tool_name"].(string)
		if name == "" {
			name, _ = m["name"].(string)
		}
		args, _ := m["arguments"].(map[string]any)
		if args == nil {
			args, _ = m["args"].(map[string]any)
		}
		if args == nil {
			args, _ = m["parameters"].(map[string]any)
		}
		return "tool_call", &toolCallAction{Name: name, Arguments: args}, nil, nil, nil
	}

	if tcRaw, ok := m["tool_call"]; ok {
		if tcMap, ok := tcRaw.(map[string]any); ok {
			name, _ := tcMap["name"].(string)
			args, _ := tcMap["arguments"].(map[string]any)
			if args == nil {
				args, _ = tcMap["args"].(map[string]any)
			}
			return "tool_call", &toolCallAction{Name: name, Arguments: args}, nil, nil, nil
		}
	}

	if strings.EqualFold(actionStr, "escalate") || strings.EqualFold(typeStr, "escalate") {
		reason, _ := m["reason"].(string)
		target, _ := m["target"].(string)
		return "escalate", nil, &escalationAction{Reason: reason, Target: target}, nil, nil
	}

	if escRaw, ok := m["escalate"]; ok {
		if escMap, ok := escRaw.(map[string]any); ok {
			reason, _ := escMap["reason"].(string)
			target, _ := escMap["target"].(string)
			return "escalate", nil, &escalationAction{Reason: reason, Target: target}, nil, nil
		}
	}

	if strings.EqualFold(actionStr, "decision") || strings.EqualFold(typeStr, "decision") {
		if decPayload, ok := m["decision"]; ok {
			return "decision_wrapped", nil, nil, decPayload, nil
		}
	}

	return "decision", nil, nil, m, nil
}

func computeToolFingerprint(name string, args map[string]any) string {
	b, err := json.Marshal(args)
	if err != nil {
		return name + ":error"
	}
	return fmt.Sprintf("%s:%s", name, string(b))
}

func sanitizeToolArguments(args map[string]any) map[string]any {
	if args == nil {
		return make(map[string]any)
	}
	clean := make(map[string]any, len(args))
	for k, v := range args {
		lower := strings.ToLower(k)
		if lower == "tenant_id" || lower == "agent_id" || lower == "agent_version_id" || lower == "execution_id" {
			continue
		}
		clean[k] = v
	}
	return clean
}
