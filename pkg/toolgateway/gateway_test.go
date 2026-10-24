package toolgateway_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/toolgateway"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockConfigProvider implements agentruntime.AgentConfigProvider for tests.
type mockConfigProvider struct {
	configs map[string]domain.Configuration
	err     error
}

func (m *mockConfigProvider) GetAgentConfig(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (domain.Configuration, error) {
	if m.err != nil {
		return domain.Configuration{}, m.err
	}
	key := fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)
	cfg, ok := m.configs[key]
	if !ok {
		return domain.Configuration{}, errors.New("agent config not found")
	}
	return cfg, nil
}

func setupTestGateway(t *testing.T) (*toolgateway.Gateway, *toolgateway.DefaultRegistry, *toolgateway.MemoryStore, *toolgateway.HandlerRegistry, *mockConfigProvider) {
	store := toolgateway.NewMemoryStore()
	registry := toolgateway.NewRegistry(store)
	handlers := toolgateway.NewHandlerRegistry()
	cfgProvider := &mockConfigProvider{
		configs: make(map[string]domain.Configuration),
	}

	gw := toolgateway.NewGateway(toolgateway.GatewayConfig{
		Registry: registry,
		Store:    store,
		Guardrail: toolgateway.NewCompositeGuardrail(
			toolgateway.NewDestructiveOperationGuardrail(),
			toolgateway.NewParameterBoundsGuardrail(1000.0), // max amount 1000
		),
		HandlerRegistry: handlers,
		AuditLogger:     toolgateway.NewStoreAuditLogger(store),
		ConfigProvider:  cfgProvider,
	})

	return gw, registry, store, handlers, cfgProvider
}

// 1. Tool registration and invalid definitions
func TestToolRegistration_Validation(t *testing.T) {
	ctx := context.Background()
	_, registry, _, _, _ := setupTestGateway(t)
	tenantID := uuid.New()

	// Valid tool registration
	validTool := toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "get_weather",
		Description:    "Get current weather",
		Classification: toolgateway.ClassificationRead,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
			"required": []any{"city"},
		},
		HandlerID: "builtin:echo",
	}
	registered, err := registry.RegisterTool(ctx, validTool)
	require.NoError(t, err)
	assert.NotEmpty(t, registered.ID)
	assert.Equal(t, 1, registered.Version)
	assert.True(t, registered.Enabled)

	// Invalid definition: missing tenant ID
	invalidTenant := validTool
	invalidTenant.TenantID = uuid.Nil
	_, err = registry.RegisterTool(ctx, invalidTenant)
	assert.ErrorIs(t, err, toolgateway.ErrInvalidToolDefinition)

	// Invalid definition: missing name
	invalidName := validTool
	invalidName.Name = ""
	_, err = registry.RegisterTool(ctx, invalidName)
	assert.ErrorIs(t, err, toolgateway.ErrInvalidToolDefinition)

	// Invalid definition: invalid classification
	invalidClass := validTool
	invalidClass.Classification = "unknown"
	_, err = registry.RegisterTool(ctx, invalidClass)
	assert.ErrorIs(t, err, toolgateway.ErrInvalidToolDefinition)

	// Invalid definition: malformed JSON Schema (unsupported type)
	invalidSchema := validTool
	invalidSchema.Name = "bad_schema_tool"
	invalidSchema.InputSchema = map[string]any{
		"type": "unsupported_data_type",
	}
	_, err = registry.RegisterTool(ctx, invalidSchema)
	assert.ErrorIs(t, err, toolgateway.ErrInvalidToolDefinition)

	// Invalid definition: empty required permission string
	invalidPerm := validTool
	invalidPerm.Name = "bad_perm_tool"
	invalidPerm.RequiredPermissions = []string{"   "}
	_, err = registry.RegisterTool(ctx, invalidPerm)
	assert.ErrorIs(t, err, toolgateway.ErrInvalidToolDefinition)
}

