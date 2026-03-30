package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// QueueMessage represents a message in the processing queue.
type QueueMessage struct {
	GroupJID  string    `json:"groupJid"`
	Content   string    `json:"content"`
	Sender    string    `json:"sender"`
	Timestamp time.Time `json:"timestamp"`
	IsTask    bool      `json:"isTask,omitempty"`
	TaskID    string    `json:"taskId,omitempty"`
}

// QueueEventType represents the type of a queue event.
type QueueEventType string

const (
	EventEnqueued QueueEventType = "enqueued"
	EventActive   QueueEventType = "active"
	EventInactive QueueEventType = "inactive"
)

// QueueEvent represents a queue state change notification.
type QueueEvent struct {
	Type     QueueEventType `json:"type"`
	GroupJID string         `json:"groupJid"`
}

// Queue defines the interface for the message processing queue.
type Queue interface {
	Enqueue(ctx context.Context, groupJID string, msg *QueueMessage) error
	Dequeue(ctx context.Context, groupJID string) (*QueueMessage, error)
	Peek(ctx context.Context, groupJID string) (*QueueMessage, error)
	Len(ctx context.Context, groupJID string) (int64, error)
	MarkActive(ctx context.Context, groupJID string) error
	MarkInactive(ctx context.Context, groupJID string) error
	IsActive(ctx context.Context, groupJID string) (bool, error)
	ActiveCount(ctx context.Context) (int64, error)
	Subscribe(ctx context.Context) (<-chan QueueEvent, error)
	Close() error
}

func queueKey(groupJID string) string { return fmt.Sprintf("kraclaw:queue:%s", groupJID) }
func activeSetKey() string            { return "kraclaw:active" }
func notifyChannel() string           { return "kraclaw:queue:notify" }

// RedisQueue implements Queue using Redis lists and sets.
type RedisQueue struct {
	rdb    redis.Cmdable
	logger *slog.Logger

	mu       sync.Mutex
	cancels  []context.CancelFunc
	closed   bool
	closedCh chan struct{}
}

// NewRedisQueue creates a new Redis-backed queue.
func NewRedisQueue(rdb redis.Cmdable, logger *slog.Logger) *RedisQueue {
	if logger == nil {
		logger = slog.Default()
	}
	return &RedisQueue{
		rdb:      rdb,
		logger:   logger,
		closedCh: make(chan struct{}),
	}
}

// Enqueue adds a message to the group's queue (LPUSH for FIFO with RPOP).
func (q *RedisQueue) Enqueue(ctx context.Context, groupJID string, msg *QueueMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	if err := q.rdb.LPush(ctx, queueKey(groupJID), string(data)).Err(); err != nil {
		return fmt.Errorf("lpush: %w", err)
	}
	q.publishEvent(ctx, QueueEvent{Type: EventEnqueued, GroupJID: groupJID})
	return nil
}

// Dequeue removes and returns the oldest message from the queue (RPOP).
func (q *RedisQueue) Dequeue(ctx context.Context, groupJID string) (*QueueMessage, error) {
	raw, err := q.rdb.RPop(ctx, queueKey(groupJID)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("rpop: %w", err)
	}
	var msg QueueMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}
	return &msg, nil
}

// Peek returns the oldest message without removing it (LINDEX -1 for rightmost).
func (q *RedisQueue) Peek(ctx context.Context, groupJID string) (*QueueMessage, error) {
	raw, err := q.rdb.LIndex(ctx, queueKey(groupJID), -1).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("lindex: %w", err)
	}
	var msg QueueMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}
	return &msg, nil
}

// Len returns the number of messages in the group's queue.
func (q *RedisQueue) Len(ctx context.Context, groupJID string) (int64, error) {
	return q.rdb.LLen(ctx, queueKey(groupJID)).Result()
}

// MarkActive adds the group to the active set.
func (q *RedisQueue) MarkActive(ctx context.Context, groupJID string) error {
	if err := q.rdb.SAdd(ctx, activeSetKey(), groupJID).Err(); err != nil {
		return fmt.Errorf("sadd: %w", err)
	}
	q.publishEvent(ctx, QueueEvent{Type: EventActive, GroupJID: groupJID})
	return nil
}

// MarkInactive removes the group from the active set.
func (q *RedisQueue) MarkInactive(ctx context.Context, groupJID string) error {
	if err := q.rdb.SRem(ctx, activeSetKey(), groupJID).Err(); err != nil {
		return fmt.Errorf("srem: %w", err)
	}
	q.publishEvent(ctx, QueueEvent{Type: EventInactive, GroupJID: groupJID})
	return nil
}

// IsActive checks if a group is in the active set.
func (q *RedisQueue) IsActive(ctx context.Context, groupJID string) (bool, error) {
	ok, err := q.rdb.SIsMember(ctx, activeSetKey(), groupJID).Result()
	if err != nil {
		return false, fmt.Errorf("sismember: %w", err)
	}
	return ok, nil
}

// ActiveCount returns the number of active groups.
func (q *RedisQueue) ActiveCount(ctx context.Context) (int64, error) {
	return q.rdb.SCard(ctx, activeSetKey()).Result()
}

// Subscribe returns a channel that receives queue events via pub/sub.
func (q *RedisQueue) Subscribe(ctx context.Context) (<-chan QueueEvent, error) {
	ch := make(chan QueueEvent, 64)

	ctx, cancel := context.WithCancel(ctx)
	q.mu.Lock()
	q.cancels = append(q.cancels, cancel)
	q.mu.Unlock()

	// We need a real *redis.Client for Subscribe; check if we have one.
	client, ok := q.rdb.(*redis.Client)
	if !ok {
		cancel()
		close(ch)
		return nil, fmt.Errorf("subscribe requires *redis.Client, got %T", q.rdb)
	}

	pubsub := client.Subscribe(ctx, notifyChannel())

	go func() {
		defer close(ch)
		defer func() { _ = pubsub.Close() }()
		defer cancel()

		subCh := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case <-q.closedCh:
				return
			case redisMsg, ok := <-subCh:
				if !ok {
					return
				}
				var evt QueueEvent
				if err := json.Unmarshal([]byte(redisMsg.Payload), &evt); err != nil {
					q.logger.Warn("unmarshal queue event", "error", err)
					continue
				}
				select {
				case ch <- evt:
				case <-ctx.Done():
					return
				case <-q.closedCh:
					return
				}
			}
		}
	}()

	return ch, nil
}

// Close stops all background goroutines.
func (q *RedisQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	for _, cancel := range q.cancels {
		cancel()
	}
	close(q.closedCh)
	return nil
}

func (q *RedisQueue) publishEvent(ctx context.Context, evt QueueEvent) {
	data, err := json.Marshal(evt)
	if err != nil {
		q.logger.Warn("marshal queue event", "error", err)
		return
	}
	if err := q.rdb.Publish(ctx, notifyChannel(), string(data)).Err(); err != nil {
		q.logger.Warn("failed to publish queue event", "type", evt.Type, "group_jid", evt.GroupJID, "error", err)
	}
}
