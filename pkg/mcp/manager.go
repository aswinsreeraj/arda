package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/aswinsreeraj/arda/pkg/mcp/simulator"
	"github.com/aswinsreeraj/arda/pkg/toolgateway"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	DefaultTimeout          = 10 * time.Second
	DefaultMaxResponseBytes = 64 * 1024 // 64KB
)

// ManagerConfig configures the MCP Connection Manager.
type ManagerConfig struct {
	CredentialResolver CredentialResolver
	AllowLocalhost     bool
}

// ConnectionManager manages tenant-scoped MCP connections, tool discovery, and tool dispatch.
type ConnectionManager struct {
	mu               sync.RWMutex
	connections      map[string]ConnectionDefinition
	inProcessServers map[string]*server.MCPServer
	clientCache      map[string]*client.Client
	credResolver     CredentialResolver
	allowLocalhost   bool
}

// NewConnectionManager initializes an MCP connection manager.
func NewConnectionManager(cfg ManagerConfig) *ConnectionManager {
	return &ConnectionManager{
		connections:      make(map[string]ConnectionDefinition),
		inProcessServers: make(map[string]*server.MCPServer),
		clientCache:      make(map[string]*client.Client),
		credResolver:     cfg.CredentialResolver,
		allowLocalhost:   cfg.AllowLocalhost,
	}
}

// RegisterConnection registers a tenant-scoped MCP connection definition.
func (m *ConnectionManager) RegisterConnection(ctx context.Context, conn ConnectionDefinition) (*ConnectionDefinition, error) {
	if err := conn.Validate(); err != nil {
		return nil, err
	}

	if conn.Transport != TransportInProcess {
		if err := ValidateEndpoint(conn.Endpoint, m.allowLocalhost); err != nil {
			return nil, err
		}
	}

	if conn.Timeout <= 0 {
		conn.Timeout = DefaultTimeout
	}
	if conn.MaxResponseBytes <= 0 {
		conn.MaxResponseBytes = DefaultMaxResponseBytes
	}

	now := time.Now().UTC()
	if conn.CreatedAt.IsZero() {
		conn.CreatedAt = now
	}
	conn.UpdatedAt = now
	conn.Status = StatusConnected

	m.mu.Lock()
	defer m.mu.Unlock()

	// Enforce uniqueness per tenant and ID
	key := fmt.Sprintf("%s:%s", conn.TenantID, conn.ID)
	m.connections[key] = conn
	return &conn, nil
}