// 2. Tool schema validation against arguments
func TestToolSchemaValidation(t *testing.T) {
	ctx := context.Background()
	gw, registry, _, _, cfgProvider := setupTestGateway(t)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()
	execID := "exec-schema-1"

	// Register a tool requiring "city" (string) and "days" (integer, min 1, max 7)
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{"type": "string", "minLength": 2},
			"days": map[string]any{"type": "integer", "minimum": 1, "maximum": 7},
		},
		"required": []any{"city", "days"},
	}
	tool, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "forecast",
		Classification: toolgateway.ClassificationRead,
		InputSchema:    schema,
		HandlerID:      "builtin:echo",
	})
	require.NoError(t, err)

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{tool.Name},
	}

	// Valid arguments
	resp, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    execID,
		ToolName:       tool.Name,
		Arguments: map[string]any{
			"city": "London",
			"days": 3,
		},
	})
	require.NoError(t, err)
	assert.True(t, resp.Allowed)

	// Invalid arguments: missing required "days"
	respInvalid, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    execID + "-bad1",
		ToolName:       tool.Name,
		Arguments: map[string]any{
			"city": "London",
		},
	})
	require.NoError(t, err)
	assert.False(t, respInvalid.Allowed)
	assert.Equal(t, toolgateway.DenialCodeSchemaValidationFailed, respInvalid.DenialCode)

	// Invalid arguments: days out of range (8 > max 7)
	respOutOfRange, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    execID + "-bad2",
		ToolName:       tool.Name,
		Arguments: map[string]any{
			"city": "London",
			"days": 10,
		},
	})
	require.NoError(t, err)
	assert.False(t, respOutOfRange.Allowed)
	assert.Equal(t, toolgateway.DenialCodeSchemaValidationFailed, respOutOfRange.DenialCode)
}

// 3. Disabled and unknown tools
func TestDisabledAndUnknownTools(t *testing.T) {
	ctx := context.Background()
	gw, registry, _, _, cfgProvider := setupTestGateway(t)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{}

	// Unknown tool
	respUnknown, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-unknown",
		ToolName:       "non_existent_tool",
		Arguments:      map[string]any{},
	})
	require.NoError(t, err)
	assert.False(t, respUnknown.Allowed)
	assert.Equal(t, toolgateway.DenialCodeToolNotFound, respUnknown.DenialCode)

	// Register tool and disable it
	tool, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "disabled_tool",
		Classification: toolgateway.ClassificationRead,
		InputSchema:    map[string]any{"type": "object"},
		HandlerID:      "builtin:echo",
	})
	require.NoError(t, err)

	_, err = registry.SetToolEnabled(ctx, tenantID, tool.ID, false)
	require.NoError(t, err)

	respDisabled, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-disabled",
		ToolName:       tool.Name,
		Arguments:      map[string]any{},
	})
	require.NoError(t, err)
	assert.False(t, respDisabled.Allowed)
	assert.Equal(t, toolgateway.DenialCodeToolDisabled, respDisabled.DenialCode)
}

// 4. Tenant isolation and RBAC denial
func TestTenantIsolationAndRBAC(t *testing.T) {
	ctx := context.Background()
	tenantA := uuid.New()
	tenantB := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	store := toolgateway.NewMemoryStore()
	registry := toolgateway.NewRegistry(store)

	// Register tool in Tenant A
	toolA, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantA,
		Name:           "tenant_a_tool",
		Classification: toolgateway.ClassificationRead,
		InputSchema:    map[string]any{"type": "object"},
		HandlerID:      "builtin:echo",
	})
	require.NoError(t, err)

	gw := toolgateway.NewGateway(toolgateway.GatewayConfig{
		Registry: registry,
		Store:    store,
		AuthHook: func(ctx context.Context, tenantID uuid.UUID, tool toolgateway.ToolDefinition) error {
			if tool.Name == "restricted_tool" {
				return errors.New("insufficient permissions for restricted tool")
			}
			return nil
		},
	})

	// Tenant B attempts to invoke Tenant A's tool by name
	respCrossTenant, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantB,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-cross",
		ToolName:       toolA.Name,
		Arguments:      map[string]any{},
	})
	require.NoError(t, err)
	assert.False(t, respCrossTenant.Allowed)
	assert.Equal(t, toolgateway.DenialCodeToolNotFound, respCrossTenant.DenialCode)

	// Register restricted tool in Tenant A and trigger AuthHook denial
	_, err = registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantA,
		Name:           "restricted_tool",
		Classification: toolgateway.ClassificationRead,
		InputSchema:    map[string]any{"type": "object"},
		HandlerID:      "builtin:echo",
	})
	require.NoError(t, err)

	respAuthDenied, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantA,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-rbac",
		ToolName:       "restricted_tool",
		Arguments:      map[string]any{},
	})
	require.NoError(t, err)
	assert.False(t, respAuthDenied.Allowed)
	assert.Equal(t, toolgateway.DenialCodeUnauthorized, respAuthDenied.DenialCode)
}

