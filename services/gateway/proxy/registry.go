package proxy

import (
	"context"
	"errors"
	"fmt"
	"time"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ErrRegistryUnconfigured is returned when no upstream address is configured for the registry service.
var ErrRegistryUnconfigured = errors.New("registry service upstream address is not configured")

// RegistryClient wraps the generated ardav1.AgentRegistryServiceClient and manages connection lifecycle.
type RegistryClient struct {
	client ardav1.AgentRegistryServiceClient
	conn   *grpc.ClientConn
}

// NewRegistryClient connects to the Agent Registry gRPC service at the given address.
func NewRegistryClient(addr string) (*RegistryClient, error) {
	if addr == "" {
		return nil, ErrRegistryUnconfigured
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to dial registry service at %s: %w", addr, err)
	}

	return &RegistryClient{
		client: ardav1.NewAgentRegistryServiceClient(conn),
		conn:   conn,
	}, nil
}

// NewRegistryClientFromConn creates a client from an existing connection (e.g. bufconn or in-process).
func NewRegistryClientFromConn(conn *grpc.ClientConn) *RegistryClient {
	return &RegistryClient{
		client: ardav1.NewAgentRegistryServiceClient(conn),
		conn:   conn,
	}
}

// Client returns the underlying ardav1.AgentRegistryServiceClient.
func (c *RegistryClient) Client() ardav1.AgentRegistryServiceClient {
	if c == nil {
		return nil
	}
	return c.client
}

// Close closes the underlying gRPC connection.
func (c *RegistryClient) Close() error {
	if c != nil && c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