// RegisterInProcessServer registers an in-memory MCP server directly for a connection.
func (m *ConnectionManager) RegisterInProcessServer(connID string, tenantID uuid.UUID, srv *server.MCPServer) error {
	if srv == nil {
		return errors.New("server cannot be nil")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s:%s", tenantID, connID)
	existing, ok := m.connections[key]
	if ok {
		existing.Transport = TransportInProcess
		existing.Status = StatusConnected
		existing.UpdatedAt = time.Now().UTC()
		m.connections[key] = existing
	} else {
		conn := ConnectionDefinition{
			ID:               connID,
			TenantID:         tenantID,
			ServerName:       "inprocess-server",
			Transport:        TransportInProcess,
			Status:           StatusConnected,
			Timeout:          DefaultTimeout,
			MaxResponseBytes: DefaultMaxResponseBytes,
			CreatedAt:        time.Now().UTC(),
			UpdatedAt:        time.Now().UTC(),
		}
		m.connections[key] = conn
	}
	m.inProcessServers[key] = srv
	return nil
}

// GetConnection retrieves an active connection definition scoped strictly to the tenant.
func (m *ConnectionManager) GetConnection(ctx context.Context, tenantID uuid.UUID, id string) (*ConnectionDefinition, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s:%s", tenantID, id)
	conn, ok := m.connections[key]
	if !ok || conn.TenantID != tenantID {
		return nil, ErrConnectionNotFound
	}
	c := conn
	return &c, nil
}

// ListConnections lists all connections for an authenticated tenant.
func (m *ConnectionManager) ListConnections(ctx context.Context, tenantID uuid.UUID) ([]ConnectionDefinition, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var res []ConnectionDefinition
	for _, conn := range m.connections {
		if conn.TenantID == tenantID {
			res = append(res, conn)
		}
	}
	return res, nil
}

// CloseConnection shuts down an active client and closes the connection.
func (m *ConnectionManager) CloseConnection(ctx context.Context, tenantID uuid.UUID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s:%s", tenantID, id)
	conn, ok := m.connections[key]
	if !ok || conn.TenantID != tenantID {
		return ErrConnectionNotFound
	}

	if cli, exists := m.clientCache[key]; exists {
		_ = cli.Close()
		delete(m.clientCache, key)
	}

	conn.Status = StatusDisconnected
	conn.UpdatedAt = time.Now().UTC()
	m.connections[key] = conn
	return nil
}

// getOrCreateClient resolves or initializes an MCP client for the connection.
func (m *ConnectionManager) getOrCreateClient(ctx context.Context, conn ConnectionDefinition) (*client.Client, error) {
	key := fmt.Sprintf("%s:%s", conn.TenantID, conn.ID)

	m.mu.RLock()
	cachedClient, ok := m.clientCache[key]
	inProcServer := m.inProcessServers[key]
	m.mu.RUnlock()

	if ok && cachedClient != nil {
		return cachedClient, nil
	}

	var cli *client.Client
	var err error

	switch conn.Transport {
	case TransportInProcess:
		if inProcServer == nil {
			return nil, fmt.Errorf("%w: in-process server not registered for connection %s", ErrConnectionUnavailable, conn.ID)
		}
		cli, err = client.NewInProcessClient(inProcServer)
		if err != nil {
			return nil, fmt.Errorf("failed to create in-process mcp client: %w", err)
		}

	case TransportSSE, TransportStreamableHTTP:
		// Resolve credentials if configured
		var token string
		if conn.CredentialRef != "" && m.credResolver != nil {
			tok, credErr := m.credResolver.Resolve(ctx, conn.TenantID, conn.CredentialRef)
			if credErr != nil {
				return nil, credErr
			}
			token = tok
		}

		var opts []transport.ClientOption
		if token != "" {
			opts = append(opts, client.WithHeaders(map[string]string{
				"Authorization": "Bearer " + token,
			}))
		}

		if conn.Transport == TransportSSE {
			cli, err = client.NewSSEMCPClient(conn.Endpoint, opts...)
		} else {
			cli, err = client.NewStreamableHttpClient(conn.Endpoint)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to initialize network mcp client: %w", err)
		}

	default:
		return nil, ErrUnsupportedTransport
	}

	// Initialize the MCP protocol handshake
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "arda-tool-gateway",
		Version: "1.0.0",
	}

	initCtx, cancel := context.WithTimeout(ctx, conn.Timeout)
	defer cancel()

	if err := cli.Start(initCtx); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("%w: failed to start mcp client: %v", ErrConnectionUnavailable, err)
	}

	if _, err := cli.Initialize(initCtx, initReq); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("%w: failed to initialize mcp handshake: %v", ErrConnectionUnavailable, err)
	}

	m.mu.Lock()
	m.clientCache[key] = cli
	m.mu.Unlock()

	return cli, nil
}

// DiscoverTools queries the connected MCP server for available tools.
func (m *ConnectionManager) DiscoverTools(ctx context.Context, tenantID uuid.UUID, connectionID string) ([]mcp.Tool, error) {
	conn, err := m.GetConnection(ctx, tenantID, connectionID)
	if err != nil {
		return nil, err
	}

	cli, err := m.getOrCreateClient(ctx, *conn)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, conn.Timeout)
	defer cancel()

	res, err := cli.ListTools(callCtx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list tools from mcp server: %w", err)
	}

	return res.Tools, nil
}

