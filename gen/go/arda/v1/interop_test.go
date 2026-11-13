package ardav1_test

import (
	"context"
	"os"
	"testing"
	"time"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestReliabilityGateInterop(t *testing.T) {
	addr := os.Getenv("ARDA_RELIABILITY_ADDR")
	if addr == "" {
		t.Skip("Skipping live gRPC interop test: ARDA_RELIABILITY_ADDR environment variable not set")
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("failed to connect to reliability service at %s: %v", addr, err)
	}
	defer conn.Close()

	client := ardav1.NewReliabilityServiceClient(conn)

	t.Run("cold start returns GATE_OUTCOME_REVIEW_REQUIRED", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		req := &ardav1.GateRequest{
			TenantId:             uuid.New().String(),
			AgentId:              uuid.New().String(),
			CaseDescription:      "Dispute claim with zero prior precedents in cold tenant partition.",
			ProposedDecision:     "APPROVE",
			ReliabilityThreshold: 0.50,
		}

		resp, err := client.Gate(ctx, req)
		if err != nil {
			t.Fatalf("Gate RPC call failed: %v", err)
		}

		if resp.Outcome != ardav1.GateOutcome_GATE_OUTCOME_REVIEW_REQUIRED {
			t.Errorf("expected outcome GATE_OUTCOME_REVIEW_REQUIRED, got %v", resp.Outcome)
		}
		if resp.Confidence != 0.0 {
			t.Errorf("expected confidence 0.0, got %f", resp.Confidence)
		}
		if resp.GetRawScore() != 0.0 {
			t.Errorf("expected raw_score 0.0, got %f", resp.GetRawScore())
		}
		if resp.GetIsCalibrated() != false {
			t.Errorf("expected is_calibrated false, got true")
		}
		if resp.CalibratedConfidence != nil {
			t.Errorf("expected calibrated_confidence nil, got %v", resp.CalibratedConfidence)
		}
		if resp.ReasonCode != "COLD_START_INSUFFICIENT_EVIDENCE" {
			t.Errorf("expected reason code COLD_START_INSUFFICIENT_EVIDENCE, got %s", resp.ReasonCode)
		}
		if len(resp.Neighbors) != 0 {
			t.Errorf("expected 0 neighbors for cold start, got %d", len(resp.Neighbors))
		}
	})

	t.Run("seeded evidence returns GATE_OUTCOME_PASS", func(t *testing.T) {
		seededTenant := os.Getenv("ARDA_SEEDED_TENANT_ID")
		seededAgent := os.Getenv("ARDA_SEEDED_AGENT_ID")
		if seededTenant == "" || seededAgent == "" {
			t.Skip("Skipping seeded evidence test: ARDA_SEEDED_TENANT_ID / ARDA_SEEDED_AGENT_ID not set")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		req := &ardav1.GateRequest{
			TenantId:             seededTenant,
			AgentId:              seededAgent,
			CaseDescription:      "Customer filed dispute within 20 days. Cardholder history is clean with high tenure. Low chargeback ratio and no merchant dispute flags.",
			ProposedDecision:     "APPROVE",
			ReliabilityThreshold: 0.50,
		}

		resp, err := client.Gate(ctx, req)
		if err != nil {
			t.Fatalf("Gate RPC call failed: %v", err)
		}

		if resp.Outcome != ardav1.GateOutcome_GATE_OUTCOME_PASS {
			t.Errorf("expected outcome GATE_OUTCOME_PASS, got %v", resp.Outcome)
		}
		if resp.Confidence < 0.50 {
			t.Errorf("expected confidence >= 0.50, got %f", resp.Confidence)
		}
		if resp.GetRawScore() < 0.50 {
			t.Errorf("expected raw_score >= 0.50, got %f", resp.GetRawScore())
		}
		if resp.Confidence != resp.GetRawScore() {
			t.Errorf("expected confidence == raw_score (%f != %f)", resp.Confidence, resp.GetRawScore())
		}
		if resp.GetIsCalibrated() != false {
			t.Errorf("expected is_calibrated false in uncalibrated mode, got true")
		}
		if resp.CalibratedConfidence != nil {
			t.Errorf("expected calibrated_confidence nil in uncalibrated mode, got %v", resp.CalibratedConfidence)
		}
		if resp.ReasonCode != "RELIABILITY_THRESHOLD_MET" {
			t.Errorf("expected reason code RELIABILITY_THRESHOLD_MET, got %s", resp.ReasonCode)
		}
		if len(resp.Neighbors) == 0 {
			t.Errorf("expected neighbors to be populated, got 0")
		}
		for i, n := range resp.Neighbors {
			if n.ReferenceExampleId == "" {
				t.Errorf("neighbor %d missing ReferenceExampleId", i)
			}
			if n.Label != "APPROVE" {
				t.Errorf("neighbor %d unexpected label %s", i, n.Label)
			}
			if n.Similarity <= 0.0 {
				t.Errorf("neighbor %d invalid similarity %f", i, n.Similarity)
			}
		}
	})

	t.Run("invalid tenant_id returns InvalidArgument", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		req := &ardav1.GateRequest{
			TenantId:         "invalid-non-uuid",
			AgentId:          uuid.New().String(),
			CaseDescription:  "Valid case description text",
			ProposedDecision: "APPROVE",
		}

		_, err := client.Gate(ctx, req)
		if err == nil {
			t.Fatal("expected error for invalid tenant_id, got nil")
		}

		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("expected gRPC status error, got %v", err)
		}
		if st.Code() != codes.InvalidArgument {
			t.Errorf("expected status code InvalidArgument, got %v", st.Code())
		}
	})

	t.Run("invalid proposed_decision returns InvalidArgument", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		req := &ardav1.GateRequest{
			TenantId:         uuid.New().String(),
			AgentId:          uuid.New().String(),
			CaseDescription:  "Valid case description text",
			ProposedDecision: "INVALID_ACTION",
		}

		_, err := client.Gate(ctx, req)
		if err == nil {
			t.Fatal("expected error for invalid proposed_decision, got nil")
		}

		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("expected gRPC status error, got %v", err)
		}
		if st.Code() != codes.InvalidArgument {
			t.Errorf("expected status code InvalidArgument, got %v", st.Code())
		}
	})
}
