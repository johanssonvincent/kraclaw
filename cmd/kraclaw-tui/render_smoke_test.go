package main

import (
	"strings"
	"testing"
)

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
