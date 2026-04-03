package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	queueStreamMaxAge = 24 * time.Hour
	queueAckWait      = 24 * time.Hour
	queueFetchTimeout = 200 * time.Millisecond
	queueEventSubject = "kraclaw.queue.events"
)

func sanitizeQueueGroupID(groupJID string) string {
	h := sha256.Sum256([]byte(groupJID))
	return hex.EncodeToString(h[:16])
}

func queueStreamName(sanitized string) string {
	return "KRACLAW_QUEUE_" + strings.ToUpper(sanitized)
}

func queueSubject(sanitized string) string {
	return "kraclaw.queue." + sanitized
}

// NATSQueue implements Queue using NATS JetStream for message storage and core
// NATS for event notifications. Active group tracking delegates to a
// groupActiveStore (backed by MySQL in production).
type NATSQueue struct {
	nc     *nats.Conn
	js     jetstream.JetStream
	gas    groupActiveStore
	logger *slog.Logger

	mu       sync.Mutex
	cancels  []context.CancelFunc
	closed   bool
	closedCh chan struct{}
}

// NewNATSQueue creates a NATSQueue. gas must not be nil.
func NewNATSQueue(nc *nats.Conn, gas groupActiveStore, logger *slog.Logger) (*NATSQueue, error) {
	if nc == nil {
		return nil, fmt.Errorf("nats queue: connection is required")
	}
	if gas == nil {
		return nil, fmt.Errorf("nats queue: groupActiveStore is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("nats queue: jetstream: %w", err)
	}
	return &NATSQueue{
		nc:       nc,
		js:       js,
		gas:      gas,
		logger:   logger,
		closedCh: make(chan struct{}),
	}, nil
}

func (q *NATSQueue) ensureStream(ctx context.Context, groupJID string) (string, error) {
	sanitized := sanitizeQueueGroupID(groupJID)
	name := queueStreamName(sanitized)
	_, err := q.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      name,
		Subjects:  []string{queueSubject(sanitized)},
		Retention: jetstream.WorkQueuePolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    queueStreamMaxAge,
		Replicas:  1,
	})
	if err != nil {
		return "", fmt.Errorf("ensure queue stream %s: %w", name, err)
	}
	return sanitized, nil
}

// Enqueue adds a message to the group's JetStream queue.
func (q *NATSQueue) Enqueue(ctx context.Context, groupJID string, msg *QueueMessage) error {
	sanitized, err := q.ensureStream(ctx, groupJID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal queue message: %w", err)
	}
	if _, err := q.js.Publish(ctx, queueSubject(sanitized), data); err != nil {
		return fmt.Errorf("publish queue message: %w", err)
	}
	q.publishEvent(ctx, QueueEvent{Type: EventEnqueued, GroupJID: groupJID})
	return nil
}

// Dequeue removes and returns the oldest message from the group's queue.
// Returns nil, nil when the queue is empty.
func (q *NATSQueue) Dequeue(ctx context.Context, groupJID string) (*QueueMessage, error) {
	sanitized, err := q.ensureStream(ctx, groupJID)
	if err != nil {
		return nil, err
	}
	streamName := queueStreamName(sanitized)
	cons, err := q.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:   "dequeue-" + sanitized,
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create dequeue consumer: %w", err)
	}

	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(queueFetchTimeout))
	if err != nil {
		return nil, fmt.Errorf("fetch queue message: %w", err)
	}
	for msg := range msgs.Messages() {
		var qm QueueMessage
		if err := json.Unmarshal(msg.Data(), &qm); err != nil {
			_ = msg.Ack()
			return nil, fmt.Errorf("unmarshal queue message: %w", err)
		}
		if err := msg.Ack(); err != nil {
			q.logger.Error("ack dequeued message", "error", err)
		}
		return &qm, nil
	}
	if err := msgs.Error(); err != nil && !errors.Is(err, jetstream.ErrMsgIteratorClosed) {
		// Timeout is expected for empty queue — don't error.
		errStr := err.Error()
		if !strings.Contains(errStr, "timeout") && !strings.Contains(errStr, "no messages") {
			return nil, fmt.Errorf("fetch error: %w", err)
		}
	}
	return nil, nil // empty queue
}

