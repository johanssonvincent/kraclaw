package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildCodexResponsesRequest(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		model        string
		wantReasonOK bool
	}{
		"gpt-5-codex includes reasoning":      {model: "gpt-5-codex", wantReasonOK: true},
		"gpt-5-codex-mini includes reasoning": {model: "gpt-5-codex-mini", wantReasonOK: true},
		"o3 includes reasoning":               {model: "o3-mini", wantReasonOK: true},
		"gpt-4 omits reasoning":               {model: "gpt-4-turbo", wantReasonOK: false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := buildCodexResponsesRequest(tt.model, []conversationTurn{
				{role: "user", text: "hello"},
				{role: "assistant", text: "hi"},
			}, "next", "thread-1", "install-1")

			encoded, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("Marshal() err = %v, want nil", err)
			}
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatalf("Unmarshal() err = %v, want nil", err)
			}

			if got["model"] != tt.model {
				t.Errorf("model = %v, want %q", got["model"], tt.model)
			}
			if got["stream"] != true {
				t.Errorf("stream = %v, want true", got["stream"])
			}
			if got["tool_choice"] != "auto" {
				t.Errorf("tool_choice = %v, want auto", got["tool_choice"])
			}
			if got["parallel_tool_calls"] != false {
				t.Errorf("parallel_tool_calls = %v, want false", got["parallel_tool_calls"])
			}
			if got["prompt_cache_key"] != "thread-1" {
				t.Errorf("prompt_cache_key = %v, want thread-1", got["prompt_cache_key"])
			}
			if instr, _ := got["instructions"].(string); !strings.Contains(instr, "Kraclaw") {
				t.Errorf("instructions = %q, want substring %q (embedded kraclaw prompt)", instr, "Kraclaw")
			}
			if _, ok := got["tools"].([]any); !ok {
				t.Errorf("tools = %#v, want array", got["tools"])
			}
			input, ok := got["input"].([]any)
			if !ok {
				t.Fatalf("input = %#v, want array", got["input"])
			}
			if len(input) != 3 {
				t.Errorf("len(input) = %d, want 3", len(input))
			}
			last := input[2].(map[string]any)
			if last["type"] != "message" || last["role"] != "user" {
				t.Errorf("last input = %#v, want user message", last)
			}
			content := last["content"].([]any)[0].(map[string]any)
			if content["type"] != "input_text" || content["text"] != "next" {
				t.Errorf("content = %#v, want input_text next", content)
			}
			userMsg := input[0].(map[string]any)
			userContent := userMsg["content"].([]any)[0].(map[string]any)
			if userContent["type"] != "input_text" {
				t.Errorf("user history content type = %v, want input_text", userContent["type"])
			}
			assistantMsg := input[1].(map[string]any)
			assistantContent := assistantMsg["content"].([]any)[0].(map[string]any)
			if assistantContent["type"] != "output_text" {
				t.Errorf("assistant history content type = %v, want output_text", assistantContent["type"])
			}
			metadata := got["client_metadata"].(map[string]any)
			if metadata["x-codex-installation-id"] != "install-1" {
				t.Errorf("client_metadata = %#v, want installation id", metadata)
			}
			if text, _ := got["text"].(map[string]any); text == nil || text["verbosity"] != "medium" {
				t.Errorf("text = %#v, want {verbosity: medium}", got["text"])
			}

			reasoning, hasReason := got["reasoning"]
			include, _ := got["include"].([]any)
			if tt.wantReasonOK {
				if !hasReason || reasoning == nil {
					t.Errorf("reasoning = %#v, want non-nil for %q", reasoning, tt.model)
				} else if m, ok := reasoning.(map[string]any); !ok || m["summary"] != "auto" {
					t.Errorf("reasoning = %#v, want {summary: auto, ...}", reasoning)
				}
				found := false
				for _, v := range include {
					if v == "reasoning.encrypted_content" {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("include = %#v, want to contain %q", include, "reasoning.encrypted_content")
				}
			} else {
				if hasReason && reasoning != nil {
					t.Errorf("reasoning = %#v, want nil/omitted for %q", reasoning, tt.model)
				}
				if len(include) != 0 {
					t.Errorf("include = %#v, want empty for %q", include, tt.model)
				}
			}
		})
	}
}

