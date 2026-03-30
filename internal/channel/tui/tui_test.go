package tui

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/channel"
)

func newTestTUI() *TUI {
	return New(slog.Default())
}

func TestOwnsJID(t *testing.T) {
	tt := []struct {
		name string
		jid  string
		want bool
	}{
		{"tui prefix", "tui:foo", true},
		{"discord prefix", "discord:foo", false},
		{"empty string", "", false},
	}

	tui := newTestTUI()
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			if got := tui.OwnsJID(tc.jid); got != tc.want {
				t.Errorf("OwnsJID(%q) = %v, want %v", tc.jid, got, tc.want)
			}
		})
	}
}

func TestHandleInbound_TriggersOnMessage(t *testing.T) {
	tui := newTestTUI()

	var gotJID string
	var gotMsg *channel.InboundMessage

	tui.SetConfig(channel.ChannelConfig{
		OnMessage: func(chatJID string, msg *channel.InboundMessage) {
			gotJID = chatJID
			gotMsg = msg
		},
		OnChatMeta: func(string, time.Time, string, string, bool) {},
	})

	if err := tui.HandleInbound("tui:abc", "user1", "Alice", "hello world"); err != nil {
		t.Fatalf("HandleInbound returned error: %v", err)
	}

	if gotJID != "tui:abc" {
		t.Errorf("OnMessage chatJID = %q, want %q", gotJID, "tui:abc")
	}
	if gotMsg == nil {
		t.Fatal("OnMessage was not called")
	}
	if gotMsg.Sender != "user1" {
		t.Errorf("msg.Sender = %q, want %q", gotMsg.Sender, "user1")
	}
	if gotMsg.SenderName != "Alice" {
		t.Errorf("msg.SenderName = %q, want %q", gotMsg.SenderName, "Alice")
	}
	if gotMsg.Content != "hello world" {
		t.Errorf("msg.Content = %q, want %q", gotMsg.Content, "hello world")
	}
	if gotMsg.ChatJID != "tui:abc" {
		t.Errorf("msg.ChatJID = %q, want %q", gotMsg.ChatJID, "tui:abc")
	}
}

func TestHandleInbound_TriggersOnChatMeta(t *testing.T) {
	tui := newTestTUI()

	var gotChannel string
	var called bool

	tui.SetConfig(channel.ChannelConfig{
		OnMessage: func(string, *channel.InboundMessage) {},
		OnChatMeta: func(_ string, _ time.Time, _ string, ch string, _ bool) {
			called = true
			gotChannel = ch
		},
	})

	if err := tui.HandleInbound("tui:abc", "user1", "Alice", "hi"); err != nil {
		t.Fatalf("HandleInbound returned error: %v", err)
	}

	if !called {
		t.Fatal("OnChatMeta was not called")
	}
	if gotChannel != "tui" {
		t.Errorf("OnChatMeta channel = %q, want %q", gotChannel, "tui")
	}
}

func TestHandleInbound_BeforeSetConfig(t *testing.T) {
	tui := newTestTUI()

	err := tui.HandleInbound("tui:abc", "user1", "Alice", "hello")
	if err == nil {
		t.Fatal("expected error when HandleInbound called before SetConfig")
	}
}

func TestSendMessage_DeliversToSubscribers(t *testing.T) {
	tui := newTestTUI()
	ch, unsub := tui.Subscribe("tui:xyz")
	defer unsub()

	err := tui.SendMessage(context.Background(), "tui:xyz", "hello")
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}

	select {
	case got := <-ch:
		if got != "hello" {
			t.Errorf("received %q, want %q", got, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestSendMessage_NoSubscribers(t *testing.T) {
	tui := newTestTUI()

	err := tui.SendMessage(context.Background(), "tui:nobody", "hello")
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
}

func TestSubscribeUnsubscribe(t *testing.T) {
	tui := newTestTUI()
	ch, unsub := tui.Subscribe("tui:abc")

	unsub()

	// Channel should be closed after unsubscribe.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after unsubscribe")
	}
}

func TestMultipleSubscribers(t *testing.T) {
	tui := newTestTUI()
	ch1, unsub1 := tui.Subscribe("tui:multi")
	defer unsub1()
	ch2, unsub2 := tui.Subscribe("tui:multi")
	defer unsub2()

	err := tui.SendMessage(context.Background(), "tui:multi", "broadcast")
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}

	for i, ch := range []<-chan string{ch1, ch2} {
		select {
		case got := <-ch:
			if got != "broadcast" {
				t.Errorf("subscriber %d received %q, want %q", i, got, "broadcast")
			}
		case <-time.After(time.Second):
			t.Errorf("subscriber %d timed out waiting for message", i)
		}
	}
}

func TestDisconnect_ClosesSubscribers(t *testing.T) {
	tui := newTestTUI()
	ch1, _ := tui.Subscribe("tui:a")
	ch2, _ := tui.Subscribe("tui:b")

	err := tui.Disconnect(context.Background())
	if err != nil {
		t.Fatalf("Disconnect returned error: %v", err)
	}

	for i, ch := range []<-chan string{ch1, ch2} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("subscriber %d: expected channel to be closed", i)
			}
		case <-time.After(time.Second):
			t.Errorf("subscriber %d: timed out waiting for channel close", i)
		}
	}
}

func TestDoubleUnsubscribe(t *testing.T) {
	tui := newTestTUI()
	_, unsub := tui.Subscribe("tui:double")

	unsub()
	unsub() // second call must not panic due to closed flag
}

func TestSendMessage_FullBufferDrops(t *testing.T) {
	tui := newTestTUI()
	ch, unsub := tui.Subscribe("tui:full")
	defer unsub()

	ctx := context.Background()
	for i := 0; i < 70; i++ {
		if err := tui.SendMessage(ctx, "tui:full", "msg"); err != nil {
			t.Fatalf("SendMessage returned error on message %d: %v", i, err)
		}
	}

	var count int
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count > 64 {
		t.Errorf("drained %d messages, want at most 64", count)
	}
}

func TestIsConnected_AfterDisconnect(t *testing.T) {
	tui := newTestTUI()

	if !tui.IsConnected() {
		t.Fatal("expected IsConnected() = true after creation")
	}

	if err := tui.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect returned error: %v", err)
	}

	if tui.IsConnected() {
		t.Fatal("expected IsConnected() = false after Disconnect")
	}
}
