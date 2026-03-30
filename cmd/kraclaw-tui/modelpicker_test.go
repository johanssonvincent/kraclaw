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
				"- claude-3-5-sonnet-20241022 (Sonnet) (current)\n" +
				"- claude-3-5-haiku-20241022 (Haiku)",
			want: []modelOption{
				{ID: "claude-3-5-sonnet-20241022", Label: "claude-3-5-sonnet-20241022 (Sonnet)", Current: true},
				{ID: "claude-3-5-haiku-20241022", Label: "claude-3-5-haiku-20241022 (Haiku)", Current: false},
			},
		},
		{
			name: "cached header ignores blanks and keeps order",
			content: "Models (cached):\n\n" +
				"- claude-3-7-sonnet-20250219\n" +
				"- claude-opus-4-1-20250805 (Opus 4.1)",
			want: []modelOption{
				{ID: "claude-3-7-sonnet-20250219", Label: "claude-3-7-sonnet-20250219", Current: false},
				{ID: "claude-opus-4-1-20250805", Label: "claude-opus-4-1-20250805 (Opus 4.1)", Current: false},
			},
		},
		{
			name: "cached upstream error header is ignored",
			content: "Models (cached; upstream error):\n" +
				"- claude-3-5-sonnet-20241022\n" +
				"Current: default",
			want: []modelOption{
				{ID: "claude-3-5-sonnet-20241022", Label: "claude-3-5-sonnet-20241022", Current: false},
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
