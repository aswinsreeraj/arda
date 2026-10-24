package toolgateway_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/mcp"
	"github.com/aswinsreeraj/arda/pkg/mcp/simulator"
	"github.com/aswinsreeraj/arda/pkg/toolgateway"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
)

func setupBenchmarkGateway(b *testing.B, maxReversalGuardrail float64) (
	*toolgateway.Gateway,
	*simulator.TransactionSimulator,
	uuid.UUID,
	uuid.UUID,
	uuid.UUID,
) {
	b.Helper()
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	sim := simulator.NewTransactionSimulator(tenantID)
	mgr := mcp.NewConnectionManager(mcp.ManagerConfig{AllowLocalhost: true})
	connID := "mcp-tx-conn-bench"
	if err := mgr.RegisterInProcessServer(connID, tenantID, sim.Server()); err != nil {
		b.Fatalf("failed to register in-process server: %v", err)
	}

	store := toolgateway.NewMemoryStore()
	registry := toolgateway.NewRegistry(store)
	handlers := toolgateway.NewHandlerRegistry()

	readHandlerID := mcp.FormatHandlerID(connID, "get_transaction")
	handlers.Register(readHandlerID, mcp.NewToolHandler(mgr, connID, "get_transaction"))

	writeHandlerID := mcp.FormatHandlerID(connID, "reverse_transaction")
	handlers.Register(writeHandlerID, mcp.NewToolHandler(mgr, connID, "reverse_transaction"))

	ctx := context.Background()
	_, err := registry.RegisterTool(ctx, toolgateway.ToolDefinition{
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
		HandlerID: readHandlerID,
	})
	if err != nil {
		b.Fatalf("failed to register get_transaction tool: %v", err)
	}

	_, err = registry.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "reverse_transaction",
		Description:    "Reverse a settled synthetic transaction",
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
		HandlerID: writeHandlerID,
	})
	if err != nil {
		b.Fatalf("failed to register reverse_transaction tool: %v", err)
	}

	guardrail := toolgateway.NewCompositeGuardrail(
		toolgateway.NewDestructiveOperationGuardrail(),
		toolgateway.NewParameterBoundsGuardrail(maxReversalGuardrail),
	)

	cfgProvider := &mockConfigProvider{
		configs: map[string]domain.Configuration{
			fmt.Sprintf("%s:%s:%s", tenantID, agentID, versionID): {
				PromptTemplate: "Execute tool task for {{input.query}}",
				Tools:          []string{"get_transaction", "reverse_transaction"},
				ExecutionMode:  domain.ExecutionModeAutonomous,
			},
		},
	}

	gw := toolgateway.NewGateway(toolgateway.GatewayConfig{
		Registry:        registry,
		Store:           store,
		Guardrail:       guardrail,
		HandlerRegistry: handlers,
		ConfigProvider:  cfgProvider,
		AuditLogger:     toolgateway.NewStoreAuditLogger(store),
	})

	return gw, sim, tenantID, agentID, versionID
}

// Benchmark_SuccessfulRead measures the throughput and latency of the full 10-step governance pipeline
// dispatching a read tool call to the in-process MCP transaction simulator server.
func Benchmark_SuccessfulRead(b *testing.B) {
	gw, _, tenantID, agentID, versionID := setupBenchmarkGateway(b, 1000.0)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		execID := fmt.Sprintf("bench-read-%d", i)
		resp, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
			TenantID:       tenantID,
			AgentID:        agentID,
			AgentVersionID: versionID,
			ExecutionID:    execID,
			ToolName:       "get_transaction",
			Arguments: map[string]any{
				"transaction_id": "tx_1001",
			},
		})
		if err != nil || !resp.Allowed {
			b.Fatalf("read invocation failed on iteration %d: err=%v, allowed=%v, denial=%s", i, err, resp.Allowed, resp.DenialCode)
		}
	}
}

// Benchmark_SuccessfulWrite measures the throughput and latency of the full 10-step governance pipeline,
// including parameter bounds guardrails, idempotency locking, MCP dispatch, and simulator state mutation.
func Benchmark_SuccessfulWrite(b *testing.B) {
	gw, sim, tenantID, agentID, versionID := setupBenchmarkGateway(b, 1000.0)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		txID := fmt.Sprintf("tx_bench_write_%d", i)
		execID := fmt.Sprintf("bench-write-%d", i)
		sim.AddTransaction(simulator.Transaction{
			ID:        txID,
			TenantID:  tenantID,
			Amount:    75.00,
			Currency:  "USD",
			Merchant:  "Benchmark Merchant",
			Status:    "SETTLED",
			CreatedAt: time.Now().UTC(),
		})
		resp, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
			TenantID:       tenantID,
			AgentID:        agentID,
			AgentVersionID: versionID,
			ExecutionID:    execID,
			ToolName:       "reverse_transaction",
			Arguments: map[string]any{
				"transaction_id": txID,
				"amount":         75.00,
				"reason":         "benchmark_reversal",
			},
		})
		if err != nil || !resp.Allowed {
			b.Fatalf("write invocation failed on iteration %d: err=%v, allowed=%v, denial=%s", i, err, resp.Allowed, resp.DenialCode)
		}
	}
}

// Benchmark_GuardrailDenial measures the latency of early guardrail enforcement at Step 7 of the governance pipeline,
// verifying performance when prohibited requests are blocked before reaching downstream MCP servers.
func Benchmark_GuardrailDenial(b *testing.B) {
	// Guardrail threshold set to $500.0
	gw, _, tenantID, agentID, versionID := setupBenchmarkGateway(b, 500.0)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		execID := fmt.Sprintf("bench-guardrail-%d", i)
		resp, err := gw.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
			TenantID:       tenantID,
			AgentID:        agentID,
			AgentVersionID: versionID,
			ExecutionID:    execID,
			ToolName:       "reverse_transaction",
			Arguments: map[string]any{
				"transaction_id": "tx_1003",
				"amount":         1200.00, // Prohibited write: > 500.0 threshold
				"reason":         "benchmark_unauthorized",
			},
		})
		if err != nil {
			b.Fatalf("unexpected error on iteration %d: %v", i, err)
		}
		if resp.Allowed || resp.DenialCode != toolgateway.DenialCodeGuardrailDenied {
			b.Fatalf("expected guardrail denial on iteration %d: allowed=%v, denial=%s", i, resp.Allowed, resp.DenialCode)
		}
	}
}
