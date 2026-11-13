package discord

import (
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
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

// newGatewaySession builds the minimum discordgo session the message handler
// touches: its own user identity, used for the self-message filter.
func newGatewaySession(botID string) *discordgo.Session {
	state := &discordgo.State{}
	state.User = &discordgo.User{ID: botID}

	return &discordgo.Session{State: state}
}

// TestHandleMessage_BeforeSessionIsPublished covers the startup window where the
// gateway dispatches a message before Connect has assigned d.session. The handler
// must still route the message instead of dereferencing a nil session.
func TestHandleMessage_BeforeSessionIsPublished(t *testing.T) {
	var got []string

	d := New("fake-token", channel.ChannelConfig{
		OnMessage: func(chatJID string, msg *channel.InboundMessage) {
			got = append(got, msg.ID)
		},
	})

	msg := &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "msg-1", ChannelID: "chan-1", Content: "hello",
		Author: &discordgo.User{ID: "user-1", Username: "someone"},
	}}

	d.handleMessage(newGatewaySession("bot-1"), msg)

	if len(got) != 1 || got[0] != "msg-1" {
		t.Fatalf("OnMessage calls = %v, want [msg-1]", got)
	}
}

// TestHandleMessage_SkipsOwnMessages pins the self-message filter against the
// dispatching session, which is the identity that is always available.
func TestHandleMessage_SkipsOwnMessages(t *testing.T) {
	called := false

	d := New("fake-token", channel.ChannelConfig{
		OnMessage: func(string, *channel.InboundMessage) { called = true },
	})

	msg := &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "msg-1", ChannelID: "chan-1", Content: "echo",
		Author: &discordgo.User{ID: "bot-1"},
	}}

	d.handleMessage(newGatewaySession("bot-1"), msg)

	if called {
		t.Error("OnMessage called for the bot's own message")
	}
}

// TestHandleMessage_NilPayloads makes sure a partially populated event (system
// messages and webhook posts carry no Author) does not panic the server.
func TestHandleMessage_NilPayloads(t *testing.T) {
	d := New("fake-token", channel.ChannelConfig{
		OnMessage: func(string, *channel.InboundMessage) {
			t.Error("OnMessage called for a payload with no author")
		},
	})

	session := newGatewaySession("bot-1")

	d.handleMessage(session, nil)
	d.handleMessage(session, &discordgo.MessageCreate{})
	d.handleMessage(session, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "msg-1"}})
}

// TestHandleMessage_NoSessionStillDelivers documents the fallback: with no
// session there is no bot identity to filter against, and dropping the message
// would lose real traffic, so it is delivered.
func TestHandleMessage_NoSessionStillDelivers(t *testing.T) {
	var got []string

	d := New("fake-token", channel.ChannelConfig{
		OnMessage: func(_ string, msg *channel.InboundMessage) {
			got = append(got, msg.ID)
		},
	})

	d.handleMessage(nil, &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "msg-2", ChannelID: "chan-1", Author: &discordgo.User{ID: "user-1"},
	}})

	if len(got) != 1 || got[0] != "msg-2" {
		t.Fatalf("OnMessage calls = %v, want [msg-2]", got)
	}
}

// TestHandleMessage_ConcurrentWithConnect covers the race between the gateway
// goroutine (handleMessage) and Connect publishing d.session: the handler used
// to read the field without d.mu.
func TestHandleMessage_ConcurrentWithConnect(t *testing.T) {
	d := New("fake-token", channel.ChannelConfig{
		OnMessage: func(string, *channel.InboundMessage) {},
	})
	session := newGatewaySession("bot-1")

	msg := &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "msg-1", ChannelID: "chan-1", Author: &discordgo.User{ID: "user-1"},
	}}

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; i < 200; i++ {
			d.mu.Lock()
			d.session = session
			d.mu.Unlock()
		}
	}()

	for i := 0; i < 200; i++ {
		d.handleMessage(nil, msg)
	}

	wg.Wait()
}
