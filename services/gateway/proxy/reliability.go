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

// ErrUnconfigured is returned when no upstream address is configured for the reliability service.
var ErrUnconfigured = errors.New("reliability service upstream address is not configured")

// ReliabilityService defines the client interface for evaluating reliability gates.
type ReliabilityService interface {
	Gate(ctx context.Context, req *ardav1.GateRequest) (*ardav1.GateResponse, error)
	Close() error
}

// GRPCClient connects to the upstream reliability service via gRPC.
type GRPCClient struct {
	client ardav1.ReliabilityServiceClient
	conn   *grpc.ClientConn
}

// NewReliabilityClient connects to the reliability service at the given address.
func NewReliabilityClient(addr string) (*GRPCClient, error) {
	if addr == "" {
		return nil, ErrUnconfigured
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to dial reliability service at %s: %w", addr, err)
	}

	return &GRPCClient{
		client: ardav1.NewReliabilityServiceClient(conn),
		conn:   conn,
	}, nil
}

// Gate evaluates whether an agent's proposed decision meets the calibrated reliability threshold.
func (c *GRPCClient) Gate(ctx context.Context, req *ardav1.GateRequest) (*ardav1.GateResponse, error) {
	if c == nil || c.client == nil {
		return nil, ErrUnconfigured
	}
	return c.client.Gate(ctx, req)
}

// Close closes the underlying gRPC connection.
func (c *GRPCClient) Close() error {
	if c != nil && c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
