package main

import (
	"errors"
	"strings"
	"testing"
)

func TestRenderOAuth_ErrorScreenDoesNotPromiseEnterRetry(t *testing.T) {
	t.Parallel()
	out := renderOAuth(oauthState{err: errors.New("denied")})
	if strings.Contains(out, "Enter to retry") {
		t.Errorf("renderOAuth still promises Enter to retry without a handler: %q", out)
	}
	if !strings.Contains(out, "Esc") {
		t.Errorf("renderOAuth error view should mention Esc; got: %q", out)
	}
}

func TestRenderOAuth_ShowsBrowserOpenFailureHint(t *testing.T) {
	t.Parallel()
	out := renderOAuth(oauthState{
		userCode:        "ABCD-1234",
		verificationURL: "https://example.com/verify",
		openURLErr:      errors.New("xdg-open: not found"),
	})
	if !strings.Contains(out, "couldn't open browser") {
		t.Errorf("renderOAuth should hint that browser launch failed; got: %q", out)
	}
}

func TestRenderSmoke_OAuthScreen(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		oauth oauthState
		want  []string
	}{
		"device-code body shows user_code, URL, and elapsed": {
			oauth: oauthState{
				active:          true,
				userCode:        "ABCD-1234",
				verificationURL: "https://auth.openai.com/codex/device",
				elapsed:         7,
			},
			want: []string{
				"ABCD-1234",
				"auth.openai.com/codex/device",
				"elapsed: 7s",
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := initialModel("test", &apiClient{channels: &mockChannelClient{}})
			m.chatState = chatStateOAuth
			m.oauth = tt.oauth
			got := m.renderChat()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("renderChat() missing %q\n--- output ---\n%s", want, got)
				}
			}
		})
	}
}
