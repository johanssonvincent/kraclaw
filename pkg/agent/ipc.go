package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
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

// IPCClient handles Redis Streams communication for a Go agent.
type IPCClient struct {
	rdb   redis.Cmdable
	group string
}

// NewIPCClient creates an IPC client for a specific group.
func NewIPCClient(rdb redis.Cmdable, group string) *IPCClient {
	return &IPCClient{rdb: rdb, group: group}
}

func (c *IPCClient) outputKey() string { return fmt.Sprintf("kraclaw:ipc:%s:output", c.group) }
func (c *IPCClient) inputKey() string  { return fmt.Sprintf("kraclaw:ipc:%s:input", c.group) }
func (c *IPCClient) closeKey() string  { return fmt.Sprintf("kraclaw:ipc:%s:close", c.group) }

// SendOutput publishes a message to the output stream (agent -> server).
func (c *IPCClient) SendOutput(ctx context.Context, msg *OutboundMessage) error {
	ipcMsg := map[string]interface{}{
		"group": c.group,
		"type":  msg.Type,
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

	if err := c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: c.outputKey(),
		Values: map[string]interface{}{"data": string(data)},
	}).Err(); err != nil {
		return fmt.Errorf("xadd output: %w", err)
	}

	_ = c.rdb.Publish(ctx, "kraclaw:ipc:notify", c.group).Err()
	return nil
}

// ReadInput returns a channel that receives messages from the input stream.
func (c *IPCClient) ReadInput(ctx context.Context) (<-chan *InboundMessage, error) {
	stream := c.inputKey()
	consumerGroup := "agent"
	consumer := "agent-0"

	err := c.rdb.XGroupCreateMkStream(ctx, stream, consumerGroup, "0").Err()
	if err != nil && !isGroupExistsErr(err) {
		return nil, fmt.Errorf("create consumer group: %w", err)
	}

	ch := make(chan *InboundMessage, 64)
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			results, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    consumerGroup,
				Consumer: consumer,
				Streams:  []string{stream, ">"},
				Count:    10,
				Block:    500 * time.Millisecond,
			}).Result()

			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}
				if errors.Is(err, redis.Nil) {
					continue
				}
				continue
			}

			for _, s := range results {
				for _, entry := range s.Messages {
					raw, ok := entry.Values["data"].(string)
					if !ok {
						continue
					}
					var ipcMsg struct {
						Type    string          `json:"type"`
						Payload json.RawMessage `json:"payload"`
					}
					if err := json.Unmarshal([]byte(raw), &ipcMsg); err != nil {
						continue
					}

					msg := &InboundMessage{
						Type:    ipcMsg.Type,
						Payload: ipcMsg.Payload,
					}

					select {
					case ch <- msg:
					case <-ctx.Done():
						return
					}

					_ = c.rdb.XAck(ctx, stream, consumerGroup, entry.ID).Err()
				}
			}
		}
	}()

	return ch, nil
}

// CheckCloseSignal checks whether the server has set the close signal.
func (c *IPCClient) CheckCloseSignal(ctx context.Context) (bool, error) {
	n, err := c.rdb.Exists(ctx, c.closeKey()).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func isGroupExistsErr(err error) bool {
	return err != nil && err.Error() == "BUSYGROUP Consumer Group name already exists"
}
