package events

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/segmentio/kafka-go"
)

// EventProducer abstracts message publication to Redpanda/Kafka.
type EventProducer interface {
	Publish(ctx context.Context, topic string, key string, payload []byte, headers map[string]string) error
	Close() error
}

// KafkaProducer publishes events to Redpanda brokers using kafka-go.
type KafkaProducer struct {
	brokers []string
	writers map[string]*kafka.Writer
	mu      sync.Mutex
}

// NewKafkaProducer initializes a Kafka/Redpanda producer.
func NewKafkaProducer(brokerAddrs string) (*KafkaProducer, error) {
	parts := strings.Split(brokerAddrs, ",")
	var brokers []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			brokers = append(brokers, trimmed)
		}
	}
	if len(brokers) == 0 {
		brokers = []string{"127.0.0.1:19092"}
	}

	return &KafkaProducer{
		brokers: brokers,
		writers: make(map[string]*kafka.Writer),
	}, nil
}

func (p *KafkaProducer) getWriter(topic string) *kafka.Writer {
	p.mu.Lock()
	defer p.mu.Unlock()

	w, ok := p.writers[topic]
	if !ok {
		w = &kafka.Writer{
			Addr:         kafka.TCP(p.brokers...),
			Topic:        topic,
			Balancer:     &kafka.LeastBytes{},
			WriteTimeout: 5 * time.Second,
			RequiredAcks: kafka.RequireOne,
		}
		p.writers[topic] = w
	}
	return w
}

// Publish delivers a message to the specified topic.
func (p *KafkaProducer) Publish(ctx context.Context, topic string, key string, payload []byte, headers map[string]string) error {
	w := p.getWriter(topic)

	var kafkaHeaders []kafka.Header
	for k, v := range headers {
		kafkaHeaders = append(kafkaHeaders, kafka.Header{
			Key:   k,
			Value: []byte(v),
		})
	}

	msg := kafka.Message{
		Key:     []byte(key),
		Value:   payload,
		Headers: kafkaHeaders,
		Time:    time.Now().UTC(),
	}

	if err := w.WriteMessages(ctx, msg); err != nil {
		return fmt.Errorf("failed to write message to topic %s: %w", topic, err)
	}
	return nil
}

// Close closes all active topic writers.
func (p *KafkaProducer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var firstErr error
	for _, w := range p.writers {
		if err := w.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// MockProducer records published events in memory for testing.
type MockProducer struct {
	mu         sync.Mutex
	Published  []PublishedMessage
	PublishErr error
}

// PublishedMessage records an individual published event.
type PublishedMessage struct {
	Topic   string
	Key     string
	Payload []byte
	Headers map[string]string
}

// NewMockProducer constructs a MockProducer.
func NewMockProducer() *MockProducer {
	return &MockProducer{}
}

// GetPublished returns a thread-safe copy of published messages.
func (m *MockProducer) GetPublished() []PublishedMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]PublishedMessage, len(m.Published))
	copy(res, m.Published)
	return res
}

// SetPublishError configures an error to return on future Publish calls.
func (m *MockProducer) SetPublishError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PublishErr = err
}

func (m *MockProducer) Publish(ctx context.Context, topic string, key string, payload []byte, headers map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.PublishErr != nil {
		return m.PublishErr
	}

	m.Published = append(m.Published, PublishedMessage{
		Topic:   topic,
		Key:     key,
		Payload: payload,
		Headers: headers,
	})
	return nil
}

func (m *MockProducer) Close() error {
	return nil
}
