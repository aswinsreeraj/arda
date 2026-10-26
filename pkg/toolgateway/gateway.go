package toolgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/jsonschema"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
)

const (
	// MaxToolResultBytes bounds the maximum serialized output size of a tool execution (64KB).
	MaxToolResultBytes = 64 * 1024
)

// AuthorizationHook provides custom RBAC / tenant policy checks for tool dispatch.
type AuthorizationHook func(ctx context.Context, tenantID uuid.UUID, tool ToolDefinition) error

// GatewayConfig holds configuration and dependencies for the Tool Gateway.
type GatewayConfig struct {
	Registry        ToolRegistry
	Store           Store
	Guardrail       Guardrail
	HandlerRegistry *HandlerRegistry
	AuditLogger     AuditLogger
	ConfigProvider  agentruntime.AgentConfigProvider
	AuthHook        AuthorizationHook
}

// Gateway implements agentruntime.ToolGateway, governing all tool execution dispatches in ARDA.
type Gateway struct {
	registry       ToolRegistry
	store          Store
	guardrail      Guardrail
	handlers       *HandlerRegistry
	auditLogger    AuditLogger
	configProvider agentruntime.AgentConfigProvider
	authHook       AuthorizationHook
	lockManager    *IdempotencyLockManager
}

// NewGateway constructs and initializes a concrete Tool Gateway.
func NewGateway(cfg GatewayConfig) *Gateway {
	handlers := cfg.HandlerRegistry
	if handlers == nil {
		handlers = NewHandlerRegistry()
	}

	auditLogger := cfg.AuditLogger
	if auditLogger == nil && cfg.Store != nil {
		auditLogger = NewStoreAuditLogger(cfg.Store)
	}

	guardrail := cfg.Guardrail
	if guardrail == nil {
		guardrail = NewDestructiveOperationGuardrail()
	}

	return &Gateway{
		registry:       cfg.Registry,
		store:          cfg.Store,
		guardrail:      guardrail,
		handlers:       handlers,
		auditLogger:    auditLogger,
		configProvider: cfg.ConfigProvider,
		authHook:       cfg.AuthHook,
		lockManager:    NewIdempotencyLockManager(),
	}
}

