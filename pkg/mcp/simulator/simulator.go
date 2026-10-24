package simulator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type tenantCtxKey struct{}

// WithTenantID binds the verified tenant identity to the context.
func WithTenantID(ctx context.Context, tenantID uuid.UUID) context.Context {
	return context.WithValue(ctx, tenantCtxKey{}, tenantID)
}

// TenantIDFromContext extracts the verified tenant identity from the context.
func TenantIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	if ctx == nil {
		return uuid.Nil, false
	}
	id, ok := ctx.Value(tenantCtxKey{}).(uuid.UUID)
	return id, ok && id != uuid.Nil
}

// Transaction represents a synthetic financial transaction.
type Transaction struct {
	ID        string    `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Amount    float64   `json:"amount"`
	Currency  string    `json:"currency"`
	Merchant  string    `json:"merchant"`
	Status    string    `json:"status"` // "SETTLED", "REVERSED", "PENDING"
	CreatedAt time.Time `json:"created_at"`
}

// CallRecord records invocation details for test observability.
type CallRecord struct {
	Operation string
	TenantID  uuid.UUID
	Arguments map[string]any
	Timestamp time.Time
}

// TransactionSimulator hosts an in-memory synthetic transaction system exposed as an MCP server.
type TransactionSimulator struct {
	mu           sync.RWMutex
	tenantID     uuid.UUID
	transactions map[string]Transaction
	calls        []CallRecord
	server       *server.MCPServer
}

// NewTransactionSimulator initializes a simulator with pre-seeded synthetic data for the given tenant.
func NewTransactionSimulator(tenantID uuid.UUID) *TransactionSimulator {
	sim := &TransactionSimulator{
		tenantID:     tenantID,
		transactions: make(map[string]Transaction),
		calls:        make([]CallRecord, 0),
		server:       server.NewMCPServer("simulated-transaction-system", "1.0.0"),
	}

	// Seed synthetic transactions
	sim.seedTransactions()
	// Register MCP tools
	sim.registerTools()

	return sim
}

func (s *TransactionSimulator) seedTransactions() {
	now := time.Now().UTC()
	txs := []Transaction{
		{
			ID:        "tx_1001",
			TenantID:  s.tenantID,
			Amount:    150.00,
			Currency:  "USD",
			Merchant:  "CloudHost Services",
			Status:    "SETTLED",
			CreatedAt: now.Add(-2 * time.Hour),
		},
		{
			ID:        "tx_1002",
			TenantID:  s.tenantID,
			Amount:    42.50,
			Currency:  "USD",
			Merchant:  "DevBooks Supply",
			Status:    "SETTLED",
			CreatedAt: now.Add(-5 * time.Hour),
		},
		{
			ID:        "tx_1003",
			TenantID:  s.tenantID,
			Amount:    1200.00,
			Currency:  "USD",
			Merchant:  "Enterprise Server Lease",
			Status:    "SETTLED",
			CreatedAt: now.Add(-24 * time.Hour),
		},
	}

	for _, tx := range txs {
		s.transactions[tx.ID] = tx
	}
}

func (s *TransactionSimulator) registerTools() {
	// 1. get_transaction
	s.server.AddTool(
		mcp.NewTool("get_transaction",
			mcp.WithDescription("Retrieve details of a synthetic transaction"),
			mcp.WithString("transaction_id", mcp.Required(), mcp.Description("Unique transaction identifier")),
		),
		s.handleGetTransaction,
	)

	// 2. list_transactions
	s.server.AddTool(
		mcp.NewTool("list_transactions",
			mcp.WithDescription("List recent synthetic transactions for the authenticated tenant"),
			mcp.WithNumber("limit", mcp.Description("Maximum number of transactions to return")),
		),
		s.handleListTransactions,
	)

	// 3. reverse_transaction
	s.server.AddTool(
		mcp.NewTool("reverse_transaction",
			mcp.WithDescription("Request a reversal for an existing settled transaction"),
			mcp.WithString("transaction_id", mcp.Required(), mcp.Description("Transaction identifier to reverse")),
			mcp.WithNumber("amount", mcp.Required(), mcp.Description("Reversal amount")),
			mcp.WithString("reason", mcp.Required(), mcp.Description("Business reason for reversal")),
		),
		s.handleReverseTransaction,
	)
}

func (s *TransactionSimulator) getEffectiveTenantID(ctx context.Context) uuid.UUID {
	if tid, ok := TenantIDFromContext(ctx); ok {
		return tid
	}
	return s.tenantID
}

func (s *TransactionSimulator) recordCall(op string, tid uuid.UUID, args map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, CallRecord{
		Operation: op,
		TenantID:  tid,
		Arguments: args,
		Timestamp: time.Now().UTC(),
	})
}

func (s *TransactionSimulator) handleGetTransaction(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	tid := s.getEffectiveTenantID(ctx)
	args := req.GetArguments()
	s.recordCall("get_transaction", tid, args)

	txID := req.GetString("transaction_id", "")
	if txID == "" {
		return mcp.NewToolResultError("transaction_id is required"), nil
	}

	s.mu.RLock()
	tx, exists := s.transactions[txID]
	s.mu.RUnlock()

	// Tenant isolation: if not found or belongs to another tenant, return error
	if !exists || tx.TenantID != tid {
		return mcp.NewToolResultError(fmt.Sprintf("transaction %q not found", txID)), nil
	}

	b, _ := json.Marshal(tx)
	return mcp.NewToolResultText(string(b)), nil
}

func (s *TransactionSimulator) handleListTransactions(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	tid := s.getEffectiveTenantID(ctx)
	args := req.GetArguments()
	s.recordCall("list_transactions", tid, args)

	limit := req.GetInt("limit", 10)
	if limit <= 0 {
		limit = 10
	}

	s.mu.RLock()
	var res []Transaction
	for _, tx := range s.transactions {
		if tx.TenantID == tid {
			res = append(res, tx)
			if len(res) >= limit {
				break
			}
		}
	}
	s.mu.RUnlock()

	out := map[string]any{"transactions": res}
	b, _ := json.Marshal(out)
	return mcp.NewToolResultText(string(b)), nil
}

func (s *TransactionSimulator) handleReverseTransaction(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	tid := s.getEffectiveTenantID(ctx)
	args := req.GetArguments()
	s.recordCall("reverse_transaction", tid, args)

	txID := req.GetString("transaction_id", "")
	reason := req.GetString("reason", "")
	amount := req.GetFloat("amount", 0.0)

	if txID == "" {
		return mcp.NewToolResultError("transaction_id is required"), nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, exists := s.transactions[txID]
	if !exists || tx.TenantID != tid {
		return mcp.NewToolResultError(fmt.Sprintf("transaction %q not found", txID)), nil
	}

	if tx.Status == "REVERSED" {
		return mcp.NewToolResultError(fmt.Sprintf("transaction %q is already reversed", txID)), nil
	}

	if amount <= 0 || amount > tx.Amount {
		return mcp.NewToolResultError(fmt.Sprintf("invalid reversal amount: %.2f (transaction amount is %.2f)", amount, tx.Amount)), nil
	}

	// Update status
	tx.Status = "REVERSED"
	s.transactions[txID] = tx

	reversal := map[string]any{
		"reversal_id":     fmt.Sprintf("rev_%s_%d", txID, time.Now().Unix()),
		"transaction_id":  txID,
		"status":          "REVERSED",
		"reversed_amount": amount,
		"reason":          reason,
	}

	b, _ := json.Marshal(reversal)
	return mcp.NewToolResultText(string(b)), nil
}

// Server returns the underlying MCPServer.
func (s *TransactionSimulator) Server() *server.MCPServer {
	return s.server
}

// CallCount returns how many times an operation was dispatched to this simulator.
func (s *TransactionSimulator) CallCount(operation string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, call := range s.calls {
		if call.Operation == operation {
			count++
		}
	}
	return count
}

// RecordedCalls returns a copy of all recorded calls.
func (s *TransactionSimulator) RecordedCalls() []CallRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	copied := make([]CallRecord, len(s.calls))
	copy(copied, s.calls)
	return copied
}

// Reset clears recorded calls and resets seed data.
func (s *TransactionSimulator) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = make([]CallRecord, 0)
	s.transactions = make(map[string]Transaction)
	s.seedTransactions()
}

// AddTransaction inserts an arbitrary synthetic transaction for testing.
func (s *TransactionSimulator) AddTransaction(tx Transaction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transactions[tx.ID] = tx
}

// GetTransaction retrieves a transaction by ID.
func (s *TransactionSimulator) GetTransaction(id string) (Transaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, exists := s.transactions[id]
	if !exists {
		return Transaction{}, errors.New("transaction not found")
	}
	return tx, nil
}

// GetTransactionForTenant retrieves a transaction by ID scoped strictly to the specified tenant.
func (s *TransactionSimulator) GetTransactionForTenant(tenantID uuid.UUID, id string) (Transaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, exists := s.transactions[id]
	if !exists || tx.TenantID != tenantID {
		return Transaction{}, errors.New("transaction not found")
	}
	return tx, nil
}
