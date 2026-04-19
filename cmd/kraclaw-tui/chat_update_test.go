package main

import (
	"context"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc"

	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

type mockChannelClient struct {
	lastText string
}

func (m *mockChannelClient) ListChannels(context.Context, *kraclawv1.ListChannelsRequest, ...grpc.CallOption) (*kraclawv1.ListChannelsResponse, error) {
	return &kraclawv1.ListChannelsResponse{}, nil
}

func (m *mockChannelClient) SendMessage(_ context.Context, in *kraclawv1.SendMessageRequest, _ ...grpc.CallOption) (*kraclawv1.SendMessageResponse, error) {
	m.lastText = in.Text
	return &kraclawv1.SendMessageResponse{}, nil
}

func (m *mockChannelClient) StreamInbound(context.Context, *kraclawv1.StreamInboundRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[kraclawv1.InboundMessage], error) {
	return nil, nil
}

func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	case "esc":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	case "up":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyUp})
	case "down":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyDown})
	case "ctrl+m":
		return tea.KeyPressMsg(tea.Key{Code: 'm', Mod: tea.ModCtrl})
	default:
		return tea.KeyPressMsg(tea.Key{Text: s, Code: []rune(s)[0]})
	}
}

func TestUpdateChatModelPicker(t *testing.T) {
	channelClient := &mockChannelClient{}
	m := initialModel("test", &apiClient{channels: channelClient})
	m.chatState = chatStateChatting
	m.chatGroup = &GroupInfo{JID: "chat:test"}

	updated, _ := m.updateChat(keyPress("m"))
	m1 := updated.(model)
	if m1.modelPicker.Open {
		t.Fatal("expected model picker to stay closed on plain 'm'")
	}
	if channelClient.lastText != "" {
		t.Fatalf("expected no command sent on plain 'm', got %q", channelClient.lastText)
	}

	var cmd tea.Cmd
	updated, cmd = m1.updateChat(keyPress("ctrl+m"))
	m1 = updated.(model)

	if !m1.modelPicker.Open {
		t.Fatal("expected model picker to open on 'ctrl+m'")
	}
	if !m1.modelPicker.Loading {
		t.Fatal("expected model picker loading state to be true")
	}
	if cmd == nil {
		t.Fatal("expected /models command to be queued")
	}

	msg := cmd()
	if sent, ok := msg.(inputSentMsg); !ok || sent.err != nil {
		t.Fatalf("expected inputSentMsg without error, got %#v", msg)
	}
	if channelClient.lastText != "/models" {
		t.Fatalf("expected /models command, got %q", channelClient.lastText)
	}

	m1.modelPicker.Options = []modelOption{{ID: "claude-3-7-sonnet-20250219", Label: "claude-3-7-sonnet-20250219"}}
	m1.modelPicker.Cursor = 0
	updated, cmd = m1.updateChat(keyPress("enter"))
	m2 := updated.(model)

	if m2.modelPicker.Open {
		t.Fatal("expected picker to close after selecting model")
	}
	if cmd == nil {
		t.Fatal("expected /model command to be queued")
	}
	msg = cmd()
	if sent, ok := msg.(inputSentMsg); !ok || sent.err != nil {
		t.Fatalf("expected inputSentMsg without error, got %#v", msg)
	}
	if channelClient.lastText != "/model claude-3-7-sonnet-20250219" {
		t.Fatalf("expected /model command, got %q", channelClient.lastText)
	}
}

func TestWaitingStateTransitions(t *testing.T) {
	m := initialModel("test", &apiClient{channels: &mockChannelClient{}})
	m.chatState = chatStateChatting
	m.chatGroup = &GroupInfo{JID: "chat:test", Name: "test"}
	m.chatInput.SetValue("hello")

	updated, _ := m.updateChat(keyPress("enter"))
	m1 := updated.(model)
	if !m1.chatWaitingForAgent {
		t.Fatal("expected waiting state to be true after sending message")
	}

	updatedAny, _ := m1.Update(channelOutputMsg{msg: &kraclawv1.InboundMessage{Content: "hi there"}})
	m2 := updatedAny.(model)
	if m2.chatWaitingForAgent {
		t.Fatal("expected waiting state to be false after inbound content")
	}

	m2.chatWaitingForAgent = true
	updatedAny, _ = m2.Update(channelOutputMsg{err: io.EOF})
	m3 := updatedAny.(model)
	if m3.chatWaitingForAgent {
		t.Fatal("expected waiting state to be false after stream disconnect")
	}
}