func TestCodexClientStreamResponseHeaders(t *testing.T) {
	t.Parallel()

	var gotHeader http.Header
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	client := newCodexClient(server.URL, "tui:g1")
	client.sessionID = "session-1"
	client.threadID = "thread-1"
	client.installationID = "install-1"

	got, err := client.streamResponse(context.Background(), "gpt-5-codex", nil, "hi")
	if err != nil {
		t.Fatalf("streamResponse() err = %v, want nil", err)
	}
	if got != "hello" {
		t.Errorf("streamResponse() = %q, want hello", got)
	}
	if gotPath != "/v1/responses" {
		t.Errorf("path = %q, want /v1/responses", gotPath)
	}
	wants := map[string]string{
		"X-Kraclaw-Group":         "tui:g1",
		"X-Kraclaw-Provider":      "openai",
		"Accept":                  "text/event-stream",
		"Content-Type":            "application/json",
		"version":                 codexClientVersion,
		"session-id":              "session-1",
		"thread-id":               "thread-1",
		"x-client-request-id":     "thread-1",
		"x-codex-installation-id": "install-1",
		"x-codex-window-id":       "thread-1:0",
	}
	for key, want := range wants {
		if got := gotHeader.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestParseSSEText(t *testing.T) {
	t.Parallel()

	stream := strings.NewReader(strings.Join([]string{
		": comment",
		"event: ignored",
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}",
		"",
		"data: {\"type\":\"unknown\",\"delta\":\"ignored\"}",
		"",
		"data: {\"type\":\"response.text.delta\",\"delta\":\"lo\"}",
		"",
		"data: [DONE]",
		"",
		"data: {\"type\":\"response.text.delta\",\"delta\":\" ignored\"}",
		"",
	}, "\n"))

	got, err := parseSSEText(stream)
	if err != nil {
		t.Fatalf("parseSSEText() err = %v, want nil", err)
	}
	if got != "hello" {
		t.Errorf("parseSSEText() = %q, want hello", got)
	}
}

func TestCodexClientNon2xxIncludesCappedBody(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("x", maxErrorBodyBytes+100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := newCodexClient(server.URL, "tui:g1")
	_, err := client.streamResponse(context.Background(), "gpt-5-codex", nil, "hi")
	if err == nil {
		t.Fatal("streamResponse() err = nil, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "400 Bad Request") {
		t.Errorf("error = %q, want status", msg)
	}
	if strings.Contains(msg, strings.Repeat("x", maxErrorBodyBytes+1)) {
		t.Errorf("error body was not capped")
	}
}

func TestReasoningForModel(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		model      string
		wantReason bool
	}{
		"empty model":         {model: "", wantReason: false},
		"gpt-5-codex":         {model: "gpt-5-codex", wantReason: true},
		"gpt-5.2":             {model: "gpt-5.2", wantReason: true},
		"GPT-5 uppercase":     {model: "GPT-5", wantReason: true},
		"o3-mini":             {model: "o3-mini", wantReason: true},
		"o4-preview":          {model: "o4-preview", wantReason: true},
		"o1-mini":             {model: "o1-mini", wantReason: true},
		"gpt-4-turbo":         {model: "gpt-4-turbo", wantReason: false},
		"claude-sonnet-4-6":   {model: "claude-sonnet-4-6", wantReason: false},
		"text-embedding":      {model: "text-embedding-3-large", wantReason: false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := reasoningForModel(tt.model)
			if tt.wantReason && got == nil {
				t.Errorf("reasoningForModel(%q) = nil, want non-nil", tt.model)
			}
			if !tt.wantReason && got != nil {
				t.Errorf("reasoningForModel(%q) = %#v, want nil", tt.model, got)
			}
			if got != nil && got.Summary != "auto" {
				t.Errorf("reasoningForModel(%q).Summary = %q, want auto", tt.model, got.Summary)
			}
		})
	}
}