// 5. Agent allowlist enforcement (cfg.Tools)
func TestAgentAllowlistEnforcement(t *testing.T) {
	ctx := context.Background()
	gw, registry, _, _, cfgProvider := setupTestGateway(t)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	// Register two tools
	tool1, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "allowed_tool",
		Classification: toolgateway.ClassificationRead,
		InputSchema:    map[string]any{"type": "object"},
		HandlerID:      "builtin:echo",
	})
	require.NoError(t, err)

	tool2, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "disallowed_tool",
		Classification: toolgateway.ClassificationRead,
		InputSchema:    map[string]any{"type": "object"},
		HandlerID:      "builtin:echo",
	})
	require.NoError(t, err)

	// Agent only has "allowed_tool" in its Tools slice
	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{tool1.Name},
	}

	// Tool 1 succeeds
	resp1, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-allow-1",
		ToolName:       tool1.Name,
		Arguments:      map[string]any{},
	})
	require.NoError(t, err)
	assert.True(t, resp1.Allowed)

	// Tool 2 blocked by agent allowlist
	resp2, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-allow-2",
		ToolName:       tool2.Name,
		Arguments:      map[string]any{},
	})
	require.NoError(t, err)
	assert.False(t, resp2.Allowed)
	assert.Equal(t, toolgateway.DenialCodeAgentNotAllowed, resp2.DenialCode)
}

// 6. Hard guardrail denial before dispatch
func TestHardGuardrailDenial(t *testing.T) {
	ctx := context.Background()
	gw, registry, _, handlers, cfgProvider := setupTestGateway(t)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	handlerInvoked := false
	handlers.Register("custom:spy", toolgateway.CustomToolHandlerFunc(func(ctx context.Context, execCtx toolgateway.HandlerExecutionContext, args map[string]any) (map[string]any, error) {
		handlerInvoked = true
		return map[string]any{"status": "executed"}, nil
	}))

	tool, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "database_query",
		Classification: toolgateway.ClassificationWrite,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":  map[string]any{"type": "string"},
				"amount": map[string]any{"type": "number"},
			},
		},
		HandlerID: "custom:spy",
	})
	require.NoError(t, err)

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{tool.Name},
	}

	// Guardrail 1: Destructive operation keyword check ("DROP TABLE")
	respDestructive, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-guard-1",
		ToolName:       tool.Name,
		Arguments: map[string]any{
			"query": "DROP TABLE users;",
		},
	})
	require.NoError(t, err)
	assert.False(t, respDestructive.Allowed)
	assert.Equal(t, toolgateway.DenialCodeGuardrailDenied, respDestructive.DenialCode)
	assert.False(t, handlerInvoked, "handler must NEVER be invoked when guardrail blocks")

	// Guardrail 2: Parameter bounds check (amount 1500 > threshold 1000)
	respBounds, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-guard-2",
		ToolName:       tool.Name,
		Arguments: map[string]any{
			"amount": 1500.0,
		},
	})
	require.NoError(t, err)
	assert.False(t, respBounds.Allowed)
	assert.Equal(t, toolgateway.DenialCodeGuardrailDenied, respBounds.DenialCode)
	assert.False(t, handlerInvoked, "handler must NEVER be invoked when guardrail blocks")
}

// 7. Audit events for success and blocked calls
func TestAuditEvents_SuccessAndBlocked(t *testing.T) {
	ctx := context.Background()
	gw, registry, store, _, cfgProvider := setupTestGateway(t)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	tool, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "audit_tool",
		Classification: toolgateway.ClassificationRead,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
			"required": []any{"city"},
		},
		HandlerID: "builtin:echo",
	})
	require.NoError(t, err)

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{tool.Name},
	}

	// Successful invocation
	_, err = gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-audit-success",
		ToolName:       tool.Name,
		Arguments:      map[string]any{"city": "Paris"},
	})
	require.NoError(t, err)

	// Blocked invocation (schema violation)
	_, err = gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-audit-blocked",
		ToolName:       tool.Name,
		Arguments:      map[string]any{}, // missing city
	})
	require.NoError(t, err)

	// Verify audit events recorded in store
	events, err := store.ListAuditEvents(ctx, tenantID, 10)
	require.NoError(t, err)
	require.Len(t, events, 2)

	// Check blocked event
	blockedEvent := events[0]
	assert.False(t, blockedEvent.Allowed)
	assert.Equal(t, toolgateway.DenialCodeSchemaValidationFailed, blockedEvent.DenialCode)
	assert.Equal(t, "exec-audit-blocked", blockedEvent.ExecutionID)

	// Check success event
	successEvent := events[1]
	assert.True(t, successEvent.Allowed)
	assert.Equal(t, "exec-audit-success", successEvent.ExecutionID)
	assert.Equal(t, tool.ID, successEvent.ToolID)
}

