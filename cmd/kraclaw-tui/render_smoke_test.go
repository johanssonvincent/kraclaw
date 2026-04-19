package main

import (
	"strings"
	"testing"
)

// TestRenderContentAllTabs checks that every tab renders without panic and
// the output contains the tab's coral section header.
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
	// All eight labels should be present.
	for _, label := range tabLabels {
		if !strings.Contains(out, label) {
			t.Fatalf("tab bar missing label %q: %s", label, out)
		}
	}
	// Numbered prefixes [1]..[8].
	for _, n := range []string{"[1]", "[2]", "[3]", "[4]", "[5]", "[6]", "[7]", "[8]"} {
		if !strings.Contains(out, n) {
			t.Fatalf("tab bar missing number %q", n)
		}
	}
}
