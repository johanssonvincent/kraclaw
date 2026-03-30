package server

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/johanssonvincent/kraclaw/internal/channel"
	"github.com/johanssonvincent/kraclaw/internal/channel/tui"
	"github.com/johanssonvincent/kraclaw/internal/store"
	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

// --- mocks ---

type mockInboundStreamServer struct {
	ctx     context.Context
	sent    []*kraclawv1.InboundMessage
	sendErr error
}

func (m *mockInboundStreamServer) Send(msg *kraclawv1.InboundMessage) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sent = append(m.sent, msg)
	return nil
}

func (m *mockInboundStreamServer) Context() context.Context        { return m.ctx }
func (m *mockInboundStreamServer) SetHeader(metadata.MD) error     { return nil }
func (m *mockInboundStreamServer) SendHeader(metadata.MD) error    { return nil }
func (m *mockInboundStreamServer) SetTrailer(metadata.MD)          {}
func (m *mockInboundStreamServer) SendMsg(interface{}) error       { return nil }
func (m *mockInboundStreamServer) RecvMsg(interface{}) error       { return nil }

type mockChannel struct {
	name      string
	connected bool
}

func (m *mockChannel) Name() string                                              { return m.name }
func (m *mockChannel) Connect(_ context.Context) error                           { return nil }
func (m *mockChannel) SendMessage(_ context.Context, _ string, _ string) error   { return nil }
func (m *mockChannel) IsConnected() bool                                         { return m.connected }
func (m *mockChannel) OwnsJID(_ string) bool                                     { return false }
func (m *mockChannel) Disconnect(_ context.Context) error                        { return nil }
func (m *mockChannel) SetTyping(_ context.Context, _ string, _ bool) error       { return nil }

// --- SendMessage tests ---

func TestSendMessage_NilTUI(t *testing.T) {
	svc := &channelService{log: testLogger()}
	_, err := svc.SendMessage(context.Background(), &kraclawv1.SendMessageRequest{
		ChatJid: "tui:test",
		Text:    "hello",
	})
	if err == nil {
		t.Fatal("expected error for nil TUI")
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", status.Code(err))
	}
}

