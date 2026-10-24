package toolgateway

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// HandlerExecutionContext passes trusted server context to the transport-neutral tool handler.
type HandlerExecutionContext struct {
	TenantID       uuid.UUID
	AgentID        uuid.UUID
	AgentVersionID uuid.UUID
	ExecutionID    string
	ToolID         string
	ToolName       string
	InvocationID   string
}

// ToolHandler defines the transport-neutral execution boundary for concrete tool implementations.
type ToolHandler interface {
	Execute(ctx context.Context, execCtx HandlerExecutionContext, arguments map[string]any) (map[string]any, error)
}

// HandlerRegistry maps handler identifiers to their concrete implementations.
type HandlerRegistry struct {
	mu       sync.RWMutex
	handlers map[string]ToolHandler
}

// NewHandlerRegistry constructs a new HandlerRegistry populated with standard built-in handlers.
func NewHandlerRegistry() *HandlerRegistry {
	r := &HandlerRegistry{
		handlers: make(map[string]ToolHandler),
	}
	// Register standard built-ins
	r.Register("builtin:echo", &EchoHandler{})
	r.Register("builtin:calculator", &CalculatorHandler{})
	r.Register("builtin:kb_search", &KBSearchHandler{})
	return r
}

// Register registers a handler under a specific identifier.
func (r *HandlerRegistry) Register(id string, handler ToolHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[id] = handler
}

// Get retrieves a handler by identifier.
func (r *HandlerRegistry) Get(id string) (ToolHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[id]
	return h, ok
}

// EchoHandler simply echoes back received arguments.
type EchoHandler struct{}

func (h *EchoHandler) Execute(ctx context.Context, execCtx HandlerExecutionContext, args map[string]any) (map[string]any, error) {
	return map[string]any{
		"echo":      args,
		"tool_name": execCtx.ToolName,
	}, nil
}

// CalculatorHandler performs arithmetic operations.
type CalculatorHandler struct{}

func (h *CalculatorHandler) Execute(ctx context.Context, execCtx HandlerExecutionContext, args map[string]any) (map[string]any, error) {
	op, _ := args["operation"].(string)
	var a, b float64

	switch val := args["a"].(type) {
	case float64:
		a = val
	case int:
		a = float64(val)
	}

	switch val := args["b"].(type) {
	case float64:
		b = val
	case int:
		b = float64(val)
	}

	var res float64
	switch op {
	case "add", "+":
		res = a + b
	case "subtract", "-":
		res = a - b
	case "multiply", "*":
		res = a * b
	case "divide", "/":
		if b == 0 {
			return nil, fmt.Errorf("division by zero")
		}
		res = a / b
	default:
		return nil, fmt.Errorf("unsupported operation %q", op)
	}

	return map[string]any{"result": res}, nil
}

// KBSearchHandler simulates knowledge base search.
type KBSearchHandler struct{}

func (h *KBSearchHandler) Execute(ctx context.Context, execCtx HandlerExecutionContext, args map[string]any) (map[string]any, error) {
	query, _ := args["query"].(string)
	return map[string]any{
		"query": query,
		"results": []any{
			map[string]any{"id": "doc-1", "snippet": "Relevant article for: " + query},
		},
	}, nil
}

// CustomToolHandlerFunc is an adapter to allow use of ordinary functions as tool handlers.
type CustomToolHandlerFunc func(ctx context.Context, execCtx HandlerExecutionContext, args map[string]any) (map[string]any, error)

func (f CustomToolHandlerFunc) Execute(ctx context.Context, execCtx HandlerExecutionContext, args map[string]any) (map[string]any, error) {
	return f(ctx, execCtx, args)
}