func TestModelPickerEmptyState(t *testing.T) {
	cases := []struct {
		name           string
		content        string
		wantLoading    bool
		wantOptionsNil bool
		wantEmptyState bool
	}{
		{
			name:           "models header with no entries clears loading",
			content:        "Models:\n",
			wantLoading:    false,
			wantOptionsNil: false,
			wantEmptyState: true,
		},
		{
			name:           "models cached header with no entries clears loading",
			content:        "Models (cached):\n",
			wantLoading:    false,
			wantOptionsNil: false,
			wantEmptyState: true,
		},
		{
			name:           "non-model content leaves loading true",
			content:        "Hello there\n",
			wantLoading:    true,
			wantOptionsNil: true,
			wantEmptyState: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := initialModel("test", &apiClient{channels: &mockChannelClient{}})
			m.chatState = chatStateChatting
			m.chatGroup = &GroupInfo{JID: "chat:test", Name: "test"}
			m.modelPicker.Open = true
			m.modelPicker.Loading = true
			m.modelPicker.Options = nil

			updatedAny, _ := m.Update(channelOutputMsg{msg: &kraclawv1.InboundMessage{Content: c.content}})
			updated := updatedAny.(model)

			if updated.modelPicker.Loading != c.wantLoading {
				t.Fatalf("expected loading=%v, got %v", c.wantLoading, updated.modelPicker.Loading)
			}

			if c.wantOptionsNil {
				if updated.modelPicker.Options != nil {
					t.Fatalf("expected options to remain nil")
				}
			} else {
				if updated.modelPicker.Options == nil {
					t.Fatalf("expected options to be set to empty slice")
				}
				if len(updated.modelPicker.Options) != 0 {
					t.Fatalf("expected no options, got %d", len(updated.modelPicker.Options))
				}
			}

			if c.wantEmptyState {
				rendered := updated.renderChat()
				if !strings.Contains(rendered, "No models available") {
					t.Fatalf("expected empty-state heading, got %q", rendered)
				}
				if !strings.Contains(rendered, "Check your agent configuration or retry `/models` to load available models.") {
					t.Fatalf("expected empty-state body, got %q", rendered)
				}
			}
		})
	}
}

func TestChatProcessingIndicators(t *testing.T) {
	cases := []struct {
		name        string
		waiting     bool
		wantTyping  bool
	}{
		{
			name:       "waiting renders typing suffix in group header",
			waiting:    true,
			wantTyping: true,
		},
		{
			name:       "idle has no typing suffix",
			waiting:    false,
			wantTyping: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := initialModel("test", &apiClient{channels: &mockChannelClient{}})
			m.activeTab = tabMessages
			m.chatState = chatStateChatting
			m.chatGroup = &GroupInfo{JID: "chat:test", Name: "test"}
			m.chatWaitingForAgent = c.waiting
			m.width = 120
			m.height = 40

			statusBar := m.renderStatusBar()
			if !strings.Contains(statusBar, "kraclaw") {
				t.Fatalf("expected status bar to contain kraclaw brand, got %q", statusBar)
			}
			if !strings.Contains(statusBar, "grpc://test") {
				t.Fatalf("expected status bar to contain grpc URL, got %q", statusBar)
			}
			if !strings.Contains(statusBar, "theme:") {
				t.Fatalf("expected status bar to contain theme cell, got %q", statusBar)
			}

			rendered := m.renderChat()
			containsTyping := strings.Contains(rendered, "typing")
			if containsTyping != c.wantTyping {
				t.Fatalf("expected typing indicator=%v, got %v", c.wantTyping, containsTyping)
			}
		})
	}
}

func TestGroupHeaderShowsModelAndState(t *testing.T) {
	m := initialModel("test", &apiClient{channels: &mockChannelClient{}})
	m.activeTab = tabMessages
	m.chatState = chatStateChatting
	m.chatGroup = &GroupInfo{JID: "chat:test", Name: "test", Folder: "test"}
	m.chatModel = "claude-sonnet-4-6"
	m.sandboxes = []SandboxInfo{{Name: "sbx-abcd", GroupJID: "chat:test", State: "running"}}
	m.width = 120
	m.height = 40

	rendered := m.renderChat()
	if !strings.Contains(rendered, "groups ›") {
		t.Fatalf("expected group header breadcrumb, got %q", rendered)
	}
	if !strings.Contains(rendered, "claude-sonnet-4-6") {
		t.Fatalf("expected group header to show model, got %q", rendered)
	}
	if !strings.Contains(rendered, "sbx-abcd") {
		t.Fatalf("expected group header to show sandbox id, got %q", rendered)
	}
	if !strings.Contains(rendered, "running") {
		t.Fatalf("expected group header to show sandbox state, got %q", rendered)
	}
}
