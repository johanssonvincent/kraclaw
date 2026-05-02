package main

import (
	"errors"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestRenderContentAllTabs(t *testing.T) {
	tabs := []struct {
		id    int
		label string
	}{
		{tabDashboard, "overview"},
		{tabSandboxes, "sandboxes"},
		{tabGroups, "groups"},
		{tabTasks, "tasks"},
		{tabEvents, "events"},
		{tabMessages, "messages"},
		{tabChannels, "channels"},
		{tabConfig, "config"},
	}

	for _, tt := range tabs {
		t.Run(tt.label, func(t *testing.T) {
			m := initialModel("localhost:50051", &apiClient{channels: &mockChannelClient{}})
			m.activeTab = tt.id
			m.width = 120
			m.height = 40
			out := m.renderContent()
			if !strings.Contains(out, tt.label) {
				t.Fatalf("render for tab %q missing label: %s", tt.label, out)
			}
		})
	}
}

func TestRenderStubsMentionRPC(t *testing.T) {
	m := initialModel("localhost:50051", &apiClient{channels: &mockChannelClient{}})
	m.width = 120
	m.height = 40

	m.activeTab = tabChannels
	if !strings.Contains(m.renderContent(), "ListChannels RPC") {
		t.Fatalf("channels stub should name ListChannels RPC")
	}

	m.activeTab = tabConfig
	if !strings.Contains(m.renderContent(), "GetConfig RPC") {
		t.Fatalf("config stub should name GetConfig RPC")
	}

	m.activeTab = tabSandboxes
	m.sandboxDetailOpen = true
	m.sandboxes = []SandboxInfo{{Name: "sbx-x"}}
	if !strings.Contains(m.renderContent(), "GetSandboxDetail") {
		t.Fatalf("sandbox detail stub should name GetSandboxDetail RPC")
	}
}

func TestRenderTabBarHighlightsActive(t *testing.T) {
	m := initialModel("localhost:50051", &apiClient{channels: &mockChannelClient{}})
	m.activeTab = tabDashboard
	m.width = 160
	m.height = 40
	out := m.renderTabBar()
	for _, label := range tabLabels {
		if !strings.Contains(out, label) {
			t.Fatalf("tab bar missing label %q: %s", label, out)
		}
	}
	for _, n := range []string{"[1]", "[2]", "[3]", "[4]", "[5]", "[6]", "[7]", "[8]"} {
		if !strings.Contains(out, n) {
			t.Fatalf("tab bar missing number %q", n)
		}
	}
}

func TestRenderTabBarFitsNormalTerminal(t *testing.T) {
	m := initialModel("localhost:50051", &apiClient{channels: &mockChannelClient{}})
	m.activeTab = tabDashboard
	m.width = 80
	m.height = 40
	out := m.renderTabBar()
	if got := lipgloss.Width(out); got > 80 {
		t.Fatalf("tab bar width = %d, want <= 80: %q", got, out)
	}
}

func TestRenderTabBarFitsNarrowTerminal(t *testing.T) {
	m := initialModel("localhost:50051", &apiClient{channels: &mockChannelClient{}})
	m.activeTab = tabDashboard
	m.width = 40
	m.height = 40
	out := m.renderTabBar()
	if got := lipgloss.Width(out); got > 40 {
		t.Fatalf("tab bar width = %d, want <= 40: %q", got, out)
	}
}

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
	tests := map[string]struct {
		state       oauthState
		wantPresent bool
	}{
		"open url failed → hint present": {
			state: oauthState{
				userCode:        "ABCD-1234",
				verificationURL: "https://example.com/verify",
				openURLErr:      errors.New("xdg-open: not found"),
			},
			wantPresent: true,
		},
		"open url succeeded → no hint": {
			state: oauthState{
				userCode:        "ABCD-1234",
				verificationURL: "https://example.com/verify",
			},
			wantPresent: false,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := renderOAuth(tt.state)
			present := strings.Contains(got, "couldn't open browser")
			if present != tt.wantPresent {
				t.Errorf("renderOAuth(openURLErr=%v): hint present = %v, want %v\noutput: %q",
					tt.state.openURLErr, present, tt.wantPresent, got)
			}
		})
	}
}

func TestRenderOAuth_StartingStateAndErrorScreen(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		state       oauthState
		wantSubstrs []string
	}{
		"starting placeholder": {
			state:       oauthState{},
			wantSubstrs: []string{"starting OAuth"},
		},
		"error renders message and Esc hint": {
			state:       oauthState{err: errors.New("boom")},
			wantSubstrs: []string{"ChatGPT auth failed", "boom", "Esc"},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out := renderOAuth(tt.state)
			for _, sub := range tt.wantSubstrs {
				if !strings.Contains(out, sub) {
					t.Errorf("renderOAuth(%+v) = %q, want substring %q", tt.state, out, sub)
				}
			}
		})
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
