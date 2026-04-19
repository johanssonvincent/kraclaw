package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	// ipc is imported for SanitizeGroupID to avoid duplicating the hash logic.
	// No cycle: internal/ipc does not import pkg/agent.
	"github.com/johanssonvincent/kraclaw/internal/ipc"
)

// InboundMessage is a message received from the server.
type InboundMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// OutboundMessage is a message sent from the agent to the server.
type OutboundMessage struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// IPCClient handles NATS JetStream communication for a Go agent.
type IPCClient struct {
	nc      *nats.Conn
	js      jetstream.JetStream
	group   string // group folder (used for sanitization to match server)
	agentID string
	logger  *slog.Logger

	readOnce sync.Once
	msgCh    chan *InboundMessage
	errCh    chan error
	readErr  error

	mu            sync.Mutex
	streamCreated bool
}

// NewIPCClient creates an IPC client for a specific group.
// The group parameter must be the group folder (not the JID), matching the
// value the server uses when computing sanitized stream names and subjects.
func NewIPCClient(nc *nats.Conn, group, agentID string, logger *slog.Logger) (*IPCClient, error) {
	if nc == nil {
		return nil, fmt.Errorf("ipc client: NATS connection is required")
	}
	if group == "" {
		return nil, fmt.Errorf("ipc client: group is required")
	}
	if agentID == "" {
		agentID = ipc.DefaultAgentID
	}
	if logger == nil {
		logger = slog.Default()
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("ipc client: jetstream: %w", err)
	}
	return &IPCClient{
		nc:      nc,
		js:      js,
		group:   group,
		agentID: agentID,
		logger:  logger,
	}, nil
}

// sanitizeGroupID delegates to ipc.SanitizeGroupID so tests in this package
// and internal callers can use the unexported name without duplicating logic.
func sanitizeGroupID(group string) string { return ipc.SanitizeGroupID(group) }

func (c *IPCClient) sanitized() string { return sanitizeGroupID(c.group) }

func (c *IPCClient) streamName() string {
	return "KRACLAW_IPC_" + strings.ToUpper(c.sanitized())
}

func (c *IPCClient) inputSubject() string {
	return "kraclaw.ipc." + c.sanitized() + "." + ipc.SanitizeAgentID(c.agentID) + ".input"
}

func (c *IPCClient) outputSubject() string {
	return "kraclaw.ipc." + c.sanitized() + "." + ipc.SanitizeAgentID(c.agentID) + ".output"
}

// ensureStream creates the IPC stream if it does not exist.
// The server creates it first, but the agent calls this defensively.
func (c *IPCClient) ensureStream(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.streamCreated {
		return nil
	}
	sanitized := c.sanitized()
	if _, err := c.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: c.streamName(),
		Subjects: []string{
			"kraclaw.ipc." + sanitized + ".*.input",
			"kraclaw.ipc." + sanitized + ".*.output",
		},
		Retention: jetstream.LimitsPolicy, // must match NATSBroker
		Storage:   jetstream.FileStorage,
		MaxAge:    ipc.StreamMaxAge,
		Replicas:  1,
	}); err != nil {
		return fmt.Errorf("ensure ipc stream %s: %w", c.streamName(), err)
	}
	c.streamCreated = true
	return nil
}

// SendOutput publishes a message from this agent to the server.
func (c *IPCClient) SendOutput(ctx context.Context, msg *OutboundMessage) error {
	if err := c.ensureStream(ctx); err != nil {
		return fmt.Errorf("send output: %w", err)
	}

	ipcMsg := map[string]interface{}{
		"group":    c.group,
		"agent_id": c.agentID,
		"type":     msg.Type,
	}
	if msg.Text != "" {
		payload, err := json.Marshal(map[string]string{"text": msg.Text})
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
		ipcMsg["payload"] = json.RawMessage(payload)
	}
	data, err := json.Marshal(ipcMsg)
	if err != nil {
		return fmt.Errorf("marshal ipc message: %w", err)
	}
	if _, err := c.js.Publish(ctx, c.outputSubject(), data); err != nil {
		return fmt.Errorf("publish output: %w", err)
	}
	return nil
}

