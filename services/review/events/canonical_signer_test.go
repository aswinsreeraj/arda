package events

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalSignerPythonInterop(t *testing.T) {
	agentID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	tenantID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	secret := "arda-producer-secret-default"

	ev := &ReviewResolvedEvent{
		EventID:                 "evt_test_456",
		EventType:               EventTypeReviewResolved,
		EventVersion:            "v1",
		TenantID:                tenantID,
		AgentID:                 agentID,
		CaseID:                  "case_test_123",
		OriginalCaseDescription: "Test narrative",
		ProposedDecision:        "APPROVE",
		HumanDecision:           "APPROVE",
		ReviewerID:              "reviewer_alice",
		Timestamp:               "2026-11-13T12:00:00Z",
		ResolvedAt:              time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC),
		ProducerID:              "arda.human-review-service",
		KeyID:                   "v1",
	}

	err := SignResolutionEvent(ev, secret)
	require.NoError(t, err)

	expectedSig := "4922f1edb0ccfd646cfdea8681f89e50b272d786b845a030ecff47b96dbdcd1c"
	assert.Equal(t, expectedSig, ev.Signature, "Go signature must match Python authorizer signature exactly")
}
