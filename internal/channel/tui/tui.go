package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/channel"
)

// TUI implements the channel.Channel interface for the TUI client.
// It is a passive channel activated by gRPC connections rather than
// an external messaging platform.
type TUI struct {
	cfg       channel.ChannelConfig
	mu        sync.Mutex
	subs      map[string][]chan string
	connected bool
	log       *slog.Logger
}

// New creates a new TUI channel.
func New(log *slog.Logger) *TUI {
	return &TUI{
		connected: true,
		subs:      make(map[string][]chan string),
		log:       log,
	}
}

// Name returns the channel name.
func (t *TUI) Name() string {
	return "tui"
}

// Connect is a no-op for the TUI channel. It is a passive channel
// activated by gRPC connections.
func (t *TUI) Connect(_ context.Context) error {
	return nil
}

// IsConnected returns the current connection state.
func (t *TUI) IsConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected
}

// OwnsJID returns true if the JID belongs to the TUI channel.
func (t *TUI) OwnsJID(jid string) bool {
	return strings.HasPrefix(jid, "tui:")
}

// Disconnect sets connected to false and closes all subscriber channels.
func (t *TUI) Disconnect(_ context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.connected = false

	for jid, chans := range t.subs {
		for _, ch := range chans {
			close(ch)
		}
		delete(t.subs, jid)
	}

	return nil
}

// SetTyping is a no-op for the TUI channel.
func (t *TUI) SetTyping(_ context.Context, _ string, _ bool) error {
	return nil
}

// SendMessage sends text to all subscribers for the given JID.
// This is called by the router when agent output arrives.
func (t *TUI) SendMessage(_ context.Context, jid string, text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, ch := range t.subs[jid] {
		// Non-blocking send; skip full channels.
		select {
		case ch <- text:
		default:
			t.log.Warn("dropping message to full subscriber channel", "jid", jid)
		}
	}

	return nil
}

// HandleInbound processes an inbound message from the TUI client.
// This is NOT part of the Channel interface -- it is called by the
// gRPC handler. Returns an error if the channel config has not been set yet.
func (t *TUI) HandleInbound(jid, sender, senderName, content string) error {
	t.mu.Lock()
	onMessage := t.cfg.OnMessage
	onChatMeta := t.cfg.OnChatMeta
	t.mu.Unlock()

	if onMessage == nil {
		return fmt.Errorf("TUI channel not yet configured")
	}

	msg := &channel.InboundMessage{
		ID:         fmt.Sprintf("tui-%d", time.Now().UnixNano()),
		ChatJID:    jid,
		Sender:     sender,
		SenderName: senderName,
		Content:    content,
		Timestamp:  time.Now(),
		IsGroup:    false,
	}

	onMessage(jid, msg)
	if onChatMeta != nil {
		onChatMeta(jid, time.Now(), jid, "tui", false)
	}
	return nil
}

// Subscribe creates a buffered channel for receiving messages on the
// given JID. It returns the receive-only channel and an unsubscribe
// function that removes the channel from the subscriber map and closes it.
func (t *TUI) Subscribe(jid string) (<-chan string, func()) {
	ch := make(chan string, 64)

	t.mu.Lock()
	t.subs[jid] = append(t.subs[jid], ch)
	t.mu.Unlock()

	closed := false
	unsubscribe := func() {
		t.mu.Lock()
		defer t.mu.Unlock()

		if closed {
			return
		}

		chans := t.subs[jid]
		found := false
		for i, c := range chans {
			if c == ch {
				t.subs[jid] = append(chans[:i], chans[i+1:]...)
				found = true
				break
			}
		}
		// Only close if we found and removed the channel. If Disconnect
		// already closed it (and removed it from the map), skip the close
		// to avoid a double-close panic.
		if found {
			close(ch)
		}
		closed = true
	}

	return ch, unsubscribe
}

// SetConfig stores the channel configuration. This is needed because the
// TUI channel is created before the orchestrator sets up the ChannelConfig.
func (t *TUI) SetConfig(cfg channel.ChannelConfig) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cfg = cfg
}