// 8. Repeated invocation idempotency
func TestRepeatedInvocationIdempotency(t *testing.T) {
	ctx := context.Background()
	gw, registry, _, handlers, cfgProvider := setupTestGateway(t)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()
	execID := "exec-idempotency-1"

	var callCount int32
	handlers.Register("custom:counter", toolgateway.CustomToolHandlerFunc(func(ctx context.Context, execCtx toolgateway.HandlerExecutionContext, args map[string]any) (map[string]any, error) {
		count := atomic.AddInt32(&callCount, 1)
		return map[string]any{"count": count}, nil
	}))

	tool, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "counter_tool",
		Classification: toolgateway.ClassificationWrite,
		InputSchema:    map[string]any{"type": "object"},
		HandlerID:      "custom:counter",
	})
	require.NoError(t, err)

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{tool.Name},
	}

	req := agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    execID,
		ToolName:       tool.Name,
		Arguments:      map[string]any{"action": "increment"},
	}

	// First execution
	resp1, err := gw.ExecuteTool(ctx, req)
	require.NoError(t, err)
	assert.True(t, resp1.Allowed)
	assert.Equal(t, int32(1), atomic.LoadInt32(&callCount))

	// Second execution with identical execution context and arguments
	resp2, err := gw.ExecuteTool(ctx, req)
	require.NoError(t, err)
	assert.True(t, resp2.Allowed)
	// Must return cached response without incrementing backend counter!
	assert.Equal(t, int32(1), atomic.LoadInt32(&callCount))
	assert.Equal(t, resp1.Output, resp2.Output)
}

// 9. Concurrent duplicate requests
func TestConcurrentDuplicateRequests(t *testing.T) {
	ctx := context.Background()
	gw, registry, _, handlers, cfgProvider := setupTestGateway(t)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()
	execID := "exec-concurrent-1"

	var executionRuns int32
	handlers.Register("custom:slow_counter", toolgateway.CustomToolHandlerFunc(func(ctx context.Context, execCtx toolgateway.HandlerExecutionContext, args map[string]any) (map[string]any, error) {
		time.Sleep(20 * time.Millisecond)
		runs := atomic.AddInt32(&executionRuns, 1)
		return map[string]any{"runs": runs}, nil
	}))

	tool, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "slow_counter",
		Classification: toolgateway.ClassificationWrite,
		InputSchema:    map[string]any{"type": "object"},
		HandlerID:      "custom:slow_counter",
	})
	require.NoError(t, err)

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{tool.Name},
	}

	req := agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    execID,
		ToolName:       tool.Name,
		Arguments:      map[string]any{"key": "value"},
	}

	const goroutines = 5
	var wg sync.WaitGroup
	wg.Add(goroutines)

	responses := make([]agentruntime.ToolExecutionResponse, goroutines)
	for i := 0; i < goroutines; i++ {
		idx := i
		go func() {
			defer wg.Done()
			resp, _ := gw.ExecuteTool(ctx, req)
			responses[idx] = resp
		}()
	}
	wg.Wait()

	// Handler must be executed at most once
	assert.Equal(t, int32(1), atomic.LoadInt32(&executionRuns))

	// All callers receive a response (either the cached success or in-progress)
	successCount := 0
	for _, r := range responses {
		if r.Allowed {
			successCount++
		}
	}
	assert.True(t, successCount >= 1)
}

