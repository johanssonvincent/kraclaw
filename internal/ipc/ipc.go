package ipc

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

// IPCMessageType represents the type of an IPC message.
type IPCMessageType string

const (
	IPCMessageText   IPCMessageType = "message"
	IPCSessionUpdate IPCMessageType = "session_update"
	IPCTaskCreate    IPCMessageType = "task_create"
	IPCTaskUpdate    IPCMessageType = "task_update"
	IPCTaskDelete    IPCMessageType = "task_delete"
	IPCSetModel      IPCMessageType = "set_model"
	IPCShutdown      IPCMessageType = "shutdown"
)

// IPCMessage represents a message exchanged between agent and server.
type IPCMessage struct {
	Group   string          `json:"group"`
	AgentID string          `json:"agent_id"`
	Type    IPCMessageType  `json:"type"`
	Payload json.RawMessage `json:"payload"`
	ID      string          `json:"id"` // Message ID set by broker on receive
}

// IPCBroker defines the interface for IPC communication between server and agents.
type IPCBroker interface {
	// SendInput sends a message from the server to a specific agent in a group.
	SendInput(ctx context.Context, group, agentID string, msg *IPCMessage) error
	// PublishOutput sends a message from an agent to the server.
	PublishOutput(ctx context.Context, group, agentID string, msg *IPCMessage) error
	// SubscribeOutput returns a channel receiving output from all agents in a group (wildcard).
	SubscribeOutput(ctx context.Context, group string) (<-chan *IPCMessage, error)
	// ReadInput returns a channel receiving input messages for a specific agent.
	ReadInput(ctx context.Context, group, agentID string) (<-chan *IPCMessage, error)
	// DeleteStreams removes all IPC data for a group (all agents).
	DeleteStreams(ctx context.Context, group string) error
	Close() error
}

const (
	consumerGroup = "kraclaw-server"
	// Keep the blocking read short so broker shutdown does not stall waiting
	// for Redis to return from XREADGROUP.
	readTimeout = 500 * time.Millisecond
)

func outputKey(group string) string { return fmt.Sprintf("kraclaw:ipc:%s:output", group) }
func inputKey(group string) string  { return fmt.Sprintf("kraclaw:ipc:%s:input", group) }
func notifyChannel() string         { return "kraclaw:ipc:notify" }

// RedisBroker implements IPCBroker using Redis streams and pub/sub.
type RedisBroker struct {
	rdb    redis.Cmdable
	logger *slog.Logger

	mu       sync.Mutex
	cancels  map[uint64]context.CancelFunc
	nextID   uint64
	closed   bool
	closedCh chan struct{}
}

// NewRedisBroker creates a new Redis-backed IPC broker.
func NewRedisBroker(rdb redis.Cmdable, logger *slog.Logger) *RedisBroker {
	if logger == nil {
		logger = slog.Default()
	}
	return &RedisBroker{
		rdb:      rdb,
		logger:   logger,
		cancels:  make(map[uint64]context.CancelFunc),
		closedCh: make(chan struct{}),
	}
}

// ensureConsumerGroup creates the consumer group if it doesn't exist.
func (b *RedisBroker) ensureConsumerGroup(ctx context.Context, stream string) error {
	err := b.rdb.XGroupCreateMkStream(ctx, stream, consumerGroup, "0").Err()
	if err != nil && !isConsumerGroupExistsErr(err) {
		return fmt.Errorf("create consumer group: %w", err)
	}
	return nil
}

func isConsumerGroupExistsErr(err error) bool {
	return err != nil && err.Error() == "BUSYGROUP Consumer Group name already exists"
}

// PublishOutput adds a message to the output stream (agent -> server).
func (b *RedisBroker) PublishOutput(ctx context.Context, group, agentID string, msg *IPCMessage) error {
	stream := outputKey(group)
	if err := b.ensureConsumerGroup(ctx, stream); err != nil {
		return err
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	result := b.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		Values: map[string]interface{}{"data": string(data)},
	})
	if result.Err() != nil {
		return fmt.Errorf("xadd: %w", result.Err())
	}

	// Publish notification for faster pickup.
	if err := b.rdb.Publish(ctx, notifyChannel(), group).Err(); err != nil {
		b.logger.Warn("failed to publish output notification", "group", group, "error", err)
	}
	return nil
}

