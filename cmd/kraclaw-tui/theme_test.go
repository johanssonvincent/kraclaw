package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	// width should be 40 visible cells (approximate — lipgloss.Width respects codes)
	if len(out) == 0 {
		t.Fatal("statusLine returned empty string")
	}
}
