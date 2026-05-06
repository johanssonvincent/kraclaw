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
	case "ctrl+t":
		return tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl})
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

func TestStreamOpenedReflowsChatComposerIntoView(t *testing.T) {
	m := initialModel("test", &apiClient{channels: &mockChannelClient{}})
	m.chatGroup = &GroupInfo{JID: "chat:test", Name: "test"}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(model)

	updated, _ = m.Update(streamOpenedMsg{})
	m = updated.(model)
	out := m.View().Content
	if !strings.Contains(out, "markdown · 0/4096") {
		t.Fatalf("chat composer should be visible immediately after stream opens:\n%s", out)
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
		name       string
		waiting    bool
		wantTyping bool
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

func TestSelectProvider_OpenAIBranchesToOAuthBeforeModel(t *testing.T) {
	t.Parallel()
	m := initialModel("test", &apiClient{channels: &mockChannelClient{}})
	m.chatState = chatStateSelectProvider
	m.creationProvidersLoaded = true
	m.creationPendingGroupName = "g1"
	m.creationProviders = []*kraclawv1.ProviderInfo{
		{Id: "openai", AuthMode: "chatgpt", Models: []*kraclawv1.ModelInfo{{Id: "model-1"}}},
	}
	m.creationPicker = creationPickerState{items: []creationPickerItem{{id: "openai", label: "OpenAI"}}}

	updated, _ := m.updateChat(keyPress("enter"))
	got := updated.(model)
	if got.chatState != chatStateOAuth {
		t.Fatalf("chatState = %v, want %v", got.chatState, chatStateOAuth)
	}
	if got.creationSelectedProvider != "openai" {
		t.Fatalf("creationSelectedProvider = %q, want openai", got.creationSelectedProvider)
	}
	if got.creationSelectedModelID != "" {
		t.Fatalf("creationSelectedModelID = %q, want empty before model selection", got.creationSelectedModelID)
	}
	if got.oauth.provider != "openai" || got.oauth.groupJID != "tui:g1" || got.oauth.pendingGroupName != "g1" {
		t.Fatalf("oauth = %+v, want openai tui:g1 pending g1", got.oauth)
	}
}

func TestSelectModel_APIKeyProviderRegistersDirectly(t *testing.T) {
	t.Parallel()
	m := initialModel("test", &apiClient{channels: &mockChannelClient{}})
	m.chatState = chatStateSelectModel
	m.creationProviders = []*kraclawv1.ProviderInfo{
		{Id: "anthropic", AuthMode: "api_key", Models: []*kraclawv1.ModelInfo{{Id: "model-1"}}},
	}
	m.creationSelectedProvider = "anthropic"
	m.creationPendingGroupName = "g1"
	m.creationPicker = creationPickerState{items: []creationPickerItem{{id: "model-1", label: "Model 1"}}}

	updated, _ := m.updateChat(keyPress("enter"))
	got := updated.(model)
	if got.chatState != chatStateConnecting {
		t.Errorf("chatState = %v, want %v", got.chatState, chatStateConnecting)
	}
	if got.creationSelectedModelID != "model-1" {
		t.Errorf("creationSelectedModelID = %q, want model-1", got.creationSelectedModelID)
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

func TestEscOnOAuth_RoutesByFlow(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		startState       chatState
		pendingGroupName string
		chatGroup        *GroupInfo // nil for new-group flow
		wantState        chatState
		// creation fields to pre-populate (only set in the regression case)
		creationPendingGroupName string
		creationSelectedProvider string
		creationSelectedModelID  string
		creationProviders        []*kraclawv1.ProviderInfo
		creationProvidersLoaded  bool
		creationPicker           creationPickerState
	}{
		"new-group flow returns to selectGroup": {
			startState:       chatStateOAuth,
			pendingGroupName: "g-new",
			wantState:        chatStateSelectGroup,
		},
		"re-auth flow returns to chatting": {
			startState:       chatStateOAuth,
			pendingGroupName: "",
			chatGroup:        &GroupInfo{JID: "tui:existing", Name: "existing"},
			wantState:        chatStateChatting,
		},
		"new-group flow clears creation state": {
			startState:               chatStateOAuth,
			pendingGroupName:         "g-stale",
			wantState:                chatStateSelectGroup,
			creationPendingGroupName: "g-stale",
			creationSelectedProvider: "openai",
			creationSelectedModelID:  "gpt-4o",
			creationProviders:        []*kraclawv1.ProviderInfo{{Id: "openai"}},
			creationProvidersLoaded:  true,
			creationPicker: creationPickerState{
				items: []creationPickerItem{{id: "gpt-4o", label: "GPT-4o"}},
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := model{
				chatState:                tt.startState,
				oauth:                    oauthState{pendingGroupName: tt.pendingGroupName},
				chatGroup:                tt.chatGroup,
				creationPendingGroupName: tt.creationPendingGroupName,
				creationSelectedProvider: tt.creationSelectedProvider,
				creationSelectedModelID:  tt.creationSelectedModelID,
				creationProviders:        tt.creationProviders,
				creationProvidersLoaded:  tt.creationProvidersLoaded,
				creationPicker:           tt.creationPicker,
			}
			got, _ := m.handleEscOAuth()
			gm := got.(model)
			if gm.chatState != tt.wantState {
				t.Errorf("Esc on oauth (pending=%q) → state %v, want %v", tt.pendingGroupName, gm.chatState, tt.wantState)
			}
			// oauthState is not directly comparable (holds func/interface fields),
			// so check the scalar fields that must be zero after a cancel.
			if gm.oauth.pendingGroupName != "" || gm.oauth.userCode != "" || gm.oauth.err != nil {
				t.Errorf("oauth not cleared: pending=%q userCode=%q err=%v",
					gm.oauth.pendingGroupName, gm.oauth.userCode, gm.oauth.err)
			}
			// All six creation fields must be zeroed regardless of flow to prevent
			// stale context surviving cancel + re-entry.
			if gm.creationPendingGroupName != "" {
				t.Errorf("creationPendingGroupName = %q, want %q", gm.creationPendingGroupName, "")
			}
			if gm.creationSelectedProvider != "" {
				t.Errorf("creationSelectedProvider = %q, want %q", gm.creationSelectedProvider, "")
			}
			if gm.creationSelectedModelID != "" {
				t.Errorf("creationSelectedModelID = %q, want %q", gm.creationSelectedModelID, "")
			}
			if len(gm.creationPicker.items) != 0 {
				t.Errorf("creationPicker.items len = %d, want 0", len(gm.creationPicker.items))
			}
			if gm.creationProviders != nil {
				t.Errorf("creationProviders = %v, want nil", gm.creationProviders)
			}
			if gm.creationProvidersLoaded {
				t.Errorf("creationProvidersLoaded = true, want false")
			}
		})
	}
}

func TestOAuthSuccess_NewOpenAIGroupFetchesModelsBeforeRegister(t *testing.T) {
	t.Parallel()
	m := initialModel("test", &apiClient{groups: &fakeGroupClient{}, channels: &mockChannelClient{}})
	m.chatState = chatStateOAuth
	m.creationFlowID = 7
	m.creationPendingGroupName = "g1"
	m.creationSelectedProvider = "openai"
	m.oauth = oauthState{provider: "openai", groupJID: "tui:g1", pendingGroupName: "g1"}

	updated, cmd := m.handleAuthEvent(authEventMsg{event: &kraclawv1.DeviceAuthEvent{
		Event: &kraclawv1.DeviceAuthEvent_Success_{Success: &kraclawv1.DeviceAuthEvent_Success{}},
	}})
	got := updated.(model)
	if got.chatState != chatStateSelectModel {
		t.Fatalf("chatState = %v, want %v", got.chatState, chatStateSelectModel)
	}
	if got.creationSelectedProvider != "openai" {
		t.Fatalf("creationSelectedProvider = %q, want openai", got.creationSelectedProvider)
	}
	if got.creationProvidersLoaded {
		t.Fatal("creationProvidersLoaded = true, want false while dynamic models load")
	}
	if cmd == nil {
		t.Fatal("expected command to fetch dynamic models")
	}
}

func TestComposerCommand_Auth(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		input        string
		wantState    chatState
		wantProvider string
		wantPending  string
		wantErr      bool
	}{
		":auth openai branches to OAuth re-auth": {
			input:        ":auth openai",
			wantState:    chatStateOAuth,
			wantProvider: "openai",
			wantPending:  "",
		},
		":auth without provider sets chatErr": {
			input:     ":auth",
			wantState: chatStateChatting,
			wantErr:   true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := initialModel("test", &apiClient{channels: &mockChannelClient{}})
			m.chatState = chatStateChatting
			m.chatGroup = &GroupInfo{JID: "tui:g1"}
			m.chatInput.SetValue(tt.input)

			updated, _ := m.updateChat(keyPress("enter"))
			got := updated.(model)

			if got.chatState != tt.wantState {
				t.Errorf("input=%q chatState = %v, want %v", tt.input, got.chatState, tt.wantState)
			}
			if tt.wantProvider != "" && got.oauth.provider != tt.wantProvider {
				t.Errorf("input=%q oauth.provider = %q, want %q", tt.input, got.oauth.provider, tt.wantProvider)
			}
			if got.oauth.pendingGroupName != tt.wantPending {
				t.Errorf("input=%q oauth.pendingGroupName = %q, want %q", tt.input, got.oauth.pendingGroupName, tt.wantPending)
			}
			if tt.wantErr && got.chatErr == nil {
				t.Errorf("input=%q expected chatErr, got nil", tt.input)
			}
		})
	}
}

func TestAuthCommand_PrefixCollision(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		input string
		want  bool
	}{
		"exact :auth no provider":     {input: ":auth", want: true},
		":auth openai triggers OAuth": {input: ":auth openai", want: true},
		":authority does NOT trigger": {input: ":authority foo", want: false},
		":authentic does NOT trigger": {input: ":authentic", want: false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := isAuthCommand(tt.input); got != tt.want {
				t.Errorf("isAuthCommand(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
