package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestPersistAndLoadTheme(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if err := persistTheme("light"); err != nil {
		t.Fatalf("persistTheme: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "kraclaw", "tui.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg tuiConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg.Theme != "light" {
		t.Fatalf("theme = %q, want %q", cfg.Theme, "light")
	}

	if got := loadSavedThemeName(); got != "light" {
		t.Fatalf("loadSavedThemeName = %q, want %q", got, "light")
	}
}

func TestSetThemeRebuildsStyles(t *testing.T) {
	restore := activePalette
	t.Cleanup(func() {
		activePalette = restore
		rebuildStyles()
	})

	setTheme("light")
	if activePalette.Name != "light" {
		t.Fatalf("active palette = %q, want light", activePalette.Name)
	}
	setTheme("dark")
	if activePalette.Name != "dark" {
		t.Fatalf("active palette = %q, want dark", activePalette.Name)
	}
}

func TestHandleLocalCommandTheme(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	restore := activePalette
	t.Cleanup(func() {
		activePalette = restore
		rebuildStyles()
	})

	setTheme("dark")
	m := initialModel("test", &apiClient{channels: &mockChannelClient{}})
	if _, handled := m.handleLocalCommand(":theme light"); !handled {
		t.Fatal(":theme light should be handled locally")
	}
	if activePalette.Name != "light" {
		t.Fatalf("after :theme light, active = %q", activePalette.Name)
	}

	if _, handled := m.handleLocalCommand(":theme"); !handled {
		t.Fatal(":theme toggle should be handled locally")
	}
	if activePalette.Name != "dark" {
		t.Fatalf("after :theme toggle, active = %q", activePalette.Name)
	}

	if _, handled := m.handleLocalCommand("hello world"); handled {
		t.Fatal("plain text should not be handled as command")
	}

	for _, input := range []string{":", ":   "} {
		if _, handled := m.handleLocalCommand(input); handled {
			t.Fatalf("bare colon input %q should not be handled as command", input)
		}
	}
}

func TestCtrlTRefreshesChatViewportContent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	restore := activePalette
	t.Cleanup(func() {
		activePalette = restore
		rebuildStyles()
	})

	setTheme("dark")
	m := initialModel("test", &apiClient{channels: &mockChannelClient{}})
	m.activeTab = tabMessages
	m.chatState = chatStateChatting
	m.chatMessages = []chatMessage{{sender: "agent", content: "existing transcript"}}
	m = m.refreshChatViewportContent()

	updated, _ := m.Update(keyPress("ctrl+t"))
	m1 := updated.(model)
	if activePalette.Name != "light" {
		t.Fatalf("active palette = %q, want light", activePalette.Name)
	}
	view := m1.chatViewport.View()
	if !strings.Contains(view, "existing") || !strings.Contains(view, "transcript") {
		t.Fatalf("chat viewport lost existing content: %q", m1.chatViewport.View())
	}
}

func TestThemeCommandRefreshesChatViewportContent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	restore := activePalette
	t.Cleanup(func() {
		activePalette = restore
		rebuildStyles()
	})

	setTheme("dark")
	m := initialModel("test", &apiClient{channels: &mockChannelClient{}})
	m.activeTab = tabMessages
	m.chatState = chatStateChatting
	m.chatMessages = []chatMessage{{sender: "agent", content: "saved response"}}
	m = m.refreshChatViewportContent()
	m.chatInput.SetValue(":theme light")

	updated, _ := m.updateChat(keyPress("enter"))
	m1 := updated.(model)
	if m1.chatInput.Value() != "" {
		t.Fatalf("chat input = %q, want empty", m1.chatInput.Value())
	}
	if activePalette.Name != "light" {
		t.Fatalf("active palette = %q, want light", activePalette.Name)
	}
	view := m1.chatViewport.View()
	if !strings.Contains(view, "saved") || !strings.Contains(view, "response") {
		t.Fatalf("chat viewport lost existing content: %q", m1.chatViewport.View())
	}
}

func TestSectionRuleContainsLabel(t *testing.T) {
	out := sectionRule(80, "resources")
	if !strings.Contains(out, "resources") {
		t.Fatalf("sectionRule missing label: %q", out)
	}
}

func TestKeyBarRendersHints(t *testing.T) {
	out := keyBar([][2]string{{"[⏎]", "send"}, {"[esc]", "back"}}, 40)
	if !strings.Contains(out, "send") || !strings.Contains(out, "back") {
		t.Fatalf("keyBar missing labels: %q", out)
	}
}

func TestStatusLineFillsWidth(t *testing.T) {
	out := statusLine([]string{"a", "b"}, 40)
	if got := lipgloss.Width(out); got != 40 {
		t.Fatalf("statusLine width = %d, want 40", got)
	}
}

func TestStatusLineTruncatesToWidth(t *testing.T) {
	out := statusLine([]string{
		coralBold.Render("kraclaw") + " ctl",
		"grpc://localhost:50051",
		okStyle.Render("● TLS"),
		"very-long-user@very-long-host",
		"UTC 12:34:56",
		"theme:dark",
	}, 32)
	if got := lipgloss.Width(out); got > 32 {
		t.Fatalf("statusLine width = %d, want <= 32", got)
	}
}
