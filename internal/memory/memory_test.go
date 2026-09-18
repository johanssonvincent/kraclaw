package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNew(t *testing.T) {
	cfg := Config{Enabled: true}
	s := New(cfg)
	if s == nil {
		t.Fatal("New() returned nil")
	}
}

func TestConfig_Disabled(t *testing.T) {
	cfg := Config{Enabled: false}
	s := New(cfg)
	// Check via Add (should be no-op when disabled).
	mem := &Memory{GroupJID: "test", Content: "test"}
	err := s.Add(context.Background(), mem)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if s.Count("test") != 0 {
		t.Error("Add() should be no-op when disabled")
	}
}

func TestAddAndGet(t *testing.T) {
	cfg := Config{Enabled: true}
	s := New(cfg)
	ctx := context.Background()

	mem := &Memory{
		GroupJID: "test-group",
		Content:  "Test memory content",
		Category: "fact",
	}

	err := s.Add(ctx, mem)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if mem.ID == "" {
		t.Error("Add() should generate ID")
	}

	result, err := s.Get(ctx, mem.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if result.Content != mem.Content {
		t.Errorf("Get() Content = %q, want %q", result.Content, mem.Content)
	}
}

func TestGet_NotFound(t *testing.T) {
	s := New(Config{})
	_, err := s.Get(context.Background(), "nonexistent")
	if err == nil {
		t.Error("Get() should return error for nonexistent memory")
	}
}

func TestList(t *testing.T) {
	s := New(Config{Enabled: true})
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		err := s.Add(ctx, &Memory{
			GroupJID: "test-group",
			Content:  "Memory " + string(rune('a'+i)),
		})
		if err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}

	mems := s.List(ctx, "test-group")
	if len(mems) != 3 {
		t.Errorf("List() = %d, want 3", len(mems))
	}
}

func TestSearch(t *testing.T) {
	s := New(Config{Enabled: true})
	ctx := context.Background()

	s.Add(ctx, &Memory{GroupJID: "test", Content: "Hello world"})
	s.Add(ctx, &Memory{GroupJID: "test", Content: "Goodbye world"})
	s.Add(ctx, &Memory{GroupJID: "test", Content: "Different content"})

	results := s.Search(ctx, "test", "hello")
	if len(results) == 0 {
		t.Error("Search() should find 'hello'")
	}

	results = s.Search(ctx, "test", "world")
	if len(results) < 2 {
		t.Errorf("Search() should find 2 results for 'world', got %d", len(results))
	}
}

func TestDelete(t *testing.T) {
	s := New(Config{Enabled: true})
	ctx := context.Background()

	mem := &Memory{GroupJID: "test", Content: "To be deleted"}
	s.Add(ctx, mem)

	err := s.Delete(ctx, mem.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	_, err = s.Get(ctx, mem.ID)
	if err == nil {
		t.Error("Get() should return error after delete")
	}
}

func TestDelete_NotFound(t *testing.T) {
	s := New(Config{})
	err := s.Delete(context.Background(), "nonexistent")
	if err == nil {
		t.Error("Delete() should return error for nonexistent memory")
	}
}

func TestCount(t *testing.T) {
	s := New(Config{Enabled: true})
	ctx := context.Background()

	s.Add(ctx, &Memory{GroupJID: "group-a", Content: "A"})
	s.Add(ctx, &Memory{GroupJID: "group-a", Content: "B"})
	s.Add(ctx, &Memory{GroupJID: "group-b", Content: "C"})

	if s.Count("group-a") != 2 {
		t.Errorf("Count(group-a) = %d, want 2", s.Count("group-a"))
	}
	if s.Count("group-b") != 1 {
		t.Errorf("Count(group-b) = %d, want 1", s.Count("group-b"))
	}
	if s.TotalCount() != 3 {
		t.Errorf("TotalCount() = %d, want 3", s.TotalCount())
	}
}

func TestClear(t *testing.T) {
	s := New(Config{Enabled: true})
	ctx := context.Background()

	s.Add(ctx, &Memory{GroupJID: "test", Content: "A"})
	s.Add(ctx, &Memory{GroupJID: "test", Content: "B"})

	err := s.Clear(ctx, "test")
	if err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	if s.Count("test") != 0 {
		t.Errorf("Count() after Clear() = %d, want 0", s.Count("test"))
	}
}

func TestFormatPrompt(t *testing.T) {
	s := New(Config{})

	// Empty memories.
	result := s.FormatPrompt(nil)
	if result != "" {
		t.Errorf("FormatPrompt(nil) = %q, want empty", result)
	}

	// With memories.
	mems := []*Memory{
		{Category: "fact", Content: "Test fact"},
		{Category: "preference", Content: "Test preference"},
	}

	result = s.FormatPrompt(mems)
	if result == "" {
		t.Error("FormatPrompt() should not be empty with memories")
	}
}

func TestRecall(t *testing.T) {
	s := New(Config{Enabled: true})
	ctx := context.Background()

	s.Add(ctx, &Memory{GroupJID: "test", Content: "Important fact about testing"})
	s.Add(ctx, &Memory{GroupJID: "test", Content: "Unrelated memory"})

	results := s.Recall(ctx, "test", "testing")
	if len(results) == 0 {
		t.Error("Recall() should find relevant memories")
	}
}

func TestRecall_Disabled(t *testing.T) {
	s := New(Config{Enabled: false})
	results := s.Recall(context.Background(), "test", "query")
	if len(results) != 0 {
		t.Error("Recall() should return empty when disabled")
	}
}

func TestSearchIndex(t *testing.T) {
	idx := newSearchIndex()
	mem := &Memory{ID: "test", Content: "Hello world test", Importance: 1.0}

	idx.add(mem)

	// Search for a term.
	results := idx.search("hello", []*Memory{mem})
	if len(results) == 0 {
		t.Error("search() should find 'hello'")
	}

	// Remove.
	idx.remove(mem.ID)
	results = idx.search("hello", []*Memory{mem})
	if len(results) != 0 {
		t.Error("search() should not find after remove")
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"path/to/file", "path_to_file"},
		{"name:with:colons", "name_with_colons"},
	}

	for _, tt := range tests {
		result := sanitizeFilename(tt.input)
		if result != tt.expected {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestLoad(t *testing.T) {
	// Create temp directory.
	tmpDir, err := os.MkdirTemp("", "memory-test")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := Config{
		Enabled:       true,
		StoragePath:   tmpDir,
		MaxMemoriesPerGroup: 100,
	}
	s := New(cfg)

	// Add memory.
	ctx := context.Background()
	mem := &Memory{GroupJID: "test", Content: "Test content"}
	s.Add(ctx, mem)

	// Save.
	if err := s.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file exists.
	files, err := filepath.Glob(filepath.Join(tmpDir, "*.json"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(files) == 0 {
		t.Error("Save() should create memory files")
	}
}
