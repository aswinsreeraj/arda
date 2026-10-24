package mcp_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/mcp"
	"github.com/aswinsreeraj/arda/pkg/mcp/simulator"
	"github.com/aswinsreeraj/arda/pkg/toolgateway"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type staticConfigProvider struct {
	configs map[string]domain.Configuration
}

func (s *staticConfigProvider) GetAgentConfig(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (domain.Configuration, error) {
	key := fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)
	cfg, ok := s.configs[key]
	if !ok {
		return domain.Configuration{}, fmt.Errorf("config not found")
	}
	return cfg, nil
}

func setupMCPGateway(t *testing.T, tenantID uuid.UUID, maxAmountGuardrail float64) (
	*toolgateway.Gateway,
	*toolgateway.DefaultRegistry,
	*toolgateway.MemoryStore,
	*mcp.ConnectionManager,
	*simulator.TransactionSimulator,
	*staticConfigProvider,
) {
	store := toolgateway.NewMemoryStore()
	registry := toolgateway.NewRegistry(store)
	handlers := toolgateway.NewHandlerRegistry()

	cfgProvider := &staticConfigProvider{
		configs: make(map[string]domain.Configuration),
	}

	mgr := mcp.NewConnectionManager(mcp.ManagerConfig{AllowLocalhost: true})
	sim := simulator.NewTransactionSimulator(tenantID)

	connID := "mcp-tx-conn"
	err := mgr.RegisterInProcessServer(connID, tenantID, sim.Server())
	require.NoError(t, err)

	// Build guardrail
	guardrail := toolgateway.NewCompositeGuardrail(
		toolgateway.NewDestructiveOperationGuardrail(),
		toolgateway.NewParameterBoundsGuardrail(maxAmountGuardrail),
	)

	gw := toolgateway.NewGateway(toolgateway.GatewayConfig{
		Registry:        registry,
		Store:           store,
		Guardrail:       guardrail,
		HandlerRegistry: handlers,
		AuditLogger:     toolgateway.NewStoreAuditLogger(store),
		ConfigProvider:  cfgProvider,
	})

	return gw, registry, store, mgr, sim, cfgProvider
}

// 7. Successful read invocation through complete path (Runtime -> Tool Gateway -> MCP Manager -> MCP Server)
func TestMCPGatewayIntegration_SuccessfulRead(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	gw, registry, store, mgr, sim, cfgProvider := setupMCPGateway(t, tenantID, 1000.0)
	connID := "mcp-tx-conn"

	// Register MCP handler in Tool Gateway's HandlerRegistry
	handlerID := mcp.FormatHandlerID(connID, "get_transaction")
	gwHandler := mcp.NewToolHandler(mgr, connID, "get_transaction")
	// Register with gateway handlers
	// Register in HandlerRegistry via gateway constructor or directly
	// Let's create tool definition in Tool Registry
	toolDef, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "get_transaction",
		Description:    "Get details of a synthetic transaction",
		Classification: toolgateway.ClassificationRead,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"transaction_id": map[string]any{"type": "string"},
			},
			"required": []any{"transaction_id"},
		},
		HandlerID: handlerID,
	})
	require.NoError(t, err)

	// Register tool handler in the gateway
	// Handler is registered under handlerID
	customHandlers := toolgateway.NewHandlerRegistry()
	customHandlers.Register(handlerID, gwHandler)

	// Reconfigure gateway with customHandlers
	gw = toolgateway.NewGateway(toolgateway.GatewayConfig{
		Registry:        registry,
		Store:           store,
		HandlerRegistry: customHandlers,
		ConfigProvider:  cfgProvider,
	})

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{toolDef.Name},
	}

	// Dispatch tool call through ToolGateway interface
	var gatewayInterface agentruntime.ToolGateway = gw
	resp, err := gatewayInterface.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-read-1",
		ToolName:       "get_transaction",
		Arguments: map[string]any{
			"transaction_id": "tx_1001",
		},
	})
	require.NoError(t, err)
	assert.True(t, resp.Allowed)
	assert.Equal(t, "tx_1001", resp.Result["id"])
	assert.Equal(t, "CloudHost Services", resp.Result["merchant"])
	assert.Equal(t, 150.0, resp.Result["amount"])
	assert.Equal(t, "SETTLED", resp.Result["status"])

	// Verify simulator recorded call
	assert.Equal(t, 1, sim.CallCount("get_transaction"))

	// Verify audit trail recorded
	events, err := store.ListAuditEvents(ctx, tenantID, 1)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.True(t, events[0].Allowed)
	assert.Equal(t, "get_transaction", events[0].ToolName)
}

