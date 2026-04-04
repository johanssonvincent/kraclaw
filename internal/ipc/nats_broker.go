package ipc

import (
	"context"
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

// sanitizeGroupID is an unexported wrapper around SanitizeGroupID so internal
// callers remain unchanged.
func sanitizeGroupID(groupJID string) string { return SanitizeGroupID(groupJID) }

func ipcStreamName(sanitized string) string {
	return "KRACLAW_IPC_" + strings.ToUpper(sanitized)
}

func ipcInputSubject(sanitized, agentID string) string {
	return "kraclaw.ipc." + sanitized + "." + SanitizeAgentID(agentID) + ".input"
}

func ipcOutputSubject(sanitized, agentID string) string {
	return "kraclaw.ipc." + sanitized + "." + SanitizeAgentID(agentID) + ".output"
}

func ipcOutputWildcard(sanitized string) string {
	return "kraclaw.ipc." + sanitized + ".*.output"
}

// consumerCleanup pairs a context cancel function with its associated message iterator.
// This ensures cleanup happens in the correct order: cancel context first (so goroutines
// see ctx.Err() != nil), then stop iterator. Pairing them in a struct prevents accidental
// out-of-order cleanup that could cause spurious error logs.
type consumerCleanup struct {
	cancel context.CancelFunc
	iter   jetstream.MessagesContext
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
	cleanups []consumerCleanup // paired cancel + iterator for ordered cleanup
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
		Retention: jetstream.LimitsPolicy,
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
		return fmt.Errorf("publish output: %w", err)
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
		return fmt.Errorf("send input: %w", err)
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
		return nil, fmt.Errorf("subscribe output: %w", err)
	}
	streamName := ipcStreamName(sanitized)
	cons, err := b.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       "kraclaw-server-" + sanitized,
		FilterSubject: ipcOutputWildcard(sanitized),
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create output consumer: %w", err)
	}
	ch, err := b.consume(ctx, cons, group)
	if err != nil {
		return nil, fmt.Errorf("consume output: %w", err)
	}
	return ch, nil
}

// ReadInput returns a channel that receives input messages for a specific agent.
func (b *NATSBroker) ReadInput(ctx context.Context, group, agentID string) (<-chan *IPCMessage, error) {
	sanitized, err := b.ensureStream(ctx, group)
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	streamName := ipcStreamName(sanitized)
	cons, err := b.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       "agent-" + SanitizeAgentID(agentID),
		FilterSubject: ipcInputSubject(sanitized, agentID),
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create input consumer: %w", err)
	}
	ch, err := b.consume(ctx, cons, group)
	if err != nil {
		return nil, fmt.Errorf("consume input: %w", err)
	}
	return ch, nil
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
	// Cancel all contexts first — so goroutines see ctx.Err() != nil when iter.Stop() fires.
	for _, cleanup := range b.cleanups {
		cleanup.cancel()
	}
	// Then stop all iterators — this unblocks iter.Next(), which returns a context error (silent).
	for _, cleanup := range b.cleanups {
		cleanup.iter.Stop()
	}
	b.cleanups = nil
	close(b.closedCh)
	return nil
}

// consume creates a goroutine that drains a JetStream consumer into a channel.
func (b *NATSBroker) consume(ctx context.Context, cons jetstream.Consumer, group string) (<-chan *IPCMessage, error) {
	ch := make(chan *IPCMessage, 64)

	ctx, cancel := context.WithCancel(ctx)

	iter, err := cons.Messages()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create message iterator for group %s: %w", group, err)
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		iter.Stop()
		cancel()
		return nil, fmt.Errorf("consume: broker closed")
	}
	b.cleanups = append(b.cleanups, consumerCleanup{cancel: cancel, iter: iter})
	b.mu.Unlock()

	done := make(chan struct{}) // closed when the consumer goroutine exits

	// Stop the iterator when ctx is cancelled externally or when the consumer
	// goroutine exits from an iter.Next() error. Without the done case, the
	// watcher would block forever if the consumer exits before ctx is done —
	// a goroutine leak per failed consumer.
	go func() {
		select {
		case <-ctx.Done():
			iter.Stop()
		case <-b.closedCh:
			// iter.Stop() already called by Close().
		case <-done:
			// Consumer exited normally (iter error); nothing to do.
		}
	}()

	go func() {
		defer close(done)
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
				b.logger.Error("ipc message iterator error", "group", group, "error", err)
				return
			}

			var msg IPCMessage
			if err := json.Unmarshal(jmsg.Data(), &msg); err != nil {
				meta, _ := jmsg.Metadata()
				var seq uint64
				if meta != nil {
					seq = meta.Sequence.Stream
				}
				b.logger.Error("unmarshal ipc message", "group", group, "sequence", seq, "error", err)
				if err := jmsg.Ack(); err != nil {
					b.logger.Error("ack malformed message", "group", group, "sequence", seq, "error", err)
				}
				continue
			}
			msg.ID = jmsg.Headers().Get(nats.MsgIdHdr)

			select {
			case ch <- &msg:
				if err := jmsg.Ack(); err != nil {
					meta, _ := jmsg.Metadata()
					var seq uint64
					if meta != nil {
						seq = meta.Sequence.Stream
					}
					b.logger.Error("ack ipc message", "group", group, "sequence", seq, "error", err)
				}
			case <-ctx.Done():
				meta, _ := jmsg.Metadata()
				var seq uint64
				if meta != nil {
					seq = meta.Sequence.Stream
				}
				if err := jmsg.Nak(); err != nil {
					b.logger.Error("nak message on context cancel", "group", group, "sequence", seq, "error", err)
				}
				return
			case <-b.closedCh:
				meta, _ := jmsg.Metadata()
				var seq uint64
				if meta != nil {
					seq = meta.Sequence.Stream
				}
				if err := jmsg.Nak(); err != nil {
					b.logger.Error("nak message on broker close", "group", group, "sequence", seq, "error", err)
				}
				return
			}
		}
	}()

	return ch, nil
}
