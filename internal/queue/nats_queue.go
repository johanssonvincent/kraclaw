package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/ipc"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	queueStreamMaxAge = 24 * time.Hour
	queueFetchTimeout = 200 * time.Millisecond
)

func sanitizeQueueGroupID(groupJID string) string { return ipc.SanitizeGroupID(groupJID) }

func queueStreamName(sanitized string) string {
	return "KRACLAW_QUEUE_" + strings.ToUpper(sanitized)
}

func queueSubject(sanitized string) string {
	return "kraclaw.queue." + sanitized
}

// NATSQueue implements Queue using NATS JetStream for message storage.
// Active group tracking delegates to a groupActiveStore (backed by MySQL in production).
type NATSQueue struct {
	nc     *nats.Conn
	js     jetstream.JetStream
	gas    groupActiveStore
	logger *slog.Logger

	mu     sync.Mutex
	closed bool
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
		nc:     nc,
		js:     js,
		gas:    gas,
		logger: logger,
	}, nil
}

func (q *NATSQueue) ensureStream(ctx context.Context, groupJID string) (string, jetstream.Stream, error) {
	sanitized := sanitizeQueueGroupID(groupJID)
	name := queueStreamName(sanitized)
	stream, err := q.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      name,
		Subjects:  []string{queueSubject(sanitized)},
		Retention: jetstream.WorkQueuePolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    queueStreamMaxAge,
		Replicas:  1,
	})
	if err != nil {
		return "", nil, fmt.Errorf("ensure queue stream %s: %w", name, err)
	}
	return sanitized, stream, nil
}

// Enqueue adds a message to the group's JetStream queue.
func (q *NATSQueue) Enqueue(ctx context.Context, groupJID string, msg *QueueMessage) error {
	sanitized, _, err := q.ensureStream(ctx, groupJID)
	if err != nil {
		return fmt.Errorf("enqueue ensure stream: %w", err)
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal queue message: %w", err)
	}
	if _, err := q.js.Publish(ctx, queueSubject(sanitized), data); err != nil {
		return fmt.Errorf("publish queue message: %w", err)
	}
	return nil
}

// Dequeue removes and returns the oldest message from the group's queue.
// Returns nil, nil when the queue is empty.
func (q *NATSQueue) Dequeue(ctx context.Context, groupJID string) (*QueueMessage, error) {
	sanitized, _, err := q.ensureStream(ctx, groupJID)
	if err != nil {
		return nil, fmt.Errorf("dequeue ensure stream: %w", err)
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
			meta, _ := msg.Metadata()
			var seq uint64
			if meta != nil {
				seq = meta.Sequence.Stream
			}
			q.logger.Warn("dequeue: malformed message payload — acking to discard",
				"subject", msg.Subject(),
				"sequence", seq,
				"raw", string(msg.Data()))
			if err := msg.Ack(); err != nil {
				q.logger.Error("ack malformed queue message", "subject", msg.Subject(), "sequence", seq, "error", err)
				return nil, fmt.Errorf("dequeue: malformed message ack: %w", err)
			}
			// Message was successfully discarded; caller should retry Dequeue for next message.
			return nil, nil
		}
		if err := msg.Ack(); err != nil {
			return nil, fmt.Errorf("dequeue: ack: %w", err)
		}
		return &qm, nil
	}
	if err := msgs.Error(); err != nil && !errors.Is(err, jetstream.ErrMsgIteratorClosed) {
		// Timeout and no-messages are expected for empty queue — don't error.
		if !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, jetstream.ErrNoMessages) {
			return nil, fmt.Errorf("fetch error: %w", err)
		}
	}
	return nil, nil // empty queue
}

// Peek returns the oldest message without removing it.
// It uses a direct stream get (not a durable consumer) so the message is never
// locked from the Dequeue durable consumer on WorkQueuePolicy streams.
func (q *NATSQueue) Peek(ctx context.Context, groupJID string) (*QueueMessage, error) {
	sanitized, stream, err := q.ensureStream(ctx, groupJID)
	if err != nil {
		return nil, fmt.Errorf("peek ensure stream: %w", err)
	}
	streamName := queueStreamName(sanitized)

	// Self-heal: remove any legacy "peek-{sanitized}" durable consumer left by
	// older deployments that used the consumer-based Peek implementation.
	if derr := q.js.DeleteConsumer(ctx, streamName, "peek-"+sanitized); derr != nil &&
		!errors.Is(derr, jetstream.ErrConsumerNotFound) {
		q.logger.Warn("peek: failed to delete legacy peek consumer", "error", derr)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("peek: stream info: %w", err)
	}
	if info.State.Msgs == 0 {
		return nil, nil // empty queue
	}

	raw, err := stream.GetMsg(ctx, info.State.FirstSeq)
	if err != nil {
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			return nil, nil // empty queue (race between Info and GetMsg)
		}
		return nil, fmt.Errorf("peek: get msg: %w", err)
	}

	var qm QueueMessage
	if err := json.Unmarshal(raw.Data, &qm); err != nil {
		return nil, fmt.Errorf("peek: unmarshal message: %w", err)
	}
	return &qm, nil
}

// Len returns the number of pending messages in the group's queue.
func (q *NATSQueue) Len(ctx context.Context, groupJID string) (int64, error) {
	_, stream, err := q.ensureStream(ctx, groupJID)
	if err != nil {
		return 0, fmt.Errorf("len ensure stream: %w", err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("stream info: %w", err)
	}
	return int64(info.State.Msgs), nil
}

// MarkActive delegates to the MySQL-backed GroupActiveStore.
func (q *NATSQueue) MarkActive(ctx context.Context, groupJID string) error {
	if err := q.gas.MarkGroupActive(ctx, groupJID); err != nil {
		return fmt.Errorf("mark active: %w", err)
	}
	return nil
}

// MarkInactive delegates to the MySQL-backed GroupActiveStore.
func (q *NATSQueue) MarkInactive(ctx context.Context, groupJID string) error {
	if err := q.gas.MarkGroupInactive(ctx, groupJID); err != nil {
		return fmt.Errorf("mark inactive: %w", err)
	}
	return nil
}

// IsActive delegates to the MySQL-backed GroupActiveStore.
func (q *NATSQueue) IsActive(ctx context.Context, groupJID string) (bool, error) {
	active, err := q.gas.IsGroupActive(ctx, groupJID)
	if err != nil {
		return false, fmt.Errorf("is active: %w", err)
	}
	return active, nil
}

// ActiveCount delegates to the MySQL-backed GroupActiveStore.
func (q *NATSQueue) ActiveCount(ctx context.Context) (int64, error) {
	count, err := q.gas.ActiveGroupCount(ctx)
	if err != nil {
		return 0, fmt.Errorf("active count: %w", err)
	}
	return count, nil
}

// ActiveJIDs delegates to the MySQL-backed GroupActiveStore.
func (q *NATSQueue) ActiveJIDs(ctx context.Context) ([]string, error) {
	jids, err := q.gas.ActiveGroupJIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("active jids: %w", err)
	}
	return jids, nil
}

// Close stops all background goroutines.
func (q *NATSQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	return nil
}