// Peek returns the oldest message without removing it.
func (q *NATSQueue) Peek(ctx context.Context, groupJID string) (*QueueMessage, error) {
	sanitized, err := q.ensureStream(ctx, groupJID)
	if err != nil {
		return nil, err
	}
	streamName := queueStreamName(sanitized)
	cons, err := q.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:   "peek-" + sanitized,
		AckPolicy: jetstream.AckExplicitPolicy,
		AckWait:   queueAckWait,
	})
	if err != nil {
		return nil, fmt.Errorf("create peek consumer: %w", err)
	}

	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(queueFetchTimeout))
	if err != nil {
		return nil, fmt.Errorf("peek fetch: %w", err)
	}
	for msg := range msgs.Messages() {
		var qm QueueMessage
		if err := json.Unmarshal(msg.Data(), &qm); err != nil {
			return nil, fmt.Errorf("unmarshal peek message: %w", err)
		}
		// Do NOT ack — leave message in queue for Dequeue to consume.
		return &qm, nil
	}
	if err := msgs.Error(); err != nil && !errors.Is(err, jetstream.ErrMsgIteratorClosed) {
		errStr := err.Error()
		if !strings.Contains(errStr, "timeout") && !strings.Contains(errStr, "no messages") {
			return nil, fmt.Errorf("peek error: %w", err)
		}
	}
	return nil, nil // empty queue
}

// Len returns the number of pending messages in the group's queue.
func (q *NATSQueue) Len(ctx context.Context, groupJID string) (int64, error) {
	sanitized, err := q.ensureStream(ctx, groupJID)
	if err != nil {
		return 0, err
	}
	stream, err := q.js.Stream(ctx, queueStreamName(sanitized))
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("get stream info: %w", err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("stream info: %w", err)
	}
	return int64(info.State.Msgs), nil
}

// MarkActive delegates to the MySQL-backed GroupActiveStore and publishes an active event.
func (q *NATSQueue) MarkActive(ctx context.Context, groupJID string) error {
	if err := q.gas.MarkGroupActive(ctx, groupJID); err != nil {
		return err
	}
	q.publishEvent(ctx, QueueEvent{Type: EventActive, GroupJID: groupJID})
	return nil
}

// MarkInactive delegates to the MySQL-backed GroupActiveStore and publishes an inactive event.
func (q *NATSQueue) MarkInactive(ctx context.Context, groupJID string) error {
	if err := q.gas.MarkGroupInactive(ctx, groupJID); err != nil {
		return err
	}
	q.publishEvent(ctx, QueueEvent{Type: EventInactive, GroupJID: groupJID})
	return nil
}

// IsActive delegates to the MySQL-backed GroupActiveStore.
func (q *NATSQueue) IsActive(ctx context.Context, groupJID string) (bool, error) {
	return q.gas.IsGroupActive(ctx, groupJID)
}

// ActiveCount delegates to the MySQL-backed GroupActiveStore.
func (q *NATSQueue) ActiveCount(ctx context.Context) (int64, error) {
	return q.gas.ActiveGroupCount(ctx)
}

// ActiveJIDs delegates to the MySQL-backed GroupActiveStore.
func (q *NATSQueue) ActiveJIDs(ctx context.Context) ([]string, error) {
	return q.gas.ActiveGroupJIDs(ctx)
}

// Subscribe returns a channel that receives queue events via core NATS.
func (q *NATSQueue) Subscribe(ctx context.Context) (<-chan QueueEvent, error) {
	ch := make(chan QueueEvent, 64)

	ctx, cancel := context.WithCancel(ctx)
	q.mu.Lock()
	q.cancels = append(q.cancels, cancel)
	q.mu.Unlock()

	sub, err := q.nc.Subscribe(queueEventSubject, func(msg *nats.Msg) {
		var evt QueueEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			q.logger.Warn("unmarshal queue event", "error", err)
			return
		}
		select {
		case ch <- evt:
		case <-ctx.Done():
		case <-q.closedCh:
		}
	})
	if err != nil {
		cancel()
		close(ch)
		return nil, fmt.Errorf("subscribe queue events: %w", err)
	}

	go func() {
		defer close(ch)
		defer func() { _ = sub.Unsubscribe() }()
		defer cancel()
		select {
		case <-ctx.Done():
		case <-q.closedCh:
		}
	}()

	return ch, nil
}

// Close stops all background goroutines.
func (q *NATSQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	for _, cancel := range q.cancels {
		cancel()
	}
	q.cancels = nil
	close(q.closedCh)
	return nil
}

func (q *NATSQueue) publishEvent(ctx context.Context, evt QueueEvent) {
	data, err := json.Marshal(evt)
	if err != nil {
		q.logger.Warn("marshal queue event", "error", err)
		return
	}
	if err := q.nc.Publish(queueEventSubject, data); err != nil {
		q.logger.Warn("publish queue event", "type", evt.Type, "group", evt.GroupJID, "error", err)
	}
}
