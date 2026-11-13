package events

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
)

// FormatCanonicalTimestamp formats a timestamp into deterministic canonical RFC3339 UTC string with second precision (YYYY-MM-DDTHH:MM:SSZ).
func FormatCanonicalTimestamp(t time.Time) string {
	utc := t.UTC()
	return utc.Format("2006-01-02T15:04:05Z")
}

// BuildCanonicalPayload constructs the deterministic canonical UTF-8 JSON payload for HMAC-SHA256 signing.
// Keys must be strictly sorted lexicographically with compact separators (",", ":") and no trailing whitespace.
func BuildCanonicalPayload(
	agentID uuid.UUID,
	caseID string,
	eventID string,
	eventType string,
	eventVersion string,
	humanDecision string,
	keyID string,
	originalCaseDescription string,
	producerID string,
	proposedDecision string,
	reviewerID string,
	tenantID uuid.UUID,
	timestamp string,
) ([]byte, error) {
	if eventType == "" {
		eventType = EventTypeReviewResolved
	}
	if eventVersion == "" {
		eventVersion = EventVersionV1
	}
	if keyID == "" {
		keyID = DefaultKeyID
	}
	if producerID == "" {
		producerID = DefaultProducerID
	}

	canonicalMap := map[string]string{
		"agent_id":                  strings.ToLower(agentID.String()),
		"case_id":                   strings.TrimSpace(caseID),
		"event_id":                  strings.TrimSpace(eventID),
		"event_type":                strings.TrimSpace(eventType),
		"event_version":             strings.TrimSpace(eventVersion),
		"human_decision":            strings.TrimSpace(humanDecision),
		"key_id":                    strings.TrimSpace(keyID),
		"original_case_description": originalCaseDescription,
		"producer_id":               strings.TrimSpace(producerID),
		"proposed_decision":         strings.TrimSpace(proposedDecision),
		"reviewer_id":               strings.TrimSpace(reviewerID),
		"tenant_id":                 strings.ToLower(tenantID.String()),
		"timestamp":                 strings.TrimSpace(timestamp),
	}

	return json.Marshal(canonicalMap)
}

// ComputeHMACSignature calculates the HMAC-SHA256 lowercase hex signature over canonical payload bytes.
func ComputeHMACSignature(secret string, canonicalPayload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(canonicalPayload)
	return hex.EncodeToString(mac.Sum(nil))
}

// SignResolutionEvent computes and attaches the canonical HMAC-SHA256 signature to a ReviewResolvedEvent.
func SignResolutionEvent(ev *ReviewResolvedEvent, secret string) error {
	if ev.Timestamp == "" {
		ev.Timestamp = FormatCanonicalTimestamp(ev.ResolvedAt)
	}
	if ev.ProducerID == "" {
		ev.ProducerID = DefaultProducerID
	}
	if ev.KeyID == "" {
		ev.KeyID = DefaultKeyID
	}
	if ev.EventVersion == "" {
		ev.EventVersion = EventVersionV1
	}
	if ev.EventType == "" {
		ev.EventType = EventTypeReviewResolved
	}

	payload, err := BuildCanonicalPayload(
		ev.AgentID,
		ev.CaseID,
		ev.EventID,
		ev.EventType,
		ev.EventVersion,
		ev.HumanDecision,
		ev.KeyID,
		ev.OriginalCaseDescription,
		ev.ProducerID,
		ev.ProposedDecision,
		ev.ReviewerID,
		ev.TenantID,
		ev.Timestamp,
	)
	if err != nil {
		return err
	}

	ev.Signature = ComputeHMACSignature(secret, payload)
	return nil
}