func TestSendMessage_MissingChatJid(t *testing.T) {
	tuiCh := tui.New(slog.Default())
	tuiCh.SetConfig(channel.ChannelConfig{
		OnMessage:  func(string, *channel.InboundMessage) {},
		OnChatMeta: func(string, time.Time, string, string, bool) {},
		Groups:     func() []store.Group { return nil },
	})
	svc := &channelService{tui: tuiCh, log: testLogger()}
	_, err := svc.SendMessage(context.Background(), &kraclawv1.SendMessageRequest{
		Text: "hello",
	})
	if err == nil {
		t.Fatal("expected error for missing chat_jid")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestSendMessage_MissingText(t *testing.T) {
	tuiCh := tui.New(slog.Default())
	tuiCh.SetConfig(channel.ChannelConfig{
		OnMessage:  func(string, *channel.InboundMessage) {},
		OnChatMeta: func(string, time.Time, string, string, bool) {},
		Groups:     func() []store.Group { return nil },
	})
	svc := &channelService{tui: tuiCh, log: testLogger()}
	_, err := svc.SendMessage(context.Background(), &kraclawv1.SendMessageRequest{
		ChatJid: "tui:test",
	})
	if err == nil {
		t.Fatal("expected error for missing text")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestSendMessage_BeforeConfig(t *testing.T) {
	tuiCh := tui.New(slog.Default())
	// Do NOT call SetConfig — simulate early request before orchestrator is ready
	svc := &channelService{tui: tuiCh, log: testLogger()}
	_, err := svc.SendMessage(context.Background(), &kraclawv1.SendMessageRequest{
		ChatJid: "tui:test",
		Text:    "hello",
	})
	if err == nil {
		t.Fatal("expected error when config not set")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

func TestSendMessage_TriggersHandleInbound(t *testing.T) {
	var gotJID string
	var gotMsg *channel.InboundMessage

	tuiCh := tui.New(slog.Default())
	tuiCh.SetConfig(channel.ChannelConfig{
		OnMessage: func(chatJID string, msg *channel.InboundMessage) {
			gotJID = chatJID
			gotMsg = msg
		},
		OnChatMeta: func(string, time.Time, string, string, bool) {},
		Groups:     func() []store.Group { return nil },
	})

	svc := &channelService{tui: tuiCh, log: testLogger()}
	resp, err := svc.SendMessage(context.Background(), &kraclawv1.SendMessageRequest{
		ChatJid:    "tui:mygroup",
		Sender:     "user1",
		SenderName: "User One",
		Text:       "hello agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if gotJID != "tui:mygroup" {
		t.Fatalf("OnMessage chatJID = %q, want %q", gotJID, "tui:mygroup")
	}
	if gotMsg == nil {
		t.Fatal("OnMessage was not called")
	}
	if gotMsg.Content != "hello agent" {
		t.Fatalf("OnMessage content = %q, want %q", gotMsg.Content, "hello agent")
	}
	if gotMsg.Sender != "user1" {
		t.Fatalf("OnMessage sender = %q, want %q", gotMsg.Sender, "user1")
	}
	if gotMsg.SenderName != "User One" {
		t.Fatalf("OnMessage senderName = %q, want %q", gotMsg.SenderName, "User One")
	}
}

// --- StreamInbound tests ---

func TestStreamInbound_NilTUI(t *testing.T) {
	svc := &channelService{log: testLogger()}
	stream := &mockInboundStreamServer{ctx: context.Background()}
	err := svc.StreamInbound(&kraclawv1.StreamInboundRequest{ChatJid: "tui:test"}, stream)
	if err == nil {
		t.Fatal("expected error for nil TUI")
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", status.Code(err))
	}
}

func TestStreamInbound_MissingChatJid(t *testing.T) {
	tuiCh := tui.New(slog.Default())
	svc := &channelService{tui: tuiCh, log: testLogger()}
	stream := &mockInboundStreamServer{ctx: context.Background()}
	err := svc.StreamInbound(&kraclawv1.StreamInboundRequest{}, stream)
	if err == nil {
		t.Fatal("expected error for missing chat_jid")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestStreamInbound_DeliversMessages(t *testing.T) {
	tuiCh := tui.New(slog.Default())
	svc := &channelService{tui: tuiCh, log: testLogger()}

	ctx, cancel := context.WithCancel(context.Background())
	stream := &mockInboundStreamServer{ctx: ctx}

	jid := "tui:testgroup"

	// Run StreamInbound in a goroutine since it blocks.
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.StreamInbound(&kraclawv1.StreamInboundRequest{ChatJid: jid}, stream)
	}()

	// Give the goroutine time to subscribe.
	time.Sleep(50 * time.Millisecond)

	// Send a message via the TUI's SendMessage (simulating agent output).
	if err := tuiCh.SendMessage(context.Background(), jid, "hello from agent"); err != nil {
		t.Fatalf("SendMessage error: %v", err)
	}

	// Give the stream time to deliver.
	time.Sleep(50 * time.Millisecond)

	// Cancel the context to stop the stream.
	cancel()

	err := <-errCh
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(stream.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(stream.sent))
	}
	if stream.sent[0].Content != "hello from agent" {
		t.Fatalf("content = %q, want %q", stream.sent[0].Content, "hello from agent")
	}
	if stream.sent[0].ChatJid != jid {
		t.Fatalf("chat_jid = %q, want %q", stream.sent[0].ChatJid, jid)
	}
	if stream.sent[0].Channel != "tui" {
		t.Fatalf("channel = %q, want %q", stream.sent[0].Channel, "tui")
	}
}

// --- ListChannels tests ---

func TestListChannels_Empty(t *testing.T) {
	svc := &channelService{log: testLogger()}
	resp, err := svc.ListChannels(context.Background(), &kraclawv1.ListChannelsRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Channels) != 0 {
		t.Fatalf("expected 0 channels, got %d", len(resp.Channels))
	}
}

func TestListChannels_WithChannels(t *testing.T) {
	svc := &channelService{
		channels: []channel.Channel{
			&mockChannel{name: "discord", connected: true},
			&mockChannel{name: "telegram", connected: false},
		},
		log: testLogger(),
	}

	resp, err := svc.ListChannels(context.Background(), &kraclawv1.ListChannelsRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(resp.Channels))
	}

	if resp.Channels[0].Name != "discord" {
		t.Fatalf("channels[0].Name = %q, want %q", resp.Channels[0].Name, "discord")
	}
	if !resp.Channels[0].Connected {
		t.Fatal("channels[0].Connected = false, want true")
	}
	if resp.Channels[1].Name != "telegram" {
		t.Fatalf("channels[1].Name = %q, want %q", resp.Channels[1].Name, "telegram")
	}
	if resp.Channels[1].Connected {
		t.Fatal("channels[1].Connected = true, want false")
	}
}

func TestStreamInbound_SendError(t *testing.T) {
	tuiCh := tui.New(slog.Default())
	tuiCh.SetConfig(channel.ChannelConfig{
		OnMessage:  func(string, *channel.InboundMessage) {},
		OnChatMeta: func(string, time.Time, string, string, bool) {},
		Groups:     func() []store.Group { return nil },
	})
	svc := &channelService{tui: tuiCh, log: testLogger()}

	jid := "tui:err-test"
	stream := &mockInboundStreamServer{
		ctx:     context.Background(),
		sendErr: fmt.Errorf("broken pipe"),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.StreamInbound(&kraclawv1.StreamInboundRequest{ChatJid: jid}, stream)
	}()

	// Give the goroutine time to subscribe.
	time.Sleep(10 * time.Millisecond)

	// Push a message to trigger stream.Send, which will return the error.
	if err := tuiCh.SendMessage(context.Background(), jid, "hello"); err != nil {
		t.Fatalf("SendMessage error: %v", err)
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from StreamInbound when Send fails")
		}
		if err.Error() != "broken pipe" {
			t.Fatalf("error = %q, want %q", err.Error(), "broken pipe")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for StreamInbound to return")
	}
}

func TestStreamInbound_ChannelClosed(t *testing.T) {
	tuiCh := tui.New(slog.Default())
	tuiCh.SetConfig(channel.ChannelConfig{
		OnMessage:  func(string, *channel.InboundMessage) {},
		OnChatMeta: func(string, time.Time, string, string, bool) {},
		Groups:     func() []store.Group { return nil },
	})
	svc := &channelService{tui: tuiCh, log: testLogger()}

	jid := "tui:close-test"
	stream := &mockInboundStreamServer{ctx: context.Background()}

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.StreamInbound(&kraclawv1.StreamInboundRequest{ChatJid: jid}, stream)
	}()

	// Give the goroutine time to subscribe.
	time.Sleep(10 * time.Millisecond)

	// Disconnect closes all subscriber channels.
	if err := tuiCh.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect error: %v", err)
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error when channel is closed")
		}
		if status.Code(err) != codes.Aborted {
			t.Fatalf("expected Aborted, got %v", status.Code(err))
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for StreamInbound to return")
	}
}
