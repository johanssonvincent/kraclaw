package discord

import (
	"testing"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/channel"
)

func TestOwnsJID(t *testing.T) {
	tests := []struct {
		name string
		jid  string
		want bool
	}{
		{"discord jid", "discord:123456789", true},
		{"telegram jid", "telegram:123456789", false},
		{"empty jid", "", false},
		{"prefix only", "discord:", true},
		{"wrong prefix", "disc:123", false},
	}

	d := New("fake-token", channel.ChannelConfig{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.OwnsJID(tt.jid); got != tt.want {
				t.Errorf("OwnsJID(%q) = %v, want %v", tt.jid, got, tt.want)
			}
		})
	}
}

func TestName(t *testing.T) {
	d := New("fake-token", channel.ChannelConfig{})
	if got := d.Name(); got != "discord" {
		t.Errorf("Name() = %q, want %q", got, "discord")
	}
}

func TestIsConnectedDefault(t *testing.T) {
	d := New("fake-token", channel.ChannelConfig{})
	if d.IsConnected() {
		t.Error("IsConnected() = true for new instance, want false")
	}
}

func TestSendMessageNotConnected(t *testing.T) {
	d := New("fake-token", channel.ChannelConfig{})
	err := d.SendMessage(t.Context(), "discord:123", "hello")
	if err == nil {
		t.Error("SendMessage() on disconnected channel should return error")
	}
}

func TestSetTypingNotConnected(t *testing.T) {
	d := New("fake-token", channel.ChannelConfig{})
	err := d.SetTyping(t.Context(), "discord:123", true)
	if err == nil {
		t.Error("SetTyping(true) on disconnected channel should return error")
	}

	// typing=false should be a no-op.
	err = d.SetTyping(t.Context(), "discord:123", false)
	if err != nil {
		t.Errorf("SetTyping(false) should not error, got %v", err)
	}
}

func TestInboundMessageFormat(t *testing.T) {
	var received *channel.InboundMessage
	cfg := channel.ChannelConfig{
		OnMessage: func(chatJID string, msg *channel.InboundMessage) {
			received = msg
		},
	}

	msg := &channel.InboundMessage{
		ID:         "msg-1",
		ChatJID:    "discord:12345",
		Sender:     "user-1",
		SenderName: "testuser",
		Content:    "hello world",
		Timestamp:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		IsGroup:    true,
	}

	cfg.OnMessage(msg.ChatJID, msg)

	if received == nil {
		t.Fatal("OnMessage callback was not called")
	}
	if received.ChatJID != "discord:12345" {
		t.Errorf("ChatJID = %q, want %q", received.ChatJID, "discord:12345")
	}
	if received.SenderName != "testuser" {
		t.Errorf("SenderName = %q, want %q", received.SenderName, "testuser")
	}
	if !received.IsGroup {
		t.Error("IsGroup = false, want true")
	}
}
