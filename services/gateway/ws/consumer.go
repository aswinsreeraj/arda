package ws

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	reviewevents "github.com/aswinsreeraj/arda/services/review/events"
	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
)

// RedpandaConsumer subscribes to review lifecycle topics in Redpanda and fans out
// live notifications to tenant-scoped WebSocket clients connected to this Gateway instance.
type RedpandaConsumer struct {
	brokers []string
	groupID string
	hub     *Hub
}

// NewRedpandaConsumer constructs a per-instance Redpanda consumer for WebSocket fanout.
// Each Gateway instance uses a unique group ID starting at LastOffset so every replica
// receives all live review notifications without replaying historical events on startup.
func NewRedpandaConsumer(brokerAddrs string, hub *Hub) *RedpandaConsumer {
	raw := strings.Split(brokerAddrs, ",")
	brokers := make([]string, 0, len(raw))
	for _, b := range raw {
		if trimmed := strings.TrimSpace(b); trimmed != "" {
			brokers = append(brokers, trimmed)
		}
	}
	return &RedpandaConsumer{
		brokers: brokers,
		groupID: fmt.Sprintf("arda-gateway-ws-%s", uuid.NewString()[:8]),
		hub:     hub,
	}
}

// Start runs reader loops for both review.created and review.resolved topics until ctx is canceled.
func (c *RedpandaConsumer) Start(ctx context.Context) {
	if c == nil || c.hub == nil || len(c.brokers) == 0 {
		return
	}

	topics := []string{
		reviewevents.TopicReviewCreated,
		reviewevents.TopicReviewResolved,
	}

	var wg sync.WaitGroup
	for _, topic := range topics {
		wg.Add(1)
		go func(t string) {
			defer wg.Done()
			c.consumeTopic(ctx, t)
		}(topic)
	}
	wg.Wait()
}

func (c *RedpandaConsumer) consumeTopic(ctx context.Context, topic string) {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        c.brokers,
		Topic:          topic,
		GroupID:        c.groupID,
		StartOffset:    kafka.LastOffset,
		MinBytes:       1,
		MaxBytes:       1e6, // 1MB
		MaxWait:        1 * time.Second,
		CommitInterval: 1 * time.Second,
	})
	defer func() {
		_ = reader.Close()
	}()

	log.Printf("[WS] Redpanda fanout consumer started (topic=%s group=%s)", topic, c.groupID)

	for {
		msg, err := reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Back off quietly on transient broker/topic unavailability (e.g. topic not yet created).
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}

		c.hub.BroadcastRawEvent(topic, msg.Value)
	}
}
