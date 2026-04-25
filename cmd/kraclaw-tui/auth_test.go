package main

import (
	"errors"
	"testing"

	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

func TestOAuthFlow_HandlesEvents(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		event        *kraclawv1.DeviceAuthEvent
		startState   oauthState
		wantState    chatState
		wantErr      bool
		wantUserCode string
	}{
		"device code populates fields": {
			event: &kraclawv1.DeviceAuthEvent{Event: &kraclawv1.DeviceAuthEvent_DeviceCode_{
				DeviceCode: &kraclawv1.DeviceAuthEvent_DeviceCode{
					UserCode:        "ABCD-1234",
					VerificationUrl: "https://auth.openai.com/codex/device",
				},
			}},
			startState:   oauthState{active: true},
			wantState:    chatStateOAuth,
			wantUserCode: "ABCD-1234",
		},
		"tick advances elapsed": {
			event: &kraclawv1.DeviceAuthEvent{Event: &kraclawv1.DeviceAuthEvent_Tick_{
				Tick: &kraclawv1.DeviceAuthEvent_Tick{ElapsedSeconds: 12},
			}},
			startState: oauthState{active: true, userCode: "ABCD-1234"},
			wantState:  chatStateOAuth,
		},
		"success transitions to connecting": {
			event: &kraclawv1.DeviceAuthEvent{Event: &kraclawv1.DeviceAuthEvent_Success_{
				Success: &kraclawv1.DeviceAuthEvent_Success{AccountId: "acct_123"},
			}},
			startState: oauthState{active: true, pendingGroupName: "g1", provider: "openai"},
			wantState:  chatStateConnecting,
		},
		"error stays on screen with err": {
			event: &kraclawv1.DeviceAuthEvent{Event: &kraclawv1.DeviceAuthEvent_Error_{
				Error: &kraclawv1.DeviceAuthEvent_Error{Code: "ACCESS_DENIED", Message: "user denied"},
			}},
			startState: oauthState{active: true},
			wantState:  chatStateOAuth,
			wantErr:    true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := model{chatState: chatStateOAuth, oauth: tt.startState}
			next, _ := m.handleAuthEvent(authEventMsg{event: tt.event})
			gotModel := next.(model)
			if gotModel.chatState != tt.wantState {
				t.Errorf("handleAuthEvent(%#v) state = %v, want %v", tt.event, gotModel.chatState, tt.wantState)
			}
			if tt.wantUserCode != "" && gotModel.oauth.userCode != tt.wantUserCode {
				t.Errorf("handleAuthEvent(%#v) user_code = %q, want %q", tt.event, gotModel.oauth.userCode, tt.wantUserCode)
			}
			if tt.wantErr && gotModel.oauth.err == nil {
				t.Errorf("handleAuthEvent(%#v) expected oauth.err, got nil", tt.event)
			}
		})
	}
}

func TestOAuthFlow_TerminalSignalsSetErr(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		msg authEventMsg
	}{
		"stream error":          {msg: authEventMsg{err: errors.New("network")}},
		"nil event from server": {msg: authEventMsg{}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := model{chatState: chatStateOAuth, oauth: oauthState{active: true}}
			next, _ := m.handleAuthEvent(tt.msg)
			got := next.(model)
			if got.oauth.err == nil {
				t.Errorf("msg=%+v: expected oauth.err, got nil", tt.msg)
			}
		})
	}
}

func TestHandleAuthEvent_UnknownVariantSetsErrAndCancels(t *testing.T) {
	t.Parallel()
	var canceled bool
	cancel := func() { canceled = true }
	m := model{
		chatState: chatStateOAuth,
		oauth:     oauthState{cancel: cancel},
	}
	// DeviceAuthEvent with no oneof variant set is the "unknown" case.
	got, cmd := m.handleAuthEvent(authEventMsg{event: &kraclawv1.DeviceAuthEvent{}})
	gm := got.(model)
	if gm.oauth.err == nil {
		t.Errorf("expected oauth.err to be set; got nil")
	}
	if cmd != nil {
		t.Errorf("expected nil cmd to halt the loop; got non-nil")
	}
	if !canceled {
		t.Errorf("expected cancel() to be invoked")
	}
}
