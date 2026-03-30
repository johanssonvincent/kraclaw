package main

import (
	"reflect"
	"testing"
)

func TestParseModelList(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []modelOption
	}{
		{
			name: "models header with current marker",
			content: "Models:\n" +
				"- claude-sonnet-4-6 (Claude Sonnet 4.6) (current)\n" +
				"- claude-haiku-4-5 (Claude Haiku 4.5)",
			want: []modelOption{
				{ID: "claude-sonnet-4-6", Label: "claude-sonnet-4-6 (Claude Sonnet 4.6)", Current: true},
				{ID: "claude-haiku-4-5", Label: "claude-haiku-4-5 (Claude Haiku 4.5)", Current: false},
			},
		},
		{
			name: "cached header ignores blanks and keeps order",
			content: "Models (cached):\n\n" +
				"- claude-sonnet-4-5 (Claude Sonnet 4.5)\n" +
				"- claude-opus-4-1 (Claude Opus 4.1)",
			want: []modelOption{
				{ID: "claude-sonnet-4-5", Label: "claude-sonnet-4-5 (Claude Sonnet 4.5)", Current: false},
				{ID: "claude-opus-4-1", Label: "claude-opus-4-1 (Claude Opus 4.1)", Current: false},
			},
		},
		{
			name: "cached upstream error header is ignored",
			content: "Models (cached; upstream error):\n" +
				"- claude-opus-4-6 (Claude Opus 4.6)\n" +
				"Current: default",
			want: []modelOption{
				{ID: "claude-opus-4-6", Label: "claude-opus-4-6 (Claude Opus 4.6)", Current: false},
			},
		},
		{
			name: "non model content returns empty",
			content: "Failed to fetch models: upstream unavailable\n" +
				"Try again later",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseModelList(tt.content)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseModelList() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
