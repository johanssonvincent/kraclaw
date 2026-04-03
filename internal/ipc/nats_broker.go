package ipc

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
	ipcStreamMaxAge   = time.Hour
	ipcServerConsumer = "kraclaw-server"
)

// sanitizeGroupID returns the first 16 bytes of the SHA-256 hex digest of the
// group JID (32 hex characters). NATS uses '.' as a subject separator, so raw
// JIDs (e.g. "123@g.us") cannot be used directly in stream names or subjects.
func sanitizeGroupID(groupJID string) string {
	h := sha256.Sum256([]byte(groupJID))
	return hex.EncodeToString(h[:16])
}

func ipcStreamName(sanitized string) string {
	return "KRACLAW_IPC_" + strings.ToUpper(sanitized)
}

func ipcInputSubject(sanitized, agentID string) string {
	return "kraclaw.ipc." + sanitized + "." + agentID + ".input"
}

func ipcOutputSubject(sanitized, agentID string) string {
	return "kraclaw.ipc." + sanitized + "." + agentID + ".output"
}

func ipcOutputWildcard(sanitized string) string {
	return "kraclaw.ipc." + sanitized + ".*.output"
}

// NATSBroker implements IPCBroker using NATS JetStream.
//
// Per-group stream topology:
//   - Stream name: KRACLAW_IPC_{sanitized_group}
//   - Input subject:  kraclaw.ipc.{sanitized}.{agentID}.input
//   - Output subject: kraclaw.ipc.{sanitized}.{agentID}.output
//   - Server subscribes to wildcard: kraclaw.ipc.{sanitized}.*.output
type NATSBroker struct {
	nc     *nats.Conn
	js     jetstream.JetStream
	logger *slog.Logger

	mu       sync.Mutex
	cancels  []context.CancelFunc
	iters    []jetstream.MessagesContext // track iterators for cleanup
	closed   bool
	closedCh chan struct{}
}

// NewNATSBroker creates a NATSBroker using the provided NATS connection.
func NewNATSBroker(nc *nats.Conn, logger *slog.Logger) (*NATSBroker, error) {
	if nc == nil {
		return nil, fmt.Errorf("nats broker: connection is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("nats broker: jetstream: %w", err)
	}
	return &NATSBroker{
		nc:       nc,
		js:       js,
		logger:   logger,
		closedCh: make(chan struct{}),
	}, nil
}

// ensureStream creates the per-group JetStream stream if it does not exist.
func (b *NATSBroker) ensureStream(ctx context.Context, group string) (string, error) {
	sanitized := sanitizeGroupID(group)
	name := ipcStreamName(sanitized)
	_, err := b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: name,
		Subjects: []string{
			"kraclaw.ipc." + sanitized + ".*.input",
			"kraclaw.ipc." + sanitized + ".*.output",
		},
		Retention: jetstream.InterestPolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    ipcStreamMaxAge,
		Replicas:  1,
	})
	if err != nil {
		return "", fmt.Errorf("ensure ipc stream %s: %w", name, err)
	}
	return sanitized, nil
}

// PublishOutput publishes a message from an agent to the server output subject.
func (b *NATSBroker) PublishOutput(ctx context.Context, group, agentID string, msg *IPCMessage) error {
	sanitized, err := b.ensureStream(ctx, group)
	if err != nil {
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal ipc message: %w", err)
	}
	subject := ipcOutputSubject(sanitized, agentID)
	if _, err := b.js.Publish(ctx, subject, data); err != nil {
		return fmt.Errorf("publish output: %w", err)
	}
	return nil
}

// SendInput publishes a message from the server to a specific agent's input subject.
func (b *NATSBroker) SendInput(ctx context.Context, group, agentID string, msg *IPCMessage) error {
	sanitized, err := b.ensureStream(ctx, group)
	if err != nil {
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal ipc message: %w", err)
	}
	subject := ipcInputSubject(sanitized, agentID)
	if _, err := b.js.Publish(ctx, subject, data); err != nil {
		return fmt.Errorf("send input: %w", err)
	}
	return nil
}

