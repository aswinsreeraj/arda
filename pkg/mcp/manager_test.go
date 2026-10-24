package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/pkg/mcp"
	"github.com/aswinsreeraj/arda/pkg/mcp/simulator"
	"github.com/aswinsreeraj/arda/pkg/toolgateway"
	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/google/uuid"
	mark3mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 1. Tenant-scoped connection lookup
func TestConnectionManager_TenantIsolation(t *testing.T) {
	ctx := context.Background()
	mgr := mcp.NewConnectionManager(mcp.ManagerConfig{AllowLocalhost: true})

	tenantA := uuid.New()
	tenantB := uuid.New()

	connA := mcp.ConnectionDefinition{
		ID:         "conn-a",
		TenantID:   tenantA,
		ServerName: "server-a",
		Transport:  mcp.TransportInProcess,
	}

	_, err := mgr.RegisterConnection(ctx, connA)
	require.NoError(t, err)

	// Tenant A can retrieve its own connection
	fetchedA, err := mgr.GetConnection(ctx, tenantA, "conn-a")
	require.NoError(t, err)
	assert.Equal(t, "conn-a", fetchedA.ID)
	assert.Equal(t, tenantA, fetchedA.TenantID)

	// Tenant B cannot retrieve Tenant A's connection
	_, err = mgr.GetConnection(ctx, tenantB, "conn-a")
	assert.ErrorIs(t, err, mcp.ErrConnectionNotFound)

	// Tenant B's connection list is empty
	listB, err := mgr.ListConnections(ctx, tenantB)
	require.NoError(t, err)
	assert.Empty(t, listB)
}

// 2. Credential isolation and Vault failures
func TestConnectionManager_VaultCredentials(t *testing.T) {
	ctx := context.Background()
	vaultStore := vault.NewMemoryStore()
	resolver, err := mcp.NewVaultCredentialResolver(vaultStore)
	require.NoError(t, err)

	tenantA := uuid.New()
	tenantB := uuid.New()

	// Write secret for Tenant A
	err = vaultStore.WriteSecret(ctx, tenantA, "credentials/mcp/tx_system", map[string]string{
		"access_token": "secret-token-tenant-a",
	})
	require.NoError(t, err)

	ref := vault.FormatReferenceWithField("credentials/mcp/tx_system", "access_token")

	// Tenant A successfully resolves its own token
	tokA, err := resolver.Resolve(ctx, tenantA, ref)
	require.NoError(t, err)
	assert.Equal(t, "secret-token-tenant-a", tokA)

	// Tenant B attempting to resolve Tenant A's reference fails
	tokB, err := resolver.Resolve(ctx, tenantB, ref)
	assert.Error(t, err)
	assert.Empty(t, tokB)

	// Non-existent secret fails gracefully
	_, err = resolver.Resolve(ctx, tenantA, "secret:credentials/mcp/nonexistent#access_token")
	assert.Error(t, err)
}

// 3. Tool discovery and schema reconciliation
func TestConnectionManager_DiscoveryAndReconciliation(t *testing.T) {
	ctx := context.Background()
	mgr := mcp.NewConnectionManager(mcp.ManagerConfig{AllowLocalhost: true})
	tenantID := uuid.New()
	connID := "tx-conn"

	sim := simulator.NewTransactionSimulator(tenantID)
	err := mgr.RegisterInProcessServer(connID, tenantID, sim.Server())
	require.NoError(t, err)

	// Discover tools from simulator
	tools, err := mgr.DiscoverTools(ctx, tenantID, connID)
	require.NoError(t, err)
	require.Len(t, tools, 3)

	var getTxTool mark3mcp.Tool
	for _, tool := range tools {
		if tool.Name == "get_transaction" {
			getTxTool = tool
			break
		}
	}
	require.NotEmpty(t, getTxTool.Name)

	// Build registered tool definition matching discovered schema
	schemaBytes, err := json.Marshal(getTxTool.InputSchema)
	require.NoError(t, err)
	var inputSchema map[string]any
	require.NoError(t, json.Unmarshal(schemaBytes, &inputSchema))

	registeredTool := toolgateway.ToolDefinition{
		Name:        "get_transaction",
		InputSchema: inputSchema,
	}

	// Reconciliation succeeds on identical schema
	err = mgr.ReconcileTool(registeredTool, getTxTool)
	require.NoError(t, err)

	// Reconciliation fails on schema drift (e.g. modified property)
	driftedTool := registeredTool
	driftedTool.InputSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"transaction_id": map[string]any{"type": "integer"}, // changed string to integer
		},
		"required": []any{"transaction_id"},
	}
	err = mgr.ReconcileTool(driftedTool, getTxTool)
	assert.ErrorIs(t, err, mcp.ErrSchemaMismatch)
}