// ReadInput initialises the background reader on the first call and returns
// the same channels on all subsequent calls.  The ctx passed to the FIRST
// call must be long-lived (process lifetime) because it governs the reader
// goroutine: if that ctx is already cancelled when the first call arrives,
// startReadInput returns immediately and all subsequent callers see a closed
// channel with no error.
//
// The returned errCh (capacity 1) receives the terminal error when the reader
// goroutine exits due to an ACK failure or iterator error.  After receiving an
// error the caller should create a new IPCClient and call ReadInput again —
// sync.Once prevents re-initialising the reader on the same client instance.
func (c *IPCClient) ReadInput(ctx context.Context) (<-chan *InboundMessage, <-chan error, error) {
	c.readOnce.Do(func() {
		c.msgCh = make(chan *InboundMessage, 64)
		c.errCh = make(chan error, 1)
		c.readErr = c.startReadInput(ctx, c.msgCh, c.errCh)
		if c.readErr != nil {
			c.msgCh = nil
			c.errCh = nil
		}
	})
	if c.readErr != nil {
		return nil, nil, c.readErr
	}
	return c.msgCh, c.errCh, nil
}

// startReadInput initializes the message reader goroutine.
func (c *IPCClient) startReadInput(ctx context.Context, ch chan *InboundMessage, errCh chan error) error {
	if err := c.ensureStream(ctx); err != nil {
		return fmt.Errorf("read input: %w", err)
	}

	cons, err := c.js.CreateOrUpdateConsumer(ctx, c.streamName(), jetstream.ConsumerConfig{
		Durable:       "agent-" + ipc.SanitizeAgentID(c.agentID),
		FilterSubject: c.inputSubject(),
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return fmt.Errorf("create input consumer: %w", err)
	}

	go func() {
		defer close(ch)
		defer close(errCh)

		iter, err := cons.Messages()
		if err != nil {
			errCh <- fmt.Errorf("create message iterator: %w", err)
			return
		}
		defer iter.Stop()

		done := make(chan struct{}) // closed when the consumer goroutine exits
		// The watcher goroutine below relies on defer close(done) to detect
		// consumer exit before ctx is done — do not remove the defer.
		go func() {
			select {
			case <-ctx.Done():
				iter.Stop()
			case <-done:
				// Consumer exited (iter error); watcher can exit too.
			}
		}()
		defer close(done)

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			jmsg, err := iter.Next()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				c.logger.Error("ipc read goroutine terminating",
					"group", c.group,
					"agent_id", c.agentID,
					"error", err)
				errCh <- fmt.Errorf("ipc read: %w", err)
				return
			}

			var ipcMsg struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(jmsg.Data(), &ipcMsg); err != nil {
				meta, _ := jmsg.Metadata()
				var seq uint64
				if meta != nil {
					seq = meta.Sequence.Stream
				}
				c.logger.Error("unmarshal ipc message",
					"group", c.group,
					"agent_id", c.agentID,
					"sequence", seq,
					"error", err)
				if err := jmsg.Ack(); err != nil {
					c.logger.Error("ack malformed message",
						"group", c.group,
						"agent_id", c.agentID,
						"sequence", seq,
						"error", err)
				}
				continue
			}

			msg := &InboundMessage{Type: ipcMsg.Type, Payload: ipcMsg.Payload}

			select {
			case ch <- msg:
				if err := jmsg.Ack(); err != nil {
					meta, _ := jmsg.Metadata()
					var seq uint64
					if meta != nil {
						seq = meta.Sequence.Stream
					}
					c.logger.Error("ack ipc message",
						"group", c.group,
						"agent_id", c.agentID,
						"sequence", seq,
						"error", err)
					errCh <- fmt.Errorf("ack ipc message: %w", err)
					return
				}
			case <-ctx.Done():
				if err := jmsg.Nak(); err != nil {
					meta, _ := jmsg.Metadata()
					var seq uint64
					if meta != nil {
						seq = meta.Sequence.Stream
					}
					c.logger.Error("nak message on context cancel",
						"group", c.group,
						"agent_id", c.agentID,
						"sequence", seq,
						"error", err)
				}
				return
			}
		}
	}()

	return nil
}