// SubscribeOutput returns a channel that receives output from all agents in
// the group via a durable wildcard push consumer.
func (b *NATSBroker) SubscribeOutput(ctx context.Context, group string) (<-chan *IPCMessage, error) {
	sanitized, err := b.ensureStream(ctx, group)
	if err != nil {
		return nil, err
	}
	streamName := ipcStreamName(sanitized)
	cons, err := b.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       ipcServerConsumer,
		FilterSubject: ipcOutputWildcard(sanitized),
		DeliverPolicy: jetstream.DeliverNewPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create output consumer: %w", err)
	}
	return b.consume(ctx, cons), nil
}

// ReadInput returns a channel that receives input messages for a specific agent.
func (b *NATSBroker) ReadInput(ctx context.Context, group, agentID string) (<-chan *IPCMessage, error) {
	sanitized, err := b.ensureStream(ctx, group)
	if err != nil {
		return nil, err
	}
	streamName := ipcStreamName(sanitized)
	cons, err := b.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       "agent-" + agentID,
		FilterSubject: ipcInputSubject(sanitized, agentID),
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create input consumer: %w", err)
	}
	return b.consume(ctx, cons), nil
}

// DeleteStreams removes the JetStream stream for a group (covers all agents).
func (b *NATSBroker) DeleteStreams(ctx context.Context, group string) error {
	sanitized := sanitizeGroupID(group)
	streamName := ipcStreamName(sanitized)
	if err := b.js.DeleteStream(ctx, streamName); err != nil {
		// Treat "stream not found" as success (idempotent delete).
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			return nil
		}
		return fmt.Errorf("delete ipc stream %s: %w", streamName, err)
	}
	return nil
}

// Close stops all background goroutines and closes the broker.
func (b *NATSBroker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	// Stop all iterators first — this unblocks iter.Next() calls.
	for _, iter := range b.iters {
		iter.Stop()
	}
	b.iters = nil
	// Then cancel all contexts.
	for _, cancel := range b.cancels {
		cancel()
	}
	b.cancels = nil
	close(b.closedCh)
	return nil
}

// consume creates a goroutine that drains a JetStream consumer into a channel.
func (b *NATSBroker) consume(ctx context.Context, cons jetstream.Consumer) <-chan *IPCMessage {
	ch := make(chan *IPCMessage, 64)

	ctx, cancel := context.WithCancel(ctx)

	iter, err := cons.Messages()
	if err != nil {
		cancel()
		b.logger.Error("create message iterator", "error", err)
		close(ch)
		return ch
	}

	b.mu.Lock()
	b.cancels = append(b.cancels, cancel)
	b.iters = append(b.iters, iter)
	b.mu.Unlock()

	// Stop the iterator when ctx is cancelled externally. Without this, a
	// blocking iter.Next() call would keep the consume goroutine alive even
	// after the caller cancels ctx — a goroutine leak.
	go func() {
		select {
		case <-ctx.Done():
			iter.Stop()
		case <-b.closedCh:
			// iter.Stop() already called by Close().
		}
	}()

	go func() {
		defer close(ch)
		defer cancel()
		defer iter.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-b.closedCh:
				return
			default:
			}

			jmsg, err := iter.Next()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				b.logger.Warn("ipc message iterator error", "error", err)
				return
			}

			var msg IPCMessage
			if err := json.Unmarshal(jmsg.Data(), &msg); err != nil {
				b.logger.Warn("unmarshal ipc message", "error", err)
				if err := jmsg.Ack(); err != nil {
					b.logger.Error("ack malformed message", "error", err)
				}
				continue
			}
			msg.ID = jmsg.Headers().Get(nats.MsgIdHdr)

			select {
			case ch <- &msg:
				if err := jmsg.Ack(); err != nil {
					b.logger.Error("ack ipc message", "error", err)
				}
			case <-ctx.Done():
				return
			case <-b.closedCh:
				return
			}
		}
	}()

	return ch
}
