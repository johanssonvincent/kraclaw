package main

import (
	"errors"
	"strings"
	"testing"

	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestOAuthFlow_HandlesEvents(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		event        *kraclawv1.DeviceAuthEvent
		startState   oauthState
		wantState    chatState
		wantErr      bool
		wantUserCode string
		wantCanceled bool
	}{
		"device code populates fields": {
			event: &kraclawv1.DeviceAuthEvent{Event: &kraclawv1.DeviceAuthEvent_DeviceCode_{
				DeviceCode: &kraclawv1.DeviceAuthEvent_DeviceCode{
					UserCode:        "ABCD-1234",
					VerificationUrl: "https://auth.openai.com/codex/device",
				},
			}},
			startState:   oauthState{},
			wantState:    chatStateOAuth,
			wantUserCode: "ABCD-1234",
		},
		"tick advances elapsed": {
			event: &kraclawv1.DeviceAuthEvent{Event: &kraclawv1.DeviceAuthEvent_Tick_{
				Tick: &kraclawv1.DeviceAuthEvent_Tick{ElapsedSeconds: 12},
			}},
			startState: oauthState{userCode: "ABCD-1234"},
			wantState:  chatStateOAuth,
		},
		"success transitions to model selection": {
			event: &kraclawv1.DeviceAuthEvent{Event: &kraclawv1.DeviceAuthEvent_Success_{
				Success: &kraclawv1.DeviceAuthEvent_Success{AccountId: "acct_123"},
			}},
			startState:   oauthState{pendingGroupName: "g1", provider: "openai", groupJID: "tui:g1"},
			wantState:    chatStateSelectModel,
			wantCanceled: true,
		},
		"error stays on screen with err": {
			event: &kraclawv1.DeviceAuthEvent{Event: &kraclawv1.DeviceAuthEvent_Error_{
				Error: &kraclawv1.DeviceAuthEvent_Error{Code: kraclawv1.DeviceAuthEvent_ACCESS_DENIED, Message: "user denied"},
			}},
			startState:   oauthState{},
			wantState:    chatStateOAuth,
			wantErr:      true,
			wantCanceled: true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var canceled bool
			cancel := func() { canceled = true }
			startState := tt.startState
			startState.cancel = cancel
			m := model{chatState: chatStateOAuth, oauth: startState}
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
			if canceled != tt.wantCanceled {
				t.Errorf("handleAuthEvent(%#v): canceled = %v, want %v", tt.event, canceled, tt.wantCanceled)
			}
		})
	}
}

func TestOAuthFlow_TerminalSignalsSetErr(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		msg authEventMsg
	}{
		"recv error sets oauth.err":       {msg: authEventMsg{err: errors.New("boom")}},
		"nil event signals stream closed": {msg: authEventMsg{}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var canceled bool
			cancel := func() { canceled = true }
			m := model{
				chatState: chatStateOAuth,
				oauth:     oauthState{cancel: cancel},
			}
			got, _ := m.handleAuthEvent(tt.msg)
			gm := got.(model)
			if gm.oauth.err == nil {
				t.Errorf("expected oauth.err to be set; got nil")
			}
			if !canceled {
				t.Errorf("expected cancel() to be invoked on terminal path")
			}
		})
	}
}

func TestHandleAuthEvent_UnknownVariantSetsErrAndCancels(t *testing.T) {
	t.Parallel()
	// Table is length-1 today; adding future unknown variants is a one-liner.
	tests := map[string]struct {
		event *kraclawv1.DeviceAuthEvent
	}{
		"empty event (no oneof set)": {event: &kraclawv1.DeviceAuthEvent{}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var canceled bool
			cancel := func() { canceled = true }
			m := model{
				chatState: chatStateOAuth,
				oauth:     oauthState{cancel: cancel},
			}
			got, cmd := m.handleAuthEvent(authEventMsg{event: tt.event})
			gm := got.(model)
			if gm.oauth.err == nil {
				t.Errorf("event=%+v: oauth.err = nil, want non-nil", tt.event)
			}
			if cmd != nil {
				t.Errorf("event=%+v: cmd = %v, want nil to halt loop", tt.event, cmd)
			}
			if !canceled {
				t.Errorf("event=%+v: cancel() not invoked", tt.event)
			}
		})
	}
}

func TestAuthEventLoop_SurfacesGRPCStatusCode(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		recvErr error
		want    string
	}{
		"unavailable":     {recvErr: status.Error(codes.Unavailable, "down"), want: "Unavailable"},
		"unauthenticated": {recvErr: status.Error(codes.Unauthenticated, "bad token"), want: "Unauthenticated"},
		"plain error":     {recvErr: errors.New("boom"), want: "boom"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stream := &fakeAuthStream{recvErr: tt.recvErr}
			msg := authEventLoopCmd(stream)()
			ev, ok := msg.(authEventMsg)
			if !ok {
				t.Fatalf("expected authEventMsg, got %T", msg)
			}
			if ev.err == nil {
				t.Fatalf("expected err, got nil")
			}
			if !strings.Contains(ev.err.Error(), tt.want) {
				t.Errorf("err = %v, want substring %q", ev.err, tt.want)
			}
		})
	}
}

func TestHandleAuthStarted(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		msg     authStartedMsg
		wantErr bool
		wantCmd bool // expect a follow-up cmd (event loop)
	}{
		"open error stays on screen, no cmd": {
			msg:     authStartedMsg{err: errors.New("dial: refused")},
			wantErr: true,
			wantCmd: false,
		},
		"happy path arms event loop": {
			msg:     authStartedMsg{stream: &fakeAuthStream{}, cancel: func() {}},
			wantErr: false,
			wantCmd: true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := model{chatState: chatStateOAuth}
			got, cmd := m.handleAuthStarted(tt.msg)
			gm := got.(model)
			if (gm.oauth.err != nil) != tt.wantErr {
				t.Errorf("oauth.err = %v, wantErr = %v", gm.oauth.err, tt.wantErr)
			}
			if (cmd != nil) != tt.wantCmd {
				t.Errorf("cmd = %v, wantCmd = %v", cmd, tt.wantCmd)
			}
		})
	}
}

// fakeAuthStream lets tests drive Recv error scenarios without a real gRPC channel.
type fakeAuthStream struct {
	kraclawv1.AuthService_StartChatGPTDeviceAuthClient
	recvErr error
}

func (f *fakeAuthStream) Recv() (*kraclawv1.DeviceAuthEvent, error) {
	return nil, f.recvErr
}
