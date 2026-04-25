// Package main provides kraclaw TUI client ChatGPT OAuth device-flow handling.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

// oauthState tracks an in-flight ChatGPT OAuth device flow. active is true
// from when startOAuthCmd dispatches until the terminal event is handled
// (success or error) or the user cancels with Esc.
type oauthState struct {
	active           bool
	provider         string
	groupJID         string
	pendingGroupName string // empty for re-auth flow (group already exists)
	userCode         string
	verificationURL  string
	elapsed          int
	err              error
	stream           kraclawv1.AuthService_StartChatGPTDeviceAuthClient
	cancel           context.CancelFunc
}

// authStartedMsg is dispatched once the AuthService stream is opened (or fails
// to open). On success it carries the stream and its cancel func so the
// model can drive the event loop and tear the stream down on Esc.
type authStartedMsg struct {
	stream kraclawv1.AuthService_StartChatGPTDeviceAuthClient
	cancel context.CancelFunc
	err    error
}

// authEventMsg carries one DeviceAuthEvent off the stream, or a stream error.
// A nil event with nil err signals server-closed-without-terminal-event,
// which the handler treats as an error.
type authEventMsg struct {
	event *kraclawv1.DeviceAuthEvent
	err   error
}

// startOAuthCmd opens the AuthService stream. It does NOT consume events —
// authEventLoopCmd does that one event at a time so each event becomes a
// distinct tea.Msg and re-renders the UI.
func (m model) startOAuthCmd(provider, groupJID, _ string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := m.api.auth.StartChatGPTDeviceAuth(ctx, &kraclawv1.StartChatGPTDeviceAuthRequest{
			GroupJid: groupJID,
			Provider: provider,
		})
		if err != nil {
			cancel()
			return authStartedMsg{err: fmt.Errorf("start chatgpt device auth: %w", err)}
		}
		return authStartedMsg{stream: stream, cancel: cancel}
	}
}

// authEventLoopCmd reads exactly one event off the stream and emits it as a
// tea.Msg. The Update loop re-issues this command after every non-terminal
// event so each event re-renders the UI.
func authEventLoopCmd(stream kraclawv1.AuthService_StartChatGPTDeviceAuthClient) tea.Cmd {
	return func() tea.Msg {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return authEventMsg{}
		}
		if err != nil {
			return authEventMsg{err: fmt.Errorf("auth stream recv: %w", err)}
		}
		return authEventMsg{event: ev}
	}
}

// handleAuthStarted records the open stream on the model and kicks off the
// event loop. On open-error it surfaces the error in oauthState.err so the
// user can press Esc to return to the picker.
func (m model) handleAuthStarted(msg authStartedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.oauth.err = msg.err
		return m, nil
	}
	m.oauth.stream = msg.stream
	m.oauth.cancel = msg.cancel
	return m, authEventLoopCmd(msg.stream)
}

// handleAuthEvent processes one DeviceAuthEvent off the stream. DeviceCode
// and Tick are non-terminal and re-arm the event loop. Success transitions
// to connecting (or back to chatting on the re-auth path). Error and stream
// errors stay on the OAuth screen with oauth.err populated.
func (m model) handleAuthEvent(msg authEventMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if m.oauth.cancel != nil {
			m.oauth.cancel()
		}
		m.oauth.err = msg.err
		return m, nil
	}
	if msg.event == nil {
		// Server closed stream without a terminal event.
		if m.oauth.cancel != nil {
			m.oauth.cancel()
		}
		m.oauth.err = fmt.Errorf("auth stream closed unexpectedly")
		return m, nil
	}

	switch e := msg.event.GetEvent().(type) {
	case *kraclawv1.DeviceAuthEvent_DeviceCode_:
		dc := e.DeviceCode
		m.oauth.userCode = dc.GetUserCode()
		m.oauth.verificationURL = dc.GetVerificationUrl()
		// Best-effort browser open — failure is non-fatal because the user_code
		// is always shown alongside.
		if err := OpenURL(dc.GetVerificationUrl()); err != nil {
			slog.Warn("OpenURL failed", "err", err)
		}
		return m, authEventLoopCmd(m.oauth.stream)

	case *kraclawv1.DeviceAuthEvent_Tick_:
		m.oauth.elapsed = int(e.Tick.GetElapsedSeconds())
		return m, authEventLoopCmd(m.oauth.stream)

	case *kraclawv1.DeviceAuthEvent_Success_:
		if m.oauth.cancel != nil {
			m.oauth.cancel()
		}
		if m.oauth.pendingGroupName != "" {
			// New-group path: group was deferred until OAuth completed; now
			// register it. Task 6 wires the trigger into the chat flow.
			name := m.oauth.pendingGroupName
			provider := m.oauth.provider
			modelID := m.creationSelectedModelID
			m.oauth = oauthState{}
			m.chatState = chatStateConnecting
			m.chatMessages = nil
			return m, m.registerGroupCmd(name, provider, modelID)
		}
		// Re-auth path (Task 7): group already exists, just go back to chat.
		m.oauth = oauthState{}
		m.chatState = chatStateChatting
		return m, nil

	case *kraclawv1.DeviceAuthEvent_Error_:
		if m.oauth.cancel != nil {
			m.oauth.cancel()
		}
		m.oauth.err = fmt.Errorf("oauth %s: %s", e.Error.GetCode(), e.Error.GetMessage())
		return m, nil
	}

	// Unknown event type — keep listening rather than crash the flow.
	return m, authEventLoopCmd(m.oauth.stream)
}

// handleEscOAuth tears down any in-flight OAuth stream and returns the model
// to the right place: re-auth (group already exists) → chatting; new-group
// (group not yet registered) → group picker.
func (m model) handleEscOAuth() (tea.Model, tea.Cmd) {
	if m.oauth.cancel != nil {
		m.oauth.cancel()
	}
	wasReauth := m.oauth.pendingGroupName == ""
	m.oauth = oauthState{}
	// Clear creation state unconditionally so stale context from a cancelled
	// new-group OAuth flow does not survive into a subsequent attempt.
	// In the re-auth path these fields are already empty, so this is a no-op.
	m.creationPendingGroupName = ""
	m.creationSelectedProvider = ""
	m.creationSelectedModelID = ""
	m.creationPicker = creationPickerState{}
	m.creationProviders = nil
	m.creationProvidersLoaded = false
	if wasReauth && m.chatGroup != nil && m.chatGroup.JID != "" {
		m.chatState = chatStateChatting
		return m, nil
	}
	m.chatState = chatStateSelectGroup
	return m, nil
}

// renderOAuth renders the device-code screen. It always shows the user_code
// and verification URL because the OpenURL helper is best-effort.
func renderOAuth(s oauthState) string {
	if s.err != nil {
		return errStyle.Render(
			fmt.Sprintf("ChatGPT auth failed: %v\n\nPress Esc to return.", s.err))
	}
	if s.userCode == "" {
		return lipgloss.NewStyle().Foreground(defaultChatTheme.Muted).Render("starting OAuth…")
	}
	body := fmt.Sprintf(
		"Open this URL in a browser and enter the code:\n\n  %s\n\n  code: %s\n\n  elapsed: %ds — waiting for approval…",
		s.verificationURL, s.userCode, s.elapsed,
	)
	return lipgloss.NewStyle().Foreground(defaultChatTheme.Text).Render(body)
}