// 10. Same idempotency key with different arguments
func TestSameIdempotencyKey_DifferentArguments(t *testing.T) {
	ctx := context.Background()
	gw, registry, _, _, cfgProvider := setupTestGateway(t)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()
	execID := "exec-key-mismatch"

	tool, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "keyed_tool",
		Classification: toolgateway.ClassificationWrite,
		InputSchema:    map[string]any{"type": "object"},
		HandlerID:      "builtin:echo",
	})
	require.NoError(t, err)

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{tool.Name},
	}

	// First call with explicit idempotency key
	resp1, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    execID,
		ToolName:       tool.Name,
		Arguments: map[string]any{
			"_idempotency_key": "fixed-key-123",
			"amount":           100.0,
		},
	})
	require.NoError(t, err)
	assert.True(t, resp1.Allowed)

	// Second call with same idempotency key but DIFFERENT arguments (amount 200 instead of 100)
	resp2, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    execID,
		ToolName:       tool.Name,
		Arguments: map[string]any{
			"_idempotency_key": "fixed-key-123",
			"amount":           200.0,
		},
	})
	require.NoError(t, err)
	assert.False(t, resp2.Allowed)
	assert.Equal(t, toolgateway.DenialCodeIdempotencyConflict, resp2.DenialCode)
	assert.Contains(t, resp2.Error, "idempotency key reused with different arguments")
}

// 11. In-progress invocation behavior
func TestInProgressInvocation(t *testing.T) {
	ctx := context.Background()
	gw, registry, store, _, cfgProvider := setupTestGateway(t)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()
	execID := "exec-in-progress"

	tool, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "in_prog_tool",
		Classification: toolgateway.ClassificationWrite,
		InputSchema:    map[string]any{"type": "object"},
		HandlerID:      "builtin:echo",
	})
	require.NoError(t, err)

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{tool.Name},
	}

	args := map[string]any{"action": "test"}
	invID, err := toolgateway.DeriveInvocationID(execID, tool.Name, args)
	require.NoError(t, err)

	hash, err := toolgateway.HashArguments(args)
	require.NoError(t, err)

	// Manually insert an in-progress invocation
	err = store.SaveInvocation(ctx, &toolgateway.ToolInvocation{
		InvocationID:  invID,
		TenantID:      tenantID,
		ToolID:        tool.ID,
		ToolName:      tool.Name,
		ArgumentsHash: hash,
		Status:        toolgateway.StatusPending,
	})
	require.NoError(t, err)

	resp, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    execID,
		ToolName:       tool.Name,
		Arguments:      args,
	})
	require.NoError(t, err)
	assert.False(t, resp.Allowed)
	assert.Equal(t, toolgateway.DenialCodeInvocationInProgress, resp.DenialCode)
}

// 12. Backend failure and recovery behavior
func TestBackendFailureAndRecovery(t *testing.T) {
	ctx := context.Background()
	gw, registry, store, handlers, cfgProvider := setupTestGateway(t)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()
	execID := "exec-failure-1"

	handlers.Register("custom:failing", toolgateway.CustomToolHandlerFunc(func(ctx context.Context, execCtx toolgateway.HandlerExecutionContext, args map[string]any) (map[string]any, error) {
		return nil, errors.New("upstream third-party service unavailable")
	}))

	tool, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "failing_tool",
		Classification: toolgateway.ClassificationWrite,
		InputSchema:    map[string]any{"type": "object"},
		HandlerID:      "custom:failing",
	})
	require.NoError(t, err)

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{tool.Name},
	}

	resp, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    execID,
		ToolName:       tool.Name,
		Arguments:      map[string]any{},
	})
	require.NoError(t, err)
	assert.False(t, resp.Allowed)
	assert.Equal(t, toolgateway.DenialCodeExecutionFailed, resp.DenialCode)

	// Verify invocation recorded as failed
	invID, err := toolgateway.DeriveInvocationID(execID, tool.Name, map[string]any{})
	require.NoError(t, err)
	inv, err := store.GetInvocation(ctx, tenantID, invID)
	require.NoError(t, err)
	assert.Equal(t, toolgateway.StatusFailed, inv.Status)
	assert.Contains(t, inv.Error, "upstream third-party service unavailable")
}

// 13. Context cancellation handling
func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	gw, registry, _, handlers, cfgProvider := setupTestGateway(t)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	handlers.Register("custom:blocking", toolgateway.CustomToolHandlerFunc(func(handlerCtx context.Context, execCtx toolgateway.HandlerExecutionContext, args map[string]any) (map[string]any, error) {
		cancel() // Cancel the parent context
		<-handlerCtx.Done()
		return nil, handlerCtx.Err()
	}))

	tool, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "blocking_tool",
		Classification: toolgateway.ClassificationRead,
		InputSchema:    map[string]any{"type": "object"},
		HandlerID:      "custom:blocking",
	})
	require.NoError(t, err)

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{tool.Name},
	}

	_, err = gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-cancel",
		ToolName:       tool.Name,
		Arguments:      map[string]any{},
	})
	assert.ErrorIs(t, err, context.Canceled)
}