// 8. Successful simulated write invocation
func TestMCPGatewayIntegration_SuccessfulWrite(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	gw, registry, store, mgr, sim, cfgProvider := setupMCPGateway(t, tenantID, 1000.0)
	connID := "mcp-tx-conn"

	handlerID := mcp.FormatHandlerID(connID, "reverse_transaction")
	gwHandler := mcp.NewToolHandler(mgr, connID, "reverse_transaction")

	toolDef, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "reverse_transaction",
		Description:    "Reverse a settled transaction",
		Classification: toolgateway.ClassificationWrite,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"transaction_id": map[string]any{"type": "string"},
				"amount":         map[string]any{"type": "number"},
				"reason":         map[string]any{"type": "string"},
			},
			"required": []any{"transaction_id", "amount", "reason"},
		},
		HandlerID: handlerID,
	})
	require.NoError(t, err)

	customHandlers := toolgateway.NewHandlerRegistry()
	customHandlers.Register(handlerID, gwHandler)

	gw = toolgateway.NewGateway(toolgateway.GatewayConfig{
		Registry:        registry,
		Store:           store,
		HandlerRegistry: customHandlers,
		ConfigProvider:  cfgProvider,
	})

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{toolDef.Name},
	}

	// Dispatch valid reversal ($150 on tx_1001)
	var gatewayInterface agentruntime.ToolGateway = gw
	resp, err := gatewayInterface.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-write-1",
		ToolName:       "reverse_transaction",
		Arguments: map[string]any{
			"transaction_id": "tx_1001",
			"amount":         150.0,
			"reason":         "customer_request",
		},
	})
	require.NoError(t, err)
	assert.True(t, resp.Allowed)
	assert.Equal(t, "REVERSED", resp.Result["status"])
	assert.Equal(t, 150.0, resp.Result["reversed_amount"])

	// Verify simulator was called once and status updated
	assert.Equal(t, 1, sim.CallCount("reverse_transaction"))
	tx, err := sim.GetTransaction("tx_1001")
	require.NoError(t, err)
	assert.Equal(t, "REVERSED", tx.Status)
}