// ExecuteTool evaluates governance rules and dispatches authorized tool invocations.
func (g *Gateway) ExecuteTool(ctx context.Context, req agentruntime.ToolExecutionRequest) (agentruntime.ToolExecutionResponse, error) {
	startTime := time.Now().UTC()

	// Validate request and execution context
	if err := req.Validate(); err != nil {
		g.emitAudit(ctx, AuditEvent{
			EventID:            uuid.New().String(),
			TenantID:           req.TenantID,
			ExecutionID:        req.ExecutionID,
			AgentID:            req.AgentID,
			AgentVersionID:     req.AgentVersionID,
			ToolName:           req.ToolName,
			Allowed:            false,
			DenialCode:         DenialCodeUnauthenticated,
			DenialReason:       err.Error(),
			FailureCategory:    string(agentruntime.CategoryInvalidInput),
			SanitizedArguments: req.Arguments,
			DurationMs:         time.Since(startTime).Milliseconds(),
			Timestamp:          startTime,
		})
		return agentruntime.ToolExecutionResponse{
			Allowed:    false,
			Error:      fmt.Sprintf("invalid execution context: %v", err),
			DenialCode: DenialCodeUnauthenticated,
			Category:   agentruntime.CategoryInvalidInput,
		}, nil
	}

	if req.ExecutionID == "" {
		g.emitAudit(ctx, AuditEvent{
			EventID:            uuid.New().String(),
			TenantID:           req.TenantID,
			ExecutionID:        req.ExecutionID,
			AgentID:            req.AgentID,
			AgentVersionID:     req.AgentVersionID,
			ToolName:           req.ToolName,
			Allowed:            false,
			DenialCode:         DenialCodeUnauthenticated,
			DenialReason:       "execution_id is required",
			FailureCategory:    string(agentruntime.CategoryInvalidInput),
			SanitizedArguments: req.Arguments,
			DurationMs:         time.Since(startTime).Milliseconds(),
			Timestamp:          startTime,
		})
		return agentruntime.ToolExecutionResponse{
			Allowed:    false,
			Error:      "execution_id is required",
			DenialCode: DenialCodeUnauthenticated,
			Category:   agentruntime.CategoryInvalidInput,
		}, nil
	}

	// Resolve pinned agent version if config provider is available
	var agentCfg *domain.Configuration
	if g.configProvider != nil {
		resolvedCfg, err := g.configProvider.GetAgentConfig(ctx, req.TenantID, req.AgentID, req.AgentVersionID)
		if err != nil {
			g.emitAudit(ctx, AuditEvent{
				EventID:            uuid.New().String(),
				TenantID:           req.TenantID,
				ExecutionID:        req.ExecutionID,
				AgentID:            req.AgentID,
				AgentVersionID:     req.AgentVersionID,
				ToolName:           req.ToolName,
				Allowed:            false,
				DenialCode:         DenialCodeAgentNotAllowed,
				DenialReason:       fmt.Sprintf("failed to resolve pinned agent version: %v", err),
				FailureCategory:    string(agentruntime.CategoryPermanent),
				SanitizedArguments: req.Arguments,
				DurationMs:         time.Since(startTime).Milliseconds(),
				Timestamp:          startTime,
			})
			return agentruntime.ToolExecutionResponse{
				Allowed:    false,
				Error:      fmt.Sprintf("failed to resolve pinned agent version: %v", err),
				DenialCode: DenialCodeAgentNotAllowed,
				Category:   agentruntime.CategoryPermanent,
			}, nil
		}
		agentCfg = &resolvedCfg
	}

	// Resolve tool definition from registry
	tool, err := g.registry.GetToolByName(ctx, req.TenantID, req.ToolName)
	if err != nil {
		// Attempt resolution by tool ID
		tByID, errID := g.registry.GetTool(ctx, req.TenantID, req.ToolName)
		if errID == nil {
			tool = tByID
		} else {
			g.emitAudit(ctx, AuditEvent{
				EventID:            uuid.New().String(),
				TenantID:           req.TenantID,
				ExecutionID:        req.ExecutionID,
				AgentID:            req.AgentID,
				AgentVersionID:     req.AgentVersionID,
				ToolName:           req.ToolName,
				Allowed:            false,
				DenialCode:         DenialCodeToolNotFound,
				DenialReason:       "tool definition not found in registry",
				FailureCategory:    string(agentruntime.CategoryPermanent),
				SanitizedArguments: req.Arguments,
				DurationMs:         time.Since(startTime).Milliseconds(),
				Timestamp:          startTime,
			})
			return agentruntime.ToolExecutionResponse{
				Allowed:    false,
				Error:      fmt.Sprintf("tool %q not found in registry", req.ToolName),
				DenialCode: DenialCodeToolNotFound,
				Category:   agentruntime.CategoryPermanent,
			}, nil
		}
	}

	// Verify tool is enabled and permitted
	if !tool.Enabled {
		g.emitAudit(ctx, AuditEvent{
			EventID:            uuid.New().String(),
			TenantID:           req.TenantID,
			ExecutionID:        req.ExecutionID,
			AgentID:            req.AgentID,
			AgentVersionID:     req.AgentVersionID,
			ToolID:             tool.ID,
			ToolName:           tool.Name,
			ToolVersion:        tool.Version,
			Classification:     tool.Classification,
			Allowed:            false,
			DenialCode:         DenialCodeToolDisabled,
			DenialReason:       fmt.Sprintf("tool %q is disabled", tool.Name),
			FailureCategory:    string(agentruntime.CategoryPermanent),
			SanitizedArguments: req.Arguments,
			DurationMs:         time.Since(startTime).Milliseconds(),
			Timestamp:          startTime,
		})
		return agentruntime.ToolExecutionResponse{
			Allowed:    false,
			Error:      fmt.Sprintf("tool %q is currently disabled", tool.Name),
			DenialCode: DenialCodeToolDisabled,
			Category:   agentruntime.CategoryPermanent,
		}, nil
	}

	// Check agent tools allowlist if configured
	if agentCfg != nil && len(agentCfg.Tools) > 0 {
		allowed := false
		for _, allowedTool := range agentCfg.Tools {
			if allowedTool == tool.Name || allowedTool == tool.ID {
				allowed = true
				break
			}
		}
		if !allowed {
			g.emitAudit(ctx, AuditEvent{
				EventID:            uuid.New().String(),
				TenantID:           req.TenantID,
				ExecutionID:        req.ExecutionID,
				AgentID:            req.AgentID,
				AgentVersionID:     req.AgentVersionID,
				ToolID:             tool.ID,
				ToolName:           tool.Name,
				ToolVersion:        tool.Version,
				Classification:     tool.Classification,
				Allowed:            false,
				DenialCode:         DenialCodeAgentNotAllowed,
				DenialReason:       fmt.Sprintf("tool %q is not included in agent tool allowlist", tool.Name),
				FailureCategory:    string(agentruntime.CategoryPermanent),
				SanitizedArguments: req.Arguments,
				DurationMs:         time.Since(startTime).Milliseconds(),
				Timestamp:          startTime,
			})
			return agentruntime.ToolExecutionResponse{
				Allowed:    false,
				Error:      fmt.Sprintf("tool %q is not permitted by agent configuration", tool.Name),
				DenialCode: DenialCodeAgentNotAllowed,
				Category:   agentruntime.CategoryPermanent,
			}, nil
		}
	}

	// Validate input arguments against registered schema
	schema, err := jsonschema.Compile(tool.InputSchema)
	if err != nil {
		// Schema compilation failure on registered tool should fail closed
		return g.denialResponse(ctx, req, *tool, startTime, DenialCodeSchemaValidationFailed, fmt.Sprintf("invalid registered schema for tool: %v", err))
	}

	argsToValidate := req.Arguments
	if argsToValidate == nil {
		argsToValidate = make(map[string]any)
	}
	if err := schema.Validate(argsToValidate); err != nil {
		return g.denialResponse(ctx, req, *tool, startTime, DenialCodeSchemaValidationFailed, fmt.Sprintf("arguments violate tool schema: %v", err))
	}

	// Tenant boundary and authorization checks
	if tool.TenantID != req.TenantID {
		return g.denialResponse(ctx, req, *tool, startTime, DenialCodeUnauthorized, "cross-tenant tool execution forbidden")
	}

	if g.authHook != nil {
		if err := g.authHook(ctx, req.TenantID, *tool); err != nil {
			return g.denialResponse(ctx, req, *tool, startTime, DenialCodeUnauthorized, fmt.Sprintf("authorization denied: %v", err))
		}
	}

	// Business guardrails
	if g.guardrail != nil {
		guardCtx := GuardrailContext{
			TenantID:       req.TenantID,
			AgentID:        req.AgentID,
			AgentVersionID: req.AgentVersionID,
			ExecutionID:    req.ExecutionID,
			Tool:           *tool,
			Arguments:      req.Arguments,
		}
		if err := g.guardrail.Evaluate(ctx, guardCtx); err != nil {
			denialCode := DenialCodeGuardrailDenied
			var gErr *GuardrailDenialError
			if errors.As(err, &gErr) && gErr.DenialCode != "" {
				denialCode = gErr.DenialCode
			}
			return g.denialResponse(ctx, req, *tool, startTime, denialCode, err.Error())
		}
	}

	// Idempotency check and concurrency lock
	invID, err := DeriveInvocationID(req.ExecutionID, tool.Name, req.Arguments)
	if err != nil {
		return g.denialResponse(ctx, req, *tool, startTime, DenialCodeIdempotencyConflict, fmt.Sprintf("failed to derive invocation ID: %v", err))
	}

	argsHash, err := HashArguments(req.Arguments)
	if err != nil {
		return g.denialResponse(ctx, req, *tool, startTime, DenialCodeIdempotencyConflict, fmt.Sprintf("failed to hash arguments: %v", err))
	}

	// Lock invocation to prevent concurrent duplicate dispatch
	unlock := g.lockManager.Acquire(invID)
	defer unlock()

	// Check existing invocation state
	existingInv, err := g.store.GetInvocation(ctx, req.TenantID, invID)
	if err == nil && existingInv != nil {
		// Idempotency check: argument hash must match exactly
		if existingInv.ArgumentsHash != argsHash {
			return g.denialResponse(ctx, req, *tool, startTime, DenialCodeIdempotencyConflict, ErrIdempotencyKeyMismatch.Error())
		}

		// Completed: return cached result without re-executing backend side effects
		if existingInv.Status == StatusCompleted {
			duration := time.Since(startTime).Milliseconds()
			g.emitAudit(ctx, AuditEvent{
				EventID:            uuid.New().String(),
				TenantID:           req.TenantID,
				ExecutionID:        req.ExecutionID,
				AgentID:            req.AgentID,
				AgentVersionID:     req.AgentVersionID,
				ToolID:             tool.ID,
				ToolName:           tool.Name,
				ToolVersion:        tool.Version,
				InvocationID:       invID,
				Classification:     tool.Classification,
				Allowed:            true,
				SanitizedArguments: req.Arguments,
				DurationMs:         duration,
				Timestamp:          startTime,
			})
			return agentruntime.ToolExecutionResponse{
				Allowed: true,
				Result:  existingInv.Result,
				Output:  existingInv.Output,
			}, nil
		}

		// In-progress: concurrent duplicate request in flight
		if existingInv.Status == StatusPending {
			return g.denialResponse(ctx, req, *tool, startTime, DenialCodeInvocationInProgress, ErrInvocationInProgress.Error())
		}
	}

	// Record initial pending state
	inv := &ToolInvocation{
		InvocationID:   invID,
		TenantID:       req.TenantID,
		AgentID:        req.AgentID,
		AgentVersionID: req.AgentVersionID,
		ExecutionID:    req.ExecutionID,
		ToolID:         tool.ID,
		ToolName:       tool.Name,
		ArgumentsHash:  argsHash,
		Status:         StatusPending,
		CreatedAt:      time.Now().UTC(),
	}
	_ = g.store.SaveInvocation(ctx, inv)

	// Dispatch to tool handler
	handler, ok := g.handlers.Get(tool.HandlerID)
	if !ok {
		inv.Status = StatusFailed
		inv.Error = ErrHandlerNotFound.Error()
		inv.DenialCode = DenialCodeExecutionFailed
		_ = g.store.UpdateInvocation(ctx, inv)
		return g.denialResponse(ctx, req, *tool, startTime, DenialCodeExecutionFailed, fmt.Sprintf("handler %q not found", tool.HandlerID))
	}

	handlerCtx := HandlerExecutionContext{
		TenantID:       req.TenantID,
		AgentID:        req.AgentID,
		AgentVersionID: req.AgentVersionID,
		ExecutionID:    req.ExecutionID,
		ToolID:         tool.ID,
		ToolName:       tool.Name,
		InvocationID:   invID,
	}

	res, execErr := handler.Execute(ctx, handlerCtx, req.Arguments)

	// Propagate context cancellation immediately
	if ctx.Err() != nil {
		inv.Status = StatusFailed
		inv.Error = ctx.Err().Error()
		_ = g.store.UpdateInvocation(ctx, inv)
		return agentruntime.ToolExecutionResponse{}, ctx.Err()
	}

	if execErr != nil {
		inv.Status = StatusFailed
		inv.Error = execErr.Error()
		inv.DenialCode = DenialCodeExecutionFailed
		_ = g.store.UpdateInvocation(ctx, inv)
		return g.denialResponse(ctx, req, *tool, startTime, DenialCodeExecutionFailed, fmt.Sprintf("tool execution failed: %v", execErr))
	}

	// Validate output payload size and serialize
	outputBytes, err := json.Marshal(res)
	if err != nil {
		inv.Status = StatusFailed
		inv.Error = fmt.Sprintf("failed to serialize tool output: %v", err)
		_ = g.store.UpdateInvocation(ctx, inv)
		return g.denialResponse(ctx, req, *tool, startTime, DenialCodeExecutionFailed, inv.Error)
	}

	if len(outputBytes) > MaxToolResultBytes {
		inv.Status = StatusFailed
		inv.Error = ErrOversizedResult.Error()
		inv.DenialCode = DenialCodeResultTooLarge
		_ = g.store.UpdateInvocation(ctx, inv)
		return g.denialResponse(ctx, req, *tool, startTime, DenialCodeResultTooLarge, "tool result exceeds 64KB limit")
	}

	duration := time.Since(startTime).Milliseconds()
	outputStr := string(outputBytes)

	inv.Status = StatusCompleted
	inv.Result = res
	inv.Output = outputStr
	inv.DurationMs = duration
	_ = g.store.UpdateInvocation(ctx, inv)

	g.emitAudit(ctx, AuditEvent{
		EventID:            uuid.New().String(),
		TenantID:           req.TenantID,
		ExecutionID:        req.ExecutionID,
		AgentID:            req.AgentID,
		AgentVersionID:     req.AgentVersionID,
		ToolID:             tool.ID,
		ToolName:           tool.Name,
		ToolVersion:        tool.Version,
		InvocationID:       invID,
		Classification:     tool.Classification,
		Allowed:            true,
		SanitizedArguments: req.Arguments,
		DurationMs:         duration,
		Timestamp:          startTime,
	})

	return agentruntime.ToolExecutionResponse{
		Allowed: true,
		Result:  res,
		Output:  outputStr,
	}, nil
}

