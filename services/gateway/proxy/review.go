package proxy

import (
	"errors"
	"fmt"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ErrReviewUnconfigured is returned when no upstream address is configured for the review service.
var ErrReviewUnconfigured = errors.New("review service upstream address is not configured")

// ReviewClient wraps ardav1.ReviewServiceClient and manages gRPC connection lifecycle.
type ReviewClient struct {
	client ardav1.ReviewServiceClient
	conn   *grpc.ClientConn
}

// NewReviewClient connects to the Human Review gRPC service at the given address.
func NewReviewClient(addr string) (*ReviewClient, error) {
	if addr == "" {
		return nil, ErrReviewUnconfigured
	}

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to dial review service at %s: %w", addr, err)
	}

	return &ReviewClient{
		client: ardav1.NewReviewServiceClient(conn),
		conn:   conn,
	}, nil
}

// NewReviewClientFromConn creates a client from an existing connection (e.g. bufconn or in-process).
func NewReviewClientFromConn(conn *grpc.ClientConn) *ReviewClient {
	return &ReviewClient{
		client: ardav1.NewReviewServiceClient(conn),
		conn:   conn,
	}
}

// Client returns the underlying ardav1.ReviewServiceClient.
func (c *ReviewClient) Client() ardav1.ReviewServiceClient {
	if c == nil {
		return nil
	}
	return c.client
}

// Close closes the underlying gRPC connection.
func (c *ReviewClient) Close() error {
	if c != nil && c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
