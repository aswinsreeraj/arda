package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/aswinsreeraj/arda/services/review/store"
)

// OutboxRelay dispatches pending database outbox events to Redpanda.
type OutboxRelay struct {
	store        store.Store
	producer     EventProducer
	batchSize    int
	pollInterval time.Duration
	onPublished  func(topic string, payload []byte)
}

// NewOutboxRelay creates a new transactional outbox relay.
func NewOutboxRelay(st store.Store, prod EventProducer, opts ...RelayOption) *OutboxRelay {
	r := &OutboxRelay{
		store:        st,
		producer:     prod,
		batchSize:    50,
		pollInterval: 1 * time.Second,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RelayOption configures an OutboxRelay.
type RelayOption func(*OutboxRelay)

// WithBatchSize configures the maximum events fetched per flush.
func WithBatchSize(size int) RelayOption {
	return func(r *OutboxRelay) {
		if size > 0 {
			r.batchSize = size
		}
	}
}

// WithPollInterval configures the polling cadence.
func WithPollInterval(interval time.Duration) RelayOption {
	return func(r *OutboxRelay) {
		if interval > 0 {
			r.pollInterval = interval
		}
	}
}

// WithOnPublished registers an optional callback invoked after an outbox event
// is successfully published and marked published (e.g., for local WebSocket fanout).
func WithOnPublished(fn func(topic string, payload []byte)) RelayOption {
	return func(r *OutboxRelay) {
		r.onPublished = fn
	}
}

// Flush fetches and publishes pending outbox events synchronously.
func (r *OutboxRelay) Flush(ctx context.Context) (int, error) {
	if r.store == nil || r.producer == nil {
		return 0, nil
	}

	events, err := r.store.FetchPendingOutboxEvents(ctx, r.batchSize)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch pending outbox events: %w", err)
	}

	publishedCount := 0
	for _, ev := range events {
		payloadBytes, err := json.Marshal(ev.Payload)
		if err != nil {
			_ = r.store.MarkOutboxEventFailed(ctx, ev.ID, fmt.Sprintf("json serialization failure: %v", err))
			continue
		}

		key := ev.TenantID.String()
		if err := r.producer.Publish(ctx, ev.Topic, key, payloadBytes, ev.Headers); err != nil {
			_ = r.store.MarkOutboxEventFailed(ctx, ev.ID, fmt.Sprintf("publish failure: %v", err))
			continue
		}

		if err := r.store.MarkOutboxEventPublished(ctx, ev.ID); err != nil {
			log.Printf("ERROR: failed to mark outbox event %s published: %v", ev.ID, err)
			continue
		}
		if r.onPublished != nil {
			r.onPublished(ev.Topic, payloadBytes)
		}
		publishedCount++
	}

	return publishedCount, nil
}

// Start begins the background relay polling loop until ctx is canceled.
func (r *OutboxRelay) Start(ctx context.Context) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := r.Flush(ctx); err != nil {
				log.Printf("OutboxRelay flush error: %v", err)
			}
		}
	}
}
