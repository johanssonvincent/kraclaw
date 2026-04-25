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

// fakeAuthStream lets tests drive Recv error scenarios without a real gRPC channel.
type fakeAuthStream struct {
	kraclawv1.AuthService_StartChatGPTDeviceAuthClient
	recvErr error
}

func (f *fakeAuthStream) Recv() (*kraclawv1.DeviceAuthEvent, error) {
	return nil, f.recvErr
}
