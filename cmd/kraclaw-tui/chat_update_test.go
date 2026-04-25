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

func TestChatStatusTokens(t *testing.T) {
	cases := []struct {
		name             string
		waiting          bool
		wantStatusToken  string
		wantWaitingTitle bool
	}{
		{
			name:             "waiting shows Waiting status and title",
			waiting:          true,
			wantStatusToken:  "Status: Waiting",
			wantWaitingTitle: true,
		},
		{
			name:             "idle shows Idle status and no waiting title",
			waiting:          false,
			wantStatusToken:  "Status: Idle",
			wantWaitingTitle: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := initialModel("test", &apiClient{channels: &mockChannelClient{}})
			m.chatState = chatStateChatting
			m.chatGroup = &GroupInfo{JID: "chat:test", Name: "test"}
			m.chatWaitingForAgent = c.waiting
			m.width = 120

			statusBar := m.renderStatusBar()
			if !strings.Contains(statusBar, c.wantStatusToken) {
				t.Fatalf("expected status bar to contain %q, got %q", c.wantStatusToken, statusBar)
			}

			rendered := m.renderChat()
			containsWaiting := strings.Contains(rendered, "Waiting...")
			if containsWaiting != c.wantWaitingTitle {
				t.Fatalf("expected waiting title=%v, got %v", c.wantWaitingTitle, containsWaiting)
			}
		})
	}
}

func TestSelectModel_OpenAIBranchesToOAuth(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		provider     string
		authMode     string
		wantState    chatState
		wantProvider string // creationSelectedProvider stashed for OAuth
	}{
		"openai with chatgpt auth_mode branches to OAuth": {
			provider:     "openai",
			authMode:     "chatgpt",
			wantState:    chatStateOAuth,
			wantProvider: "openai",
		},
		"anthropic with api_key auth_mode skips OAuth": {
			provider:  "anthropic",
			authMode:  "api_key",
			wantState: chatStateConnecting,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := initialModel("test", &apiClient{channels: &mockChannelClient{}})
			m.chatState = chatStateSelectModel
			m.creationProviders = []*kraclawv1.ProviderInfo{
				{Id: tt.provider, AuthMode: tt.authMode, Models: []*kraclawv1.ModelInfo{{Id: "model-1"}}},
			}
			m.creationSelectedProvider = tt.provider
			m.creationPendingGroupName = "g1"
			m.creationPicker = creationPickerState{
				items: []creationPickerItem{{id: "model-1", label: "Model 1"}},
			}
			updated, _ := m.updateChat(keyPress("enter"))
			got := updated.(model)
			if got.chatState != tt.wantState {
				t.Errorf("provider=%q authMode=%q chatState = %v, want %v",
					tt.provider, tt.authMode, got.chatState, tt.wantState)
			}
			if tt.wantState == chatStateOAuth {
				if got.creationSelectedModelID != "model-1" {
					t.Errorf("provider=%q creationSelectedModelID = %q, want %q",
						tt.provider, got.creationSelectedModelID, "model-1")
				}
				if got.oauth.provider != tt.wantProvider {
					t.Errorf("provider=%q oauth.provider = %q, want %q",
						tt.provider, got.oauth.provider, tt.wantProvider)
				}
				if got.oauth.pendingGroupName != "g1" {
					t.Errorf("provider=%q oauth.pendingGroupName = %q, want %q",
						tt.provider, got.oauth.pendingGroupName, "g1")
				}
			}
		})
	}
}

func TestSidebarShowsModelAndProcessing(t *testing.T) {
	sidebar := newSidebarModel()
	sidebar.width = 30
	sidebar.height = 20
	sidebar.sessionID = "sess-123"
	sidebar.messageCount = 12
	sidebar.uptime = "2026-03-24 01:02:03"
	sidebar.model = "claude-3-7-sonnet-20250219"
	sidebar.processing = "Waiting"

	view := sidebar.View()
	if !strings.Contains(view, "Model") {
		t.Fatalf("expected sidebar to contain Model label, got %q", view)
	}
	if !strings.Contains(view, "Processing") {
		t.Fatalf("expected sidebar to contain Processing label, got %q", view)
	}
}