// 9. Blocked write invocation with proof of zero downstream dispatches
func TestMCPGatewayIntegration_HardGuardrailBlocksBeforeMCP(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	// Max amount guardrail set to $500.0
	gw, registry, store, mgr, sim, cfgProvider := setupMCPGateway(t, tenantID, 500.0)
	connID := "mcp-tx-conn"

	handlerID := mcp.FormatHandlerID(connID, "reverse_transaction")
	gwHandler := mcp.NewToolHandler(mgr, connID, "reverse_transaction")

	toolDef, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "reverse_transaction",
		Description:    "Reverse a settled transaction",
		Classification: toolgateway.ClassificationWrite,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"transaction_id": map[string]any{"type": "string"},
				"amount":         map[string]any{"type": "number"},
				"reason":         map[string]any{"type": "string"},
			},
			"required": []any{"transaction_id", "amount", "reason"},
		},
		HandlerID: handlerID,
	})
	require.NoError(t, err)

	customHandlers := toolgateway.NewHandlerRegistry()
	customHandlers.Register(handlerID, gwHandler)

	// Hard guardrail: Parameter bounds threshold $500.0
	guardrail := toolgateway.NewCompositeGuardrail(
		toolgateway.NewDestructiveOperationGuardrail(),
		toolgateway.NewParameterBoundsGuardrail(500.0),
	)

	gw = toolgateway.NewGateway(toolgateway.GatewayConfig{
		Registry:        registry,
		Store:           store,
		Guardrail:       guardrail,
		HandlerRegistry: customHandlers,
		ConfigProvider:  cfgProvider,
	})

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{toolDef.Name},
	}

	// Attempt to reverse tx_1003 with amount $1200.0 (> $500.0 threshold)
	var gatewayInterface agentruntime.ToolGateway = gw
	resp, err := gatewayInterface.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-guard-blocked",
		ToolName:       "reverse_transaction",
		Arguments: map[string]any{
			"transaction_id": "tx_1003",
			"amount":         1200.0,
			"reason":         "unauthorized_charge",
		},
	})
	require.NoError(t, err)

	// 1. Verify Gateway denied execution
	assert.False(t, resp.Allowed)
	assert.Equal(t, toolgateway.DenialCodeGuardrailDenied, resp.DenialCode)
	assert.Contains(t, resp.Error, "exceeds maximum allowed threshold of 500.00")

	// 2. VERIFIABLE PROOF: Simulator call count for reverse_transaction is strictly 0!
	assert.Equal(t, 0, sim.CallCount("reverse_transaction"), "MCP simulator must NEVER receive the blocked call")

	// 3. Verify underlying transaction status remains SETTLED (no side effects)
	tx, err := sim.GetTransaction("tx_1003")
	require.NoError(t, err)
	assert.Equal(t, "SETTLED", tx.Status)

	// 4. Verify audit event was logged with denial metadata
	events, err := store.ListAuditEvents(ctx, tenantID, 1)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.False(t, events[0].Allowed)
	assert.Equal(t, toolgateway.DenialCodeGuardrailDenied, events[0].DenialCode)
	assert.Equal(t, "exec-guard-blocked", events[0].ExecutionID)
}

// 10. Invocation ID propagation and repeated invocation idempotency
func TestMCPGatewayIntegration_Idempotency(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()
	execID := "exec-idempotency-mcp"

	gw, registry, store, mgr, sim, cfgProvider := setupMCPGateway(t, tenantID, 1000.0)
	connID := "mcp-tx-conn"

	handlerID := mcp.FormatHandlerID(connID, "reverse_transaction")
	customHandlers := toolgateway.NewHandlerRegistry()
	customHandlers.Register(handlerID, mcp.NewToolHandler(mgr, connID, "reverse_transaction"))

	toolDef, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "reverse_transaction",
		Classification: toolgateway.ClassificationWrite,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"transaction_id": map[string]any{"type": "string"},
				"amount":         map[string]any{"type": "number"},
				"reason":         map[string]any{"type": "string"},
			},
			"required": []any{"transaction_id", "amount", "reason"},
		},
		HandlerID: handlerID,
	})
	require.NoError(t, err)

	gw = toolgateway.NewGateway(toolgateway.GatewayConfig{
		Registry:        registry,
		Store:           store,
		HandlerRegistry: customHandlers,
		ConfigProvider:  cfgProvider,
	})

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{toolDef.Name},
	}

	req := agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    execID,
		ToolName:       "reverse_transaction",
		Arguments: map[string]any{
			"transaction_id": "tx_1002",
			"amount":         42.50,
			"reason":         "duplicate",
		},
	}

	// Call 1: succeeds and calls MCP server
	resp1, err := gw.ExecuteTool(ctx, req)
	require.NoError(t, err)
	assert.True(t, resp1.Allowed)
	assert.Equal(t, 1, sim.CallCount("reverse_transaction"))

	// Call 2: repeated call with same execution and arguments
	resp2, err := gw.ExecuteTool(ctx, req)
	require.NoError(t, err)
	assert.True(t, resp2.Allowed)
	assert.Equal(t, resp1.Output, resp2.Output)

	// Verifies MCP server was NOT invoked a second time!
	assert.Equal(t, 1, sim.CallCount("reverse_transaction"))
}