func (g *Gateway) denialResponse(ctx context.Context, req agentruntime.ToolExecutionRequest, tool ToolDefinition, startTime time.Time, code, reason string) (agentruntime.ToolExecutionResponse, error) {
	duration := time.Since(startTime).Milliseconds()
	g.emitAudit(ctx, AuditEvent{
		EventID:            uuid.New().String(),
		TenantID:           req.TenantID,
		ExecutionID:        req.ExecutionID,
		AgentID:            req.AgentID,
		AgentVersionID:     req.AgentVersionID,
		ToolID:             tool.ID,
		ToolName:           tool.Name,
		ToolVersion:        tool.Version,
		Classification:     tool.Classification,
		Allowed:            false,
		DenialCode:         code,
		DenialReason:       reason,
		FailureCategory:    string(agentruntime.CategoryPermanent),
		SanitizedArguments: req.Arguments,
		DurationMs:         duration,
		Timestamp:          startTime,
	})

	return agentruntime.ToolExecutionResponse{
		Allowed:    false,
		Error:      reason,
		DenialCode: code,
		Category:   agentruntime.CategoryPermanent,
	}, nil
}

func (g *Gateway) emitAudit(ctx context.Context, event AuditEvent) {
	if g.auditLogger != nil {
		_ = g.auditLogger.Log(ctx, event)
	}
}
