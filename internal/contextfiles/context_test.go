package contextfiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_NoFiles(t *testing.T) {
	dir := t.TempDir()
	result, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if result != "" {
		t.Errorf("Load() = %q, want empty string", result)
	}
}

func TestLoad_SingleFile(t *testing.T) {
	dir := t.TempDir()
	content := "This is a CLAUDE.md file.\n"
	err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(content), 0o644)
	if err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	result, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !strings.Contains(result, "## CLAUDE.md") {
		t.Errorf("Load() missing header; got %q", result)
	}
	if !strings.Contains(result, "This is a CLAUDE.md file.") {
		t.Errorf("Load() missing content; got %q", result)
	}
}

func TestLoad_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"SOUL.md":   "I am the soul.",
		"CLAUDE.md": "Claude instructions.",
		"README.md": "Project readme.",
	}
	for name, content := range files {
		err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
		if err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}

	result, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// SOUL.md should appear before CLAUDE.md (priority order).
	soulIdx := strings.Index(result, "## SOUL.md")
	claudeIdx := strings.Index(result, "## CLAUDE.md")
	if soulIdx < 0 || claudeIdx < 0 || soulIdx > claudeIdx {
		t.Errorf("Load() priority order wrong; got %q", result)
	}
}

func TestLoad_EmptyFileSkipped(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("   \n  \n"), 0o644)
	if err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	result, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if result != "" {
		t.Errorf("Load() = %q, want empty (whitespace-only file should be skipped)", result)
	}
}

func TestLoadCustom(t *testing.T) {
	dir := t.TempDir()
	content := "Custom content."
	err := os.WriteFile(filepath.Join(dir, "custom.md"), []byte(content), 0o644)
	if err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	result, err := LoadCustom(dir, "custom.md")
	if err != nil {
		t.Fatalf("LoadCustom() error = %v", err)
	}
	if result != content {
		t.Errorf("LoadCustom() = %q, want %q", result, content)
	}
}

func TestLoadCustom_MissingFile(t *testing.T) {
	dir := t.TempDir()
	result, err := LoadCustom(dir, "missing.md")
	if err != nil {
		t.Fatalf("LoadCustom() error = %v", err)
	}
	if result != "" {
		t.Errorf("LoadCustom() = %q, want empty string for missing file", result)
	}
}

func TestExists(t *testing.T) {
	dir := t.TempDir()
	if Exists(dir) {
		t.Error("Exists() = true, want false (empty dir)")
	}

	err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("test"), 0o644)
	if err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	if !Exists(dir) {
		t.Error("Exists() = false, want true (CLAUDE.md present)")
	}
}

func TestLoad_WhitespaceTrimmed(t *testing.T) {
	dir := t.TempDir()
	content := "  \n  actual content  \n  "
	err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(content), 0o644)
	if err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	result, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if strings.Contains(result, "  actual content  ") {
		t.Errorf("Load() content not trimmed; got %q", result)
	}
	if !strings.Contains(result, "actual content") {
		t.Errorf("Load() missing content; got %q", result)
	}
}
