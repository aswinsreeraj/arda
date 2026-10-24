package mcp

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TransportType identifies the transport mechanism used for the MCP connection.
type TransportType string

const (
	TransportInProcess      TransportType = "inprocess"
	TransportSSE            TransportType = "sse"
	TransportStreamableHTTP TransportType = "streamable_http"
)

// ConnectionStatus tracks the health and connectivity state of an MCP connection.
type ConnectionStatus string

const (
	StatusConnected    ConnectionStatus = "CONNECTED"
	StatusDisconnected ConnectionStatus = "DISCONNECTED"
	StatusFailed       ConnectionStatus = "FAILED"
)

var (
	ErrConnectionNotFound    = errors.New("mcp connection not found")
	ErrConnectionUnavailable = errors.New("mcp connection unavailable or unhealthy")
	ErrInvalidEndpoint       = errors.New("invalid mcp server endpoint")
	ErrUnsupportedTransport  = errors.New("unsupported mcp transport type")
	ErrSchemaMismatch        = errors.New("mcp server tool schema does not match registered tool definition")
	ErrOversizedResponse     = errors.New("mcp response exceeds maximum allowed size of 64KB")
	ErrServerSideError       = errors.New("mcp server returned an error")
	ErrSSRFBlocked           = errors.New("mcp server endpoint blocked by security policy (SSRF prevention)")
	ErrToolNotFound          = errors.New("mcp tool not found on server")
)

// ConnectionDefinition defines the tenant-scoped configuration for an external or in-process MCP server.
type ConnectionDefinition struct {
	ID               string           `json:"id"`
	TenantID         uuid.UUID        `json:"tenant_id"`
	ServerName       string           `json:"server_name"`
	Endpoint         string           `json:"endpoint"`
	Transport        TransportType    `json:"transport"`
	CredentialRef    string           `json:"credential_ref,omitempty"`
	Status           ConnectionStatus `json:"status"`
	Timeout          time.Duration    `json:"timeout"`
	MaxResponseBytes int              `json:"max_response_bytes"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

// Validate ensures required fields are set and well-formed.
func (c ConnectionDefinition) Validate() error {
	if c.ID == "" {
		return errors.New("connection ID is required")
	}
	if c.TenantID == uuid.Nil {
		return errors.New("tenant ID is required")
	}
	if c.ServerName == "" {
		return errors.New("server name is required")
	}
	switch c.Transport {
	case TransportInProcess, TransportSSE, TransportStreamableHTTP:
		// Valid
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedTransport, c.Transport)
	}
	if c.Transport != TransportInProcess && c.Endpoint == "" {
		return fmt.Errorf("%w: endpoint is required for network transport", ErrInvalidEndpoint)
	}
	return nil
}

// CallToolOptions specifies parameters for dispatching an invocation to an MCP tool.
type CallToolOptions struct {
	TenantID       uuid.UUID
	AgentID        uuid.UUID
	AgentVersionID uuid.UUID
	ExecutionID    string
	InvocationID   string
	ConnectionID   string
	ToolName       string
	Arguments      map[string]any
}
