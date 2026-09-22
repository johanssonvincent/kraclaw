package main

import (
	"testing"
)

func TestParseToolCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		wantName string
		wantArgs map[string]any
		wantErr  bool
	}{
		{
			name:     "valid simple",
			input:    "TOOL_CALL:get_weather:{\"city\": \"London\"}",
			wantName: "get_weather",
			wantArgs: map[string]any{"city": "London"},
		},
		{
			name:     "valid nested",
			input:    "TOOL_CALL:query:{\"filter\": {\"status\": \"active\"}}",
			wantName: "query",
			wantArgs: map[string]any{"filter": map[string]any{"status": "active"}},
		},
		{
			name:     "valid empty args",
			input:    "TOOL_CALL:ping:{}",
			wantName: "ping",
			wantArgs: map[string]any{},
		},
		{
			name:    "missing prefix",
			input:   "CALL:get_weather:{\"city\": \"London\"}",
			wantErr: true,
		},
		{
			name:    "missing colon after name",
			input:   "TOOL_CALL:get_weather",
			wantErr: true,
		},
		{
			name:    "empty name",
			input:   "TOOL_CALL:{\"city\": \"London\"}",
			wantErr: true,
		},
		{
			name:    "invalid json",
			input:   "TOOL_CALL:get_weather:{bad json}",
			wantErr: true,
		},
		{
			name:     "whitespace trimmed",
			input:    "  TOOL_CALL:get_weather:{\"city\": \"London\"}  ",
			wantName: "get_weather",
			wantArgs: map[string]any{"city": "London"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseToolCall(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.Name != tt.wantName {
				t.Errorf("name = %q, want %q", got.Name, tt.wantName)
			}

			if len(got.Args) != len(tt.wantArgs) {
				t.Errorf("args length = %d, want %d", len(got.Args), len(tt.wantArgs))
			}

			for k, v := range tt.wantArgs {
				gotVal, ok := got.Args[k]
				if !ok {
					t.Errorf("missing args key %q", k)

					continue
				}

				if nv, ok := v.(map[string]any); ok {
					gnv, gok := gotVal.(map[string]any)
					if !gok {
						t.Errorf("args[%q] is not a map", k)

						continue
					}

					for nk, nvv := range nv {
						if gnv[nk] != nvv {
							t.Errorf("args[%q][%q] = %v, want %v", k, nk, gnv[nk], nvv)
						}
					}
				} else if gotVal != v {
					t.Errorf("args[%q] = %v, want %v", k, gotVal, v)
				}
			}
		})
	}
}

func TestExtractToolCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		wantName string
		wantOK   bool
	}{
		{
			name:     "simple tool call",
			input:    "TOOL_CALL:get_weather:{\"city\": \"London\"}",
			wantName: "get_weather",
			wantOK:   true,
		},
		{
			name:     "tool call with surrounding text",
			input:    "Let me check the weather.\nTOOL_CALL:get_weather:{\"city\": \"London\"}\nI'll get that for you.",
			wantName: "get_weather",
			wantOK:   true,
		},
		{
			name:   "no tool call",
			input:  "The weather in London is rainy.",
			wantOK: false,
		},
		{
			name:   "malformed tool call",
			input:  "TOOL_CALL:incomplete",
			wantOK: false,
		},
		{
			name:     "tool call on second line",
			input:    "Thinking...\nTOOL_CALL:search:{\"query\": \"test\"}",
			wantName: "search",
			wantOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := extractToolCall(tt.input)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}

			if ok && got.Name != tt.wantName {
				t.Errorf("name = %q, want %q", got.Name, tt.wantName)
			}
		})
	}
}
