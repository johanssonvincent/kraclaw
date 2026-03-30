package channel

import (
	"context"
	"fmt"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/store"
)

// InboundMessage represents a message received from a channel.
type InboundMessage struct {
	ID         string
	ChatJID    string
	Sender     string
	SenderName string
	Content    string
	Timestamp  time.Time
	IsGroup    bool
}

// Channel defines the interface that all messaging channels must implement.
type Channel interface {
	Name() string
	Connect(ctx context.Context) error
	SendMessage(ctx context.Context, jid string, text string) error
	IsConnected() bool
	OwnsJID(jid string) bool
	Disconnect(ctx context.Context) error
	SetTyping(ctx context.Context, jid string, typing bool) error
}

// ChannelConfig holds callbacks and dependencies for channel operation.
type ChannelConfig struct {
	OnMessage  func(chatJID string, msg *InboundMessage)
	OnChatMeta func(chatJID string, timestamp time.Time, name string, channel string, isGroup bool)
	Groups     func() []store.Group
}

// Validate checks that required callbacks are set.
func (c *ChannelConfig) Validate() error {
	if c.OnMessage == nil {
		return fmt.Errorf("OnMessage callback is required")
	}
	if c.Groups == nil {
		return fmt.Errorf("groups callback is required")
	}
	return nil
}

// Factory creates a Channel from a ChannelConfig.
type Factory func(cfg ChannelConfig) (Channel, error)
