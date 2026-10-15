package ardav1_test

import (
	"testing"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProtobufContracts(t *testing.T) {
	t.Run("common.proto descriptors and messages", func(t *testing.T) {
		req := &ardav1.PaginationRequest{
			PageSize:  20,
			PageToken: "token-123",
		}
		data, err := proto.Marshal(req)
		if err != nil {
			t.Fatalf("failed to marshal PaginationRequest: %v", err)
		}
		var unmarshaledReq ardav1.PaginationRequest
		if err := proto.Unmarshal(data, &unmarshaledReq); err != nil {
			t.Fatalf("failed to unmarshal PaginationRequest: %v", err)
		}
		if unmarshaledReq.PageSize != 20 || unmarshaledReq.PageToken != "token-123" {
			t.Errorf("unexpected unmarshaled pagination: got %+v", &unmarshaledReq)
		}

		errDetail := &ardav1.ErrorDetail{
			Code:    "VALIDATION_FAILED",
			Message: "invalid prompt",
			Metadata: map[string]string{
				"field": "prompt_template",
			},
		}
		if _, err := proto.Marshal(errDetail); err != nil {
			t.Fatalf("failed to marshal ErrorDetail: %v", err)
		}
	})

	t.Run("tenant.proto descriptors and messages", func(t *testing.T) {
		now := timestamppb.Now()
		tenant := &ardav1.Tenant{
			Id:        "a0000000-0000-0000-0000-000000000001",
			Name:      "Acme Corp",
			Status:    ardav1.TenantStatus_TENANT_STATUS_ACTIVE,
			CreatedAt: now,
			UpdatedAt: now,
		}
		data, err := proto.Marshal(tenant)
		if err != nil {
			t.Fatalf("failed to marshal Tenant: %v", err)
		}
		var unmarshaledTenant ardav1.Tenant
		if err := proto.Unmarshal(data, &unmarshaledTenant); err != nil {
			t.Fatalf("failed to unmarshal Tenant: %v", err)
		}
		if unmarshaledTenant.Status != ardav1.TenantStatus_TENANT_STATUS_ACTIVE {
			t.Errorf("unexpected tenant status: got %v", unmarshaledTenant.Status)
		}

		ctxMsg := &ardav1.TenantContext{
			TenantId:   "a0000000-0000-0000-0000-000000000001",
			TenantName: "Acme Corp",
		}
		if _, err := proto.Marshal(ctxMsg); err != nil {
			t.Fatalf("failed to marshal TenantContext: %v", err)
		}
	})

	t.Run("auth.proto descriptors and roles", func(t *testing.T) {
		roles := []ardav1.Role{
			ardav1.Role_ROLE_SUPER_ADMIN,
			ardav1.Role_ROLE_COMPANY_ADMIN,
			ardav1.Role_ROLE_AGENT_DEVELOPER,
			ardav1.Role_ROLE_REVIEWER,
		}
		for _, role := range roles {
			ident := &ardav1.UserIdentity{
				UserId:   "u-123",
				TenantId: "t-456",
				Role:     role,
				Email:    "user@example.com",
			}
			data, err := proto.Marshal(ident)
			if err != nil {
				t.Fatalf("failed to marshal UserIdentity with role %v: %v", role, err)
			}
			var unmarshaled ardav1.UserIdentity
			if err := proto.Unmarshal(data, &unmarshaled); err != nil {
				t.Fatalf("failed to unmarshal UserIdentity: %v", err)
			}
			if unmarshaled.Role != role {
				t.Errorf("role mismatch: got %v, want %v", unmarshaled.Role, role)
			}
		}
	})

	t.Run("agent.proto descriptors and messages", func(t *testing.T) {
		guardrails, err := structpb.NewStruct(map[string]any{
			"max_tokens": float64(1000),
		})
		if err != nil {
			t.Fatalf("failed to create guardrails struct: %v", err)
		}

		cfg := &ardav1.AgentConfiguration{
			PromptTemplate:       "Answer clearly",
			Tools:                []string{"calculator", "search"},
			ReliabilityThreshold: 0.85,
			ExecutionMode:        ardav1.ExecutionMode_EXECUTION_MODE_GATED,
			Guardrails:           guardrails,
		}

		agent := &ardav1.Agent{
			Id:        "ag-1",
			TenantId:  "t-1",
			Name:      "Customer Support Agent",
			CreatedAt: timestamppb.Now(),
			UpdatedAt: timestamppb.Now(),
		}

		version := &ardav1.AgentVersion{
			Id:            "v-1",
			AgentId:       agent.Id,
			VersionNumber: 1,
			Configuration: cfg,
			Published:     true,
			CreatedAt:     timestamppb.Now(),
			UpdatedAt:     timestamppb.Now(),
			PublishedAt:   timestamppb.Now(),
		}

		detail := &ardav1.AgentDetail{
			Agent:                  agent,
			LatestPublishedVersion: version,
		}

		data, err := proto.Marshal(detail)
		if err != nil {
			t.Fatalf("failed to marshal AgentDetail: %v", err)
		}

		var unmarshaled ardav1.AgentDetail
		if err := proto.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("failed to unmarshal AgentDetail: %v", err)
		}
		if unmarshaled.Agent.Name != "Customer Support Agent" {
			t.Errorf("agent name mismatch: got %s", unmarshaled.Agent.Name)
		}
		if unmarshaled.LatestPublishedVersion.Configuration.ExecutionMode != ardav1.ExecutionMode_EXECUTION_MODE_GATED {
			t.Errorf("execution mode mismatch: got %v", unmarshaled.LatestPublishedVersion.Configuration.ExecutionMode)
		}
	})

	t.Run("reliability.proto compatibility", func(t *testing.T) {
		gateReq := &ardav1.GateRequest{
			TenantId:             "t-1",
			AgentId:              "ag-1",
			CaseDescription:      "Credit limit increase",
			ProposedDecision:     "APPROVE",
			ReliabilityThreshold: 0.9,
		}
		gateResp := &ardav1.GateResponse{
			Confidence: 0.95,
			Outcome:    ardav1.GateOutcome_GATE_OUTCOME_PASS,
			ReasonCode: "HIGH_CONFIDENCE",
			Neighbors: []*ardav1.ReferenceNeighbor{
				{ReferenceExampleId: "ex-1", Label: "APPROVE", Similarity: 0.98},
			},
		}

		dataReq, err := proto.Marshal(gateReq)
		if err != nil {
			t.Fatalf("failed to marshal GateRequest: %v", err)
		}
		var unmarshaledReq ardav1.GateRequest
		if err := proto.Unmarshal(dataReq, &unmarshaledReq); err != nil {
			t.Fatalf("failed to unmarshal GateRequest: %v", err)
		}

		dataResp, err := proto.Marshal(gateResp)
		if err != nil {
			t.Fatalf("failed to marshal GateResponse: %v", err)
		}
		var unmarshaledResp ardav1.GateResponse
		if err := proto.Unmarshal(dataResp, &unmarshaledResp); err != nil {
			t.Fatalf("failed to unmarshal GateResponse: %v", err)
		}
		if unmarshaledResp.Outcome != ardav1.GateOutcome_GATE_OUTCOME_PASS {
			t.Errorf("gate outcome mismatch: got %v", unmarshaledResp.Outcome)
		}
	})
}