// SubscribeOutput returns a channel that receives messages from the output stream.
func (b *RedisBroker) SubscribeOutput(ctx context.Context, group string) (<-chan *IPCMessage, error) {
	stream := outputKey(group)
	if err := b.ensureConsumerGroup(ctx, stream); err != nil {
		return nil, err
	}
	return b.readStream(ctx, stream, "output-reader"), nil
}

// SendInput adds a message to the input stream (server -> agent).
func (b *RedisBroker) SendInput(ctx context.Context, group, agentID string, msg *IPCMessage) error {
	stream := inputKey(group)
	if err := b.ensureConsumerGroup(ctx, stream); err != nil {
		return err
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	result := b.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		Values: map[string]interface{}{"data": string(data)},
	})
	if result.Err() != nil {
		return fmt.Errorf("xadd: %w", result.Err())
	}

	if err := b.rdb.Publish(ctx, notifyChannel(), group).Err(); err != nil {
		b.logger.Warn("failed to publish input notification", "group", group, "error", err)
	}
	return nil
}

// ReadInput returns a channel that receives messages from the input stream.
func (b *RedisBroker) ReadInput(ctx context.Context, group, agentID string) (<-chan *IPCMessage, error) {
	stream := inputKey(group)
	if err := b.ensureConsumerGroup(ctx, stream); err != nil {
		return nil, err
	}
	return b.readStream(ctx, stream, "input-reader"), nil
}

// DeleteStreams removes the input and output keys for a group.
func (b *RedisBroker) DeleteStreams(ctx context.Context, group string) error {
	keys := []string{outputKey(group), inputKey(group)}
	if err := b.rdb.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("delete ipc streams for %s: %w", group, err)
	}
	return nil
}

// Close stops all background goroutines.
func (b *RedisBroker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	for _, cancel := range b.cancels {
		cancel()
	}
	b.cancels = nil
	close(b.closedCh)
	return nil
}

// readStream runs a blocking XREADGROUP loop in a goroutine, sending parsed messages to the channel.
func (b *RedisBroker) readStream(ctx context.Context, stream, consumer string) <-chan *IPCMessage {
	ch := make(chan *IPCMessage, 64)

	ctx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.cancels[id] = cancel
	b.mu.Unlock()

	go func() {
		defer close(ch)
		defer func() {
			b.mu.Lock()
			delete(b.cancels, id)
			b.mu.Unlock()
			cancel()
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case <-b.closedCh:
				return
			default:
			}

			results, err := b.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    consumerGroup,
				Consumer: consumer,
				Streams:  []string{stream, ">"},
				Count:    10,
				Block:    readTimeout,
			}).Result()

			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}
				if errors.Is(err, redis.Nil) {
					continue
				}
				b.logger.Warn("xreadgroup error", "stream", stream, "error", err)
				continue
			}

			for _, s := range results {
				for _, entry := range s.Messages {
					raw, ok := entry.Values["data"].(string)
					if !ok {
						b.logger.Error("stream entry missing data field", "stream", stream, "entry_id", entry.ID)
						continue
					}
					var msg IPCMessage
					if err := json.Unmarshal([]byte(raw), &msg); err != nil {
						b.logger.Warn("unmarshal stream message", "stream", stream, "error", err)
						continue
					}
					msg.ID = entry.ID

					// Send to subscriber channel BEFORE acknowledging.
					select {
					case ch <- &msg:
					case <-ctx.Done():
						return
					case <-b.closedCh:
						return
					}

					// ACK the message only after successful delivery.
					if err := b.rdb.XAck(ctx, stream, consumerGroup, entry.ID).Err(); err != nil {
						b.logger.Error("failed to ack stream entry", "stream", stream, "entry_id", entry.ID, "error", err)
					}
				}
			}
		}
	}()

	return ch
}