// 14. Oversized tool results
func TestOversizedToolResult(t *testing.T) {
	ctx := context.Background()
	gw, registry, _, handlers, cfgProvider := setupTestGateway(t)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	// Handler that returns a payload exceeding 64KB
	handlers.Register("custom:huge", toolgateway.CustomToolHandlerFunc(func(ctx context.Context, execCtx toolgateway.HandlerExecutionContext, args map[string]any) (map[string]any, error) {
		hugePayload := strings.Repeat("A", 70*1024)
		return map[string]any{"huge": hugePayload}, nil
	}))

	tool, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "huge_tool",
		Classification: toolgateway.ClassificationRead,
		InputSchema:    map[string]any{"type": "object"},
		HandlerID:      "custom:huge",
	})
	require.NoError(t, err)

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{tool.Name},
	}

	resp, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-huge",
		ToolName:       tool.Name,
		Arguments:      map[string]any{},
	})
	require.NoError(t, err)
	assert.False(t, resp.Allowed)
	assert.Equal(t, toolgateway.DenialCodeResultTooLarge, resp.DenialCode)
}

// 15. Secret and sensitive-argument redaction in audit logs
func TestSecretRedactionInAudit(t *testing.T) {
	ctx := context.Background()
	gw, registry, store, _, cfgProvider := setupTestGateway(t)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()
	execID := "exec-secret-redaction"

	tool, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "api_caller",
		Classification: toolgateway.ClassificationRead,
		InputSchema:    map[string]any{"type": "object"},
		HandlerID:      "builtin:echo",
	})
	require.NoError(t, err)

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{tool.Name},
	}

	// Tool call with sensitive keys in arguments
	_, err = gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    execID,
		ToolName:       tool.Name,
		Arguments: map[string]any{
			"normal_arg": "normal_value",
			"api_key":    "sk-secret-123456",
			"password":   "super_secret_pw",
			"nested": map[string]any{
				"auth_token": "bearer-xyz-secret",
			},
		},
	})
	require.NoError(t, err)

	events, err := store.ListAuditEvents(ctx, tenantID, 1)
	require.NoError(t, err)
	require.Len(t, events, 1)

	loggedArgs := events[0].SanitizedArguments
	assert.Equal(t, "normal_value", loggedArgs["normal_arg"])
	assert.Equal(t, "[REDACTED]", loggedArgs["api_key"])
	assert.Equal(t, "[REDACTED]", loggedArgs["password"])

	nestedMap, ok := loggedArgs["nested"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "[REDACTED]", nestedMap["auth_token"])
}

// 16. Proof that runtime cannot bypass the gateway
func TestRuntimeCannotBypassGateway(t *testing.T) {
	// The LLM Executor only receives the agentruntime.ToolGateway interface.
	// We demonstrate that when a tool is blocked by the gateway, the runtime
	// strictly respects the denial and never reaches the backend handler.
	ctx := context.Background()
	gw, registry, _, handlers, cfgProvider := setupTestGateway(t)
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	backendCalled := false
	handlers.Register("custom:unreachable", toolgateway.CustomToolHandlerFunc(func(ctx context.Context, execCtx toolgateway.HandlerExecutionContext, args map[string]any) (map[string]any, error) {
		backendCalled = true
		return map[string]any{"status": "bypassed"}, nil
	}))

	// Register a tool that is disabled
	tool, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "guarded_tool",
		Classification: toolgateway.ClassificationWrite,
		InputSchema:    map[string]any{"type": "object"},
		HandlerID:      "custom:unreachable",
	})
	require.NoError(t, err)

	_, err = registry.SetToolEnabled(ctx, tenantID, tool.ID, false)
	require.NoError(t, err)

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{tool.Name},
	}

	// Dispatch through the ToolGateway interface
	var toolGW agentruntime.ToolGateway = gw
	resp, err := toolGW.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-bypass-check",
		ToolName:       tool.Name,
		Arguments:      map[string]any{"bypass": true},
	})
	require.NoError(t, err)
	assert.False(t, resp.Allowed)
	assert.Equal(t, toolgateway.DenialCodeToolDisabled, resp.DenialCode)
	assert.False(t, backendCalled, "backend handler must not be reachable when gateway blocks")
}
