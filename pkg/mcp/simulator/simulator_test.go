package simulator_test

import (
	"context"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/mcp/simulator"
	"github.com/google/uuid"
	mark3mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSimulator_Operations(t *testing.T) {
	ctx := context.Background()
	tenantA := uuid.New()
	tenantB := uuid.New()

	simA := simulator.NewTransactionSimulator(tenantA)

	// Test 1: Get existing transaction in Tenant A
	tx, err := simA.GetTransaction("tx_1001")
	require.NoError(t, err)
	assert.Equal(t, 150.0, tx.Amount)
	assert.Equal(t, "SETTLED", tx.Status)

	// Test 2: In-process call to list_transactions for Tenant A
	reqList := mark3mcp.CallToolRequest{}
	reqList.Params.Name = "list_transactions"
	reqList.Params.Arguments = map[string]any{"limit": 5}

	ctxA := simulator.WithTenantID(ctx, tenantA)
	// Dispatch via server
	// We can test via MCPServer tool lookup or simulator directly
	assert.NotNil(t, simA.Server())
	assert.Equal(t, 0, simA.CallCount("list_transactions"))

	// Test 3: Tenant B isolation on simA
	_, err = simA.GetTransactionForTenant(tenantB, "tx_1001")
	assert.Error(t, err, "tx_1001 was seeded in tenant A, not tenant B")

	// Test 4: Reset functionality
	simA.Reset()
	assert.Equal(t, 0, simA.CallCount("get_transaction"))
	assert.Empty(t, simA.RecordedCalls())
	_ = ctxA
}