// ReconcileTool verifies that a registered tool definition matches the discovered server tool schema.
// Returns ErrSchemaMismatch if the remote schema has changed unexpectedly.
func (m *ConnectionManager) ReconcileTool(registered toolgateway.ToolDefinition, discovered mcp.Tool) error {
	if registered.Name != discovered.Name {
		return fmt.Errorf("%w: name mismatch (registered: %s, discovered: %s)", ErrSchemaMismatch, registered.Name, discovered.Name)
	}

	// Normalize schemas to JSON maps for deep semantic comparison
	regBytes, err := json.Marshal(registered.InputSchema)
	if err != nil {
		return fmt.Errorf("invalid registered input schema: %w", err)
	}

	discBytes, err := json.Marshal(discovered.InputSchema)
	if err != nil {
		return fmt.Errorf("invalid discovered input schema: %w", err)
	}

	var regMap, discMap map[string]any
	if err := json.Unmarshal(regBytes, &regMap); err != nil {
		return err
	}
	if err := json.Unmarshal(discBytes, &discMap); err != nil {
		return err
	}

	if !reflect.DeepEqual(regMap, discMap) {
		return fmt.Errorf("%w: schema drift detected for tool %q", ErrSchemaMismatch, registered.Name)
	}

	return nil
}

// CallTool dispatches an authorized tool call to the MCP server.
func (m *ConnectionManager) CallTool(ctx context.Context, opts CallToolOptions) (map[string]any, error) {
	conn, err := m.GetConnection(ctx, opts.TenantID, opts.ConnectionID)
	if err != nil {
		return nil, err
	}

	cli, err := m.getOrCreateClient(ctx, *conn)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, conn.Timeout)
	defer cancel()

	// Inject trusted tenant ID into context for in-process dispatch
	callCtx = simulator.WithTenantID(callCtx, opts.TenantID)

	args := opts.Arguments
	if args == nil {
		args = make(map[string]any)
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = opts.ToolName
	req.Params.Arguments = args
	if opts.InvocationID != "" {
		req.Params.Meta = mcp.NewMetaFromMap(map[string]any{
			"invocation_id": opts.InvocationID,
			"execution_id":  opts.ExecutionID,
		})
	}

	res, err := cli.CallTool(callCtx, req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("mcp tool call timed out after %v", conn.Timeout)
		}
		if errors.Is(err, context.Canceled) || errors.Is(callCtx.Err(), context.Canceled) {
			return nil, context.Canceled
		}
		return nil, fmt.Errorf("mcp transport error: %w", err)
	}

	// Check if the tool reported an execution error
	if res.IsError {
		var errorMsgs []string
		for _, content := range res.Content {
			if textContent, ok := mcp.AsTextContent(content); ok {
				errorMsgs = append(errorMsgs, textContent.Text)
			}
		}
		errMsg := strings.Join(errorMsgs, "; ")
		if errMsg == "" {
			errMsg = "tool returned isError=true"
		}
		return nil, fmt.Errorf("%w: %s", ErrServerSideError, errMsg)
	}

	// Extract result payload
	outputMap := make(map[string]any)
	for _, content := range res.Content {
		if textContent, ok := mcp.AsTextContent(content); ok {
			var parsed map[string]any
			if json.Unmarshal([]byte(textContent.Text), &parsed) == nil {
				for k, v := range parsed {
					outputMap[k] = v
				}
			} else {
				outputMap["content"] = textContent.Text
			}
		}
	}

	// Enforce bounded response size (64KB)
	rawBytes, err := json.Marshal(outputMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal mcp output: %w", err)
	}

	if len(rawBytes) > conn.MaxResponseBytes {
		return nil, fmt.Errorf("%w: response length %d exceeds maximum of %d bytes", ErrOversizedResponse, len(rawBytes), conn.MaxResponseBytes)
	}

	return outputMap, nil
}
