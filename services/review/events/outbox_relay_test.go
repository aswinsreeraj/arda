package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/services/review/events"
	"github.com/aswinsreeraj/arda/services/review/store"
	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutboxRelay_Flush_Success(t *testing.T) {
	ctx := context.Background()
	memStore := store.NewMemoryStore()
	mockProd := events.NewMockProducer()

	tenantID := uuid.New()
	eventID := uuid.New().String()

	// Seed outbox event
	outboxEv := &store.OutboxEventModel{
		ID:        eventID,
		TenantID:  tenantID,
		EventType: events.EventTypeReviewResolved,
		Topic:     events.TopicReviewResolved,
		Payload: map[string]any{
			"event_id": eventID,
			"status":   "RESOLVED",
		},
		Headers: map[string]string{
			"x-arda-signature": "sig_abc",
			"x-arda-key-id":    "v1",
		},
		Status:    "PENDING",
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, memStore.SaveOutboxEvent(ctx, outboxEv))

	relay := events.NewOutboxRelay(memStore, mockProd, events.WithBatchSize(10))

	flushed, err := relay.Flush(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)

	// Verify published via producer
	published := mockProd.GetPublished()
	require.Len(t, published, 1)
	pub := published[0]

	assert.Equal(t, events.TopicReviewResolved, pub.Topic)
	assert.Equal(t, tenantID.String(), pub.Key)
	assert.Equal(t, "sig_abc", pub.Headers["x-arda-signature"])

	// Verify outbox record marked published in store
	eventsPending, err := memStore.FetchPendingOutboxEvents(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, eventsPending)
}

func TestOutboxRelay_Flush_ProducerErrorMarksFailed(t *testing.T) {
	ctx := context.Background()
	memStore := store.NewMemoryStore()
	mockProd := events.NewMockProducer()
	mockProd.SetPublishError(errors.New("kafka broker unavailable"))

	tenantID := uuid.New()
	eventID := uuid.New().String()

	outboxEv := &store.OutboxEventModel{
		ID:        eventID,
		TenantID:  tenantID,
		EventType: events.EventTypeReviewResolved,
		Topic:     events.TopicReviewResolved,
		Payload:   map[string]any{"data": "val"},
		Status:    "PENDING",
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, memStore.SaveOutboxEvent(ctx, outboxEv))

	relay := events.NewOutboxRelay(memStore, mockProd, events.WithBatchSize(10))

	flushed, err := relay.Flush(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, flushed)

	// Event should now have failure recorded
	rec, err := memStore.GetOutboxEvent(ctx, eventID)
	require.NoError(t, err)
	assert.Equal(t, "PENDING", rec.Status) // Still pending retry
	assert.Contains(t, rec.ErrorMessage, "kafka broker unavailable")
	assert.Equal(t, 1, rec.RetryCount)
}

func TestOutboxRelay_StartStopLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	memStore := store.NewMemoryStore()
	mockProd := events.NewMockProducer()

	relay := events.NewOutboxRelay(memStore, mockProd,
		events.WithPollInterval(20*time.Millisecond),
		events.WithBatchSize(5),
	)

	doneCh := make(chan struct{})
	go func() {
		relay.Start(ctx)
		close(doneCh)
	}()

	// Add an event while running
	tenantID := uuid.New()
	require.NoError(t, memStore.SaveOutboxEvent(context.Background(), &store.OutboxEventModel{
		ID:        uuid.New().String(),
		TenantID:  tenantID,
		EventType: events.EventTypeReviewCreated,
		Topic:     events.TopicReviewResolved,
		Payload:   map[string]any{"case_id": "case_async"},
		Status:    "PENDING",
		CreatedAt: time.Now().UTC(),
	}))

	// Wait for background poll to pick it up
	require.Eventually(t, func() bool {
		return len(mockProd.GetPublished()) == 1
	}, 1*time.Second, 20*time.Millisecond)

	// Cancel context to stop relay cleanly
	cancel()
	select {
	case <-doneCh:
		// stopped cleanly
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected relay to terminate upon context cancellation")
	}
}

// TestRedpanda_LivePublishAndConsume tests end-to-end publishing to live Redpanda
func TestRedpanda_LivePublishAndConsume(t *testing.T) {
	secret := "test-feedback-secret-key"
	producer, err := events.NewKafkaProducer("127.0.0.1:19092")
	require.NoError(t, err)
	defer producer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testCaseID := "case_live_" + uuid.New().String()[:8]
	tenantID := uuid.New()
	agentID := uuid.New()
	now := time.Now().UTC()

	eventPayload := events.ReviewResolvedEvent{
		EventType:               events.EventTypeReviewResolved,
		EventID:                 uuid.New().String(),
		EventVersion:            events.EventVersionV1,
		TenantID:                tenantID,
		AgentID:                 agentID,
		CaseID:                  testCaseID,
		ExecutionID:             uuid.New().String(),
		CorrelationID:           uuid.New().String(),
		OriginalCaseDescription: "Original prompt",
		ProposedDecision:        "APPROVE",
		HumanDecision:           "HOLD",
		FinalDecision:           "HOLD",
		DecisionType:            "override",
		ResolutionReason:        "Live verification integration test",
		ReviewerID:              "reviewer_live_test",
		ResolvedAt:              now,
		Timestamp:               events.FormatCanonicalTimestamp(now),
		ExecutionMode:           "realtime",
		ProducerID:              events.DefaultProducerID,
		KeyID:                   "v1",
	}

	err = events.SignResolutionEvent(&eventPayload, secret)
	require.NoError(t, err)

	payloadBytes, err := json.Marshal(eventPayload)
	require.NoError(t, err)

	headers := map[string]string{
		"x-arda-signature": eventPayload.Signature,
		"x-arda-key-id":    eventPayload.KeyID,
		"x-arda-topic":     events.TopicReviewResolved,
	}

	// 1. Publish to live Redpanda
	err = producer.Publish(ctx, events.TopicReviewResolved, eventPayload.TenantID.String(), payloadBytes, headers)
	if err != nil {
		t.Skipf("skipping live redpanda test: failed connecting: %v", err)
	}

	// 2. Consume from Redpanda to verify delivery
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:   []string{"127.0.0.1:19092"},
		Topic:     events.TopicReviewResolved,
		Partition: 0,
		MinBytes:  1,
		MaxBytes:  10e6,
	})
	defer reader.Close()

	found := false
	consumeCtx, consumeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer consumeCancel()

	for {
		msg, err := reader.ReadMessage(consumeCtx)
		if err != nil {
			break
		}

		if string(msg.Key) == eventPayload.TenantID.String() {
			var decoded events.ReviewResolvedEvent
			if err := json.Unmarshal(msg.Value, &decoded); err == nil && decoded.CaseID == testCaseID {
				found = true
				assert.Equal(t, "HOLD", decoded.FinalDecision)
				assert.Equal(t, "HOLD", decoded.HumanDecision)
				assert.Equal(t, "override", decoded.DecisionType)

				// Verify headers
				headersMap := make(map[string]string)
				for _, h := range msg.Headers {
					headersMap[h.Key] = string(h.Value)
				}
				assert.Equal(t, eventPayload.Signature, headersMap["x-arda-signature"])
				assert.Equal(t, eventPayload.KeyID, headersMap["x-arda-key-id"])
				break
			}
		}
	}

	assert.True(t, found, "expected published message to be consumed from live Redpanda")
}
