package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/pkg/agentruntime"
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

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
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

// RuntimeAdapter adapts a ReliabilityService client to agentruntime.ReliabilityService.
type RuntimeAdapter struct {
	client ReliabilityService
}

// NewRuntimeAdapter constructs an adapter for agentruntime.
func NewRuntimeAdapter(client ReliabilityService) *RuntimeAdapter {
	return &RuntimeAdapter{client: client}
}

// Evaluate evaluates an agent decision using the upstream gRPC Reliability Service.
func (a *RuntimeAdapter) Evaluate(ctx context.Context, req agentruntime.ReliabilityRequest) (agentruntime.ReliabilityEvaluation, error) {
	if a == nil || a.client == nil {
		return agentruntime.ReliabilityEvaluation{}, ErrUnconfigured
	}

	caseDescription := strings.TrimSpace(req.CaseDescription)
	if caseDescription == "" {
		caseDescription = strings.TrimSpace(req.ProposedDecision)
	}
	if caseDescription == "" {
		caseDescription = "Agent execution decision"
	}

	proposedDecision := normalizeProposedDecision(req.ProposedDecision)

	grpcReq := &ardav1.GateRequest{
		TenantId:             req.TenantID.String(),
		AgentId:              req.AgentID.String(),
		ProposedDecision:     proposedDecision,
		CaseDescription:      caseDescription,
		ReliabilityThreshold: req.ReliabilityThreshold,
	}

	resp, err := a.client.Gate(ctx, grpcReq)
	if err != nil {
		return agentruntime.ReliabilityEvaluation{}, err
	}

	decision := agentruntime.ReliabilityAllow
	if resp.Outcome == ardav1.GateOutcome_GATE_OUTCOME_REVIEW_REQUIRED {
		decision = agentruntime.ReliabilityEscalate
	}

	var calibVer string
	if resp.CalibrationVersion != nil {
		calibVer = *resp.CalibrationVersion
	}

	return agentruntime.ReliabilityEvaluation{
		Decision:             decision,
		Confidence:           resp.Confidence,
		ReasonCode:           resp.ReasonCode,
		RawScore:             resp.RawScore,
		CalibratedConfidence: resp.CalibratedConfidence,
		IsCalibrated:         resp.IsCalibrated,
		CalibrationVersion:   calibVer,
	}, nil
}

// normalizeProposedDecision extracts a canonical Reliability Service decision ("APPROVE" or "REJECT")
// from raw LLM output (which may be plain text, JSON, or markdown-fenced JSON).
func normalizeProposedDecision(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) >= 2 && strings.HasPrefix(lines[0], "```") {
			trimmed = strings.Join(lines[1:], "\n")
		}
		if idx := strings.LastIndex(trimmed, "```"); idx != -1 {
			trimmed = trimmed[:idx]
		}
		trimmed = strings.TrimSpace(trimmed)
	}

	if strings.HasPrefix(trimmed, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(trimmed), &m); err == nil {
			for _, key := range []string{"decision", "proposed_decision", "verdict", "outcome", "action"} {
				if val, ok := m[key].(string); ok {
					upper := strings.ToUpper(strings.TrimSpace(val))
					switch upper {
					case "APPROVE", "APPROVED", "ALLOW":
						return "APPROVE"
					case "REJECT", "REJECTED", "DENY", "DENIED", "BLOCK":
						return "REJECT"
					}
				}
			}
		}
	}

	upper := strings.ToUpper(trimmed)
	if upper == "APPROVE" || upper == "APPROVED" {
		return "APPROVE"
	}
	if upper == "REJECT" || upper == "REJECTED" || upper == "DENY" || upper == "DENIED" {
		return "REJECT"
	}
	hasReject := strings.Contains(upper, "REJECT") || strings.Contains(upper, "DENY")
	hasApprove := strings.Contains(upper, "APPROVE")
	if hasReject && !hasApprove {
		return "REJECT"
	}
	return "APPROVE"
}