// 11. Audit logging and secret redaction
func TestMCPGatewayIntegration_AuditAndSecretRedaction(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	gw, registry, store, mgr, _, cfgProvider := setupMCPGateway(t, tenantID, 1000.0)
	connID := "mcp-tx-conn"

	handlerID := mcp.FormatHandlerID(connID, "get_transaction")
	customHandlers := toolgateway.NewHandlerRegistry()
	customHandlers.Register(handlerID, mcp.NewToolHandler(mgr, connID, "get_transaction"))

	toolDef, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "get_transaction",
		Classification: toolgateway.ClassificationRead,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"transaction_id": map[string]any{"type": "string"},
				"auth_token":     map[string]any{"type": "string"},
			},
			"required": []any{"transaction_id"},
		},
		HandlerID: handlerID,
	})
	require.NoError(t, err)

	gw = toolgateway.NewGateway(toolgateway.GatewayConfig{
		Registry:        registry,
		Store:           store,
		HandlerRegistry: customHandlers,
		ConfigProvider:  cfgProvider,
	})

	cfgProvider.configs[fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID)] = domain.Configuration{
		Tools: []string{toolDef.Name},
	}

	_, err = gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-audit-secrets",
		ToolName:       "get_transaction",
		Arguments: map[string]any{
			"transaction_id": "tx_1001",
			"auth_token":     "super-secret-mcp-bearer-token",
		},
	})
	require.NoError(t, err)

	events, err := store.ListAuditEvents(ctx, tenantID, 1)
	require.NoError(t, err)
	require.Len(t, events, 1)

	loggedArgs := events[0].SanitizedArguments
	assert.Equal(t, "tx_1001", loggedArgs["transaction_id"])
	assert.Equal(t, "[REDACTED]", loggedArgs["auth_token"], "sensitive token MUST be redacted in audit logs")
}

// 12. Connection and server tenant isolation
func TestMCPGatewayIntegration_TenantIsolation(t *testing.T) {
	ctx := context.Background()
	tenantA := uuid.New()
	tenantB := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	gwA, registryA, storeA, mgr, _, cfgProviderA := setupMCPGateway(t, tenantA, 1000.0)
	connIDA := "mcp-tx-conn-a"
	handlerIDA := mcp.FormatHandlerID(connIDA, "get_transaction")

	customHandlersA := toolgateway.NewHandlerRegistry()
	customHandlersA.Register(handlerIDA, mcp.NewToolHandler(mgr, connIDA, "get_transaction"))

	toolDefA, err := registryA.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantA,
		Name:           "get_transaction",
		Classification: toolgateway.ClassificationRead,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"transaction_id": map[string]any{"type": "string"},
			},
		},
		HandlerID: handlerIDA,
	})
	require.NoError(t, err)

	gwA = toolgateway.NewGateway(toolgateway.GatewayConfig{
		Registry:        registryA,
		Store:           storeA,
		HandlerRegistry: customHandlersA,
		ConfigProvider:  cfgProviderA,
	})

	cfgProviderA.configs[fmt.Sprintf("%s:%s:%s", tenantA, agentID, versionID)] = domain.Configuration{
		Tools: []string{toolDefA.Name},
	}
	cfgProviderA.configs[fmt.Sprintf("%s:%s:%s", tenantB, agentID, versionID)] = domain.Configuration{
		Tools: []string{toolDefA.Name},
	}

	// Tenant B tries to dispatch to Tenant A's tool and connection
	resp, err := gwA.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       tenantB, // Cross-tenant!
		AgentID:        agentID,
		AgentVersionID: versionID,
		ExecutionID:    "exec-cross-tenant",
		ToolName:       "get_transaction",
		Arguments: map[string]any{
			"transaction_id": "tx_1001",
		},
	})
	require.NoError(t, err)
	assert.False(t, resp.Allowed)
	assert.Equal(t, toolgateway.DenialCodeToolNotFound, resp.DenialCode)
}