// 4. MCP timeouts, cancellation, and oversized results
func TestConnectionManager_TimeoutsAndOversized(t *testing.T) {
	ctx := context.Background()
	mgr := mcp.NewConnectionManager(mcp.ManagerConfig{AllowLocalhost: true})
	tenantID := uuid.New()

	sim := simulator.NewTransactionSimulator(tenantID)
	// Register a slow tool
	sim.Server().AddTool(
		mark3mcp.NewTool("slow_op", mark3mcp.WithDescription("Slow operation")),
		func(ctx context.Context, req mark3mcp.CallToolRequest) (*mark3mcp.CallToolResult, error) {
			select {
			case <-time.After(200 * time.Millisecond):
				return mark3mcp.NewToolResultText(`{"status":"done"}`), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	)

	// Register huge response tool
	sim.Server().AddTool(
		mark3mcp.NewTool("huge_op", mark3mcp.WithDescription("Huge operation")),
		func(ctx context.Context, req mark3mcp.CallToolRequest) (*mark3mcp.CallToolResult, error) {
			hugeStr := strings.Repeat("X", 70*1024)
			return mark3mcp.NewToolResultText(fmt.Sprintf(`{"data":"%s"}`, hugeStr)), nil
		},
	)

	connID := "perf-conn"
	conn := mcp.ConnectionDefinition{
		ID:               connID,
		TenantID:         tenantID,
		ServerName:       "perf-server",
		Transport:        mcp.TransportInProcess,
		Timeout:          50 * time.Millisecond, // very short timeout
		MaxResponseBytes: 64 * 1024,
	}
	_, err := mgr.RegisterConnection(ctx, conn)
	require.NoError(t, err)
	_ = mgr.RegisterInProcessServer(connID, tenantID, sim.Server())

	// Timeout enforcement
	_, err = mgr.CallTool(ctx, mcp.CallToolOptions{
		TenantID:     tenantID,
		ConnectionID: connID,
		ToolName:     "slow_op",
		Arguments:    map[string]any{},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")

	// Context cancellation
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	_, err = mgr.CallTool(cancelCtx, mcp.CallToolOptions{
		TenantID:     tenantID,
		ConnectionID: connID,
		ToolName:     "slow_op",
		Arguments:    map[string]any{},
	})
	assert.ErrorIs(t, err, context.Canceled)

	// Oversized response enforcement (> 64KB)
	longCtx := context.Background()
	_, err = mgr.CallTool(longCtx, mcp.CallToolOptions{
		TenantID:     tenantID,
		ConnectionID: connID,
		ToolName:     "huge_op",
		Arguments:    map[string]any{},
	})
	assert.ErrorIs(t, err, mcp.ErrOversizedResponse)
}

// SSRF prevention test
func TestSSRFPrevention(t *testing.T) {
	// Rejects AWS / GCP / Azure metadata endpoint
	err := mcp.ValidateEndpoint("http://169.254.169.254/latest/meta-data/", false)
	assert.ErrorIs(t, err, mcp.ErrSSRFBlocked)

	// Rejects GCP metadata domain
	err = mcp.ValidateEndpoint("http://metadata.google.internal/computeMetadata/v1/", false)
	assert.ErrorIs(t, err, mcp.ErrSSRFBlocked)

	// Rejects loopback in production mode
	err = mcp.ValidateEndpoint("http://127.0.0.1:8080/mcp", false)
	assert.ErrorIs(t, err, mcp.ErrSSRFBlocked)

	// Allows loopback when explicitly permitted for testing/development
	err = mcp.ValidateEndpoint("http://127.0.0.1:8080/mcp", true)
	assert.NoError(t, err)

	// Rejects non-HTTP schemes
	err = mcp.ValidateEndpoint("file:///etc/passwd", true)
	assert.ErrorIs(t, err, mcp.ErrInvalidEndpoint)
}
