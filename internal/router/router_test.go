package router

import (
	"testing"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/store"
)

func TestMatchesTrigger(t *testing.T) {
	r := &Router{}

	tests := []struct {
		name    string
		content string
		pattern string
		want    bool
	}{
		{"exact match", "!bot hello", "!bot", true},
		{"case insensitive", "!Bot hello", "!bot", true},
		{"no match", "hello world", "!bot", false},
		{"empty pattern matches all", "anything", "", true},
		{"content is pattern", "!bot", "!bot", true},
		{"partial match", "!bo", "!bot", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.MatchesTrigger(tt.content, tt.pattern)
			if got != tt.want {
				t.Errorf("MatchesTrigger(%q, %q) = %v, want %v", tt.content, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestFormatMessagesForAgent(t *testing.T) {
	r := &Router{}

	tests := []struct {
		name          string
		messages      []store.Message
		assistantName string
		wantContains  []string
	}{
		{
			name:          "empty messages",
			messages:      nil,
			assistantName: "Bot",
			wantContains:  []string{"<messages>", "</messages>"},
		},
		{
			name: "single user message",
			messages: []store.Message{
				{
					SenderName: "Alice",
					Content:    "hello",
					Timestamp:  time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
				},
			},
			assistantName: "Bot",
			wantContains:  []string{`sender="Alice"`, `timestamp="2024-01-01T12:00:00"`, "hello"},
		},
		{
			name: "bot message uses assistant name",
			messages: []store.Message{
				{
					SenderName:   "ignored",
					Content:      "response",
					Timestamp:    time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
					IsBotMessage: true,
				},
			},
			assistantName: "Kraclaw",
			wantContains:  []string{`sender="Kraclaw"`},
		},
		{
			name: "xml escaping",
			messages: []store.Message{
				{
					SenderName: `Al<ice & "Bob"`,
					Content:    "a > b",
					Timestamp:  time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
				},
			},
			assistantName: "Bot",
			wantContains:  []string{"&lt;", "&amp;", "&gt;", "&quot;"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.FormatMessagesForAgent(tt.messages, tt.assistantName)
			for _, want := range tt.wantContains {
				if !contains(got, want) {
					t.Errorf("FormatMessagesForAgent() missing %q in:\n%s", want, got)
				}
			}
		})
	}
}

func TestStripInternalTags(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no tags", "hello world", "hello world"},
		{"with internal tag", "hello <internal>secret</internal> world", "hello  world"},
		{"multiple tags", "<internal>a</internal>keep<internal>b</internal>", "keep"},
		{"empty internal", "before<internal></internal>after", "beforeafter"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripInternalTags(tt.input)
			if got != tt.want {
				t.Errorf("stripInternalTags(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
