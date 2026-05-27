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
	req := buildCodexResponsesRequest("gpt-5.4", []conversationTurn{
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

	if got["model"] != "gpt-5.4" {
		t.Fatalf("model = %v, want gpt-5.4", got["model"])
	}
	if got["stream"] != true {
		t.Fatalf("stream = %v, want true", got["stream"])
	}
	if got["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %v, want auto", got["tool_choice"])
	}
	if got["parallel_tool_calls"] != false {
		t.Fatalf("parallel_tool_calls = %v, want false", got["parallel_tool_calls"])
	}
	if got["prompt_cache_key"] != "thread-1" {
		t.Fatalf("prompt_cache_key = %v, want thread-1", got["prompt_cache_key"])
	}
	if _, ok := got["tools"].([]any); !ok {
		t.Fatalf("tools = %#v, want array", got["tools"])
	}
	input, ok := got["input"].([]any)
	if !ok {
		t.Fatalf("input = %#v, want array", got["input"])
	}
	if len(input) != 3 {
		t.Fatalf("len(input) = %d, want 3", len(input))
	}
	last := input[2].(map[string]any)
	if last["type"] != "message" || last["role"] != "user" {
		t.Fatalf("last input = %#v, want user message", last)
	}
	content := last["content"].([]any)[0].(map[string]any)
	if content["type"] != "input_text" || content["text"] != "next" {
		t.Fatalf("content = %#v, want input_text next", content)
	}
	metadata := got["client_metadata"].(map[string]any)
	if metadata["x-codex-installation-id"] != "install-1" {
		t.Fatalf("client_metadata = %#v, want installation id", metadata)
	}
}

func TestCodexClientStreamResponseHeaders(t *testing.T) {
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

	got, err := client.streamResponse(context.Background(), "gpt-5.4", nil, "hi")
	if err != nil {
		t.Fatalf("streamResponse() err = %v, want nil", err)
	}
	if got != "hello" {
		t.Fatalf("streamResponse() = %q, want hello", got)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
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
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestParseSSEText(t *testing.T) {
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
		t.Fatalf("parseSSEText() = %q, want hello", got)
	}
}

func TestCodexClientNon2xxIncludesCappedBody(t *testing.T) {
	body := strings.Repeat("x", maxErrorBodyBytes+100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := newCodexClient(server.URL, "tui:g1")
	_, err := client.streamResponse(context.Background(), "gpt-5.4", nil, "hi")
	if err == nil {
		t.Fatal("streamResponse() err = nil, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "400 Bad Request") {
		t.Fatalf("error = %q, want status", msg)
	}
	if strings.Contains(msg, strings.Repeat("x", maxErrorBodyBytes+1)) {
		t.Fatalf("error body was not capped")
	}
}
