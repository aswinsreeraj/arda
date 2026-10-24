package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/aswinsreeraj/arda/pkg/toolgateway"
)

// MCPHandlerIDPrefix is the standard prefix for MCP handlers registered in the Tool Gateway.
const MCPHandlerIDPrefix = "mcp:"

// FormatHandlerID creates a stable handler ID formatted as "mcp:<connection_id>:<tool_name>".
func FormatHandlerID(connectionID, toolName string) string {
	return fmt.Sprintf("%s%s:%s", MCPHandlerIDPrefix, connectionID, toolName)
}

// ParseHandlerID extracts the connection ID and server-side tool name from an MCP handler ID.
func ParseHandlerID(handlerID string) (connectionID, toolName string, ok bool) {
	if !strings.HasPrefix(handlerID, MCPHandlerIDPrefix) {
		return "", "", false
	}
	trimmed := strings.TrimPrefix(handlerID, MCPHandlerIDPrefix)
	parts := strings.SplitN(trimmed, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// ToolHandler adapts the MCP Connection Manager to the toolgateway.ToolHandler interface.
type ToolHandler struct {
	manager      *ConnectionManager
	connectionID string
	toolName     string
}

// NewToolHandler creates an MCP tool handler for a specific connection and server tool.
func NewToolHandler(manager *ConnectionManager, connectionID, toolName string) *ToolHandler {
	return &ToolHandler{
		manager:      manager,
		connectionID: connectionID,
		toolName:     toolName,
	}
}

// Execute dispatches the authorized tool call to the MCP Connection Manager.
func (h *ToolHandler) Execute(ctx context.Context, execCtx toolgateway.HandlerExecutionContext, args map[string]any) (map[string]any, error) {
	connID := h.connectionID
	toolName := h.toolName

	// If handler was not bound to a specific tool, attempt parsing from HandlerID / ToolName
	if connID == "" || toolName == "" {
		if c, t, ok := ParseHandlerID(execCtx.ToolName); ok {
			connID = c
			toolName = t
		} else {
			toolName = execCtx.ToolName
		}
	}

	opts := CallToolOptions{
		TenantID:       execCtx.TenantID,
		AgentID:        execCtx.AgentID,
		AgentVersionID: execCtx.AgentVersionID,
		ExecutionID:    execCtx.ExecutionID,
		InvocationID:   execCtx.InvocationID,
		ConnectionID:   connID,
		ToolName:       toolName,
		Arguments:      args,
	}

	return h.manager.CallTool(ctx, opts)
}
