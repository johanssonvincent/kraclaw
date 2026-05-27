package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	codexClientVersion = "0.0.0-kraclaw"
	maxErrorBodyBytes  = 4096
)

//go:embed codex_prompt.md
var codexBaseInstructions string

type codexClient struct {
	proxyURL       string
	groupJID       string
	httpClient     *http.Client
	sessionID      string
	threadID       string
	installationID string
}

type codexResponsesRequest struct {
	Model             string            `json:"model"`
	Instructions      string            `json:"instructions"`
	Input             []codexInputItem  `json:"input"`
	Tools             []any             `json:"tools"`
	ToolChoice        string            `json:"tool_choice"`
	ParallelToolCalls bool              `json:"parallel_tool_calls"`
	Reasoning         *codexReasoning   `json:"reasoning,omitempty"`
	Store             bool              `json:"store"`
	Stream            bool              `json:"stream"`
	Include           []string          `json:"include"`
	PromptCacheKey    string            `json:"prompt_cache_key"`
	Text              *codexText        `json:"text,omitempty"`
	ClientMetadata    map[string]string `json:"client_metadata"`
}

type codexReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary"`
}

type codexText struct {
	Verbosity string `json:"verbosity,omitempty"`
}

type codexInputItem struct {
	Type    string             `json:"type"`
	Role    string             `json:"role"`
	Content []codexContentItem `json:"content"`
}

type codexContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func newCodexClient(proxyURL, groupJID string) *codexClient {
	return &codexClient{
		proxyURL:       strings.TrimRight(proxyURL, "/"),
		groupJID:       groupJID,
		httpClient:     &http.Client{Timeout: 600 * time.Second},
		sessionID:      uuid.NewString(),
		threadID:       uuid.NewString(),
		installationID: uuid.NewString(),
	}
}

func (c *codexClient) streamResponse(ctx context.Context, model string, history []conversationTurn, text string) (string, error) {
	body, err := json.Marshal(buildCodexResponsesRequest(model, history, text, c.threadID, c.installationID))
	if err != nil {
		return "", fmt.Errorf("marshal codex request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.proxyURL+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create codex request: %w", err)
	}
	setCodexHeaders(req, c.groupJID, c.sessionID, c.threadID, c.installationID)

	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("post codex response: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return "", fmt.Errorf("openai upstream returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	textOut, err := parseSSEText(resp.Body)
	if err != nil {
		return "", fmt.Errorf("parse codex stream: %w", err)
	}
	return textOut, nil
}

func buildCodexResponsesRequest(model string, history []conversationTurn, text string, threadID string, installationID string) codexResponsesRequest {
	input := make([]codexInputItem, 0, len(history)+1)
	for _, turn := range history {
		input = append(input, codexMessage(turn.role, turn.text))
	}
	input = append(input, codexMessage("user", text))

	reasoning := reasoningForModel(model)
	include := []string{}
	if reasoning != nil {
		include = append(include, "reasoning.encrypted_content")
	}

	return codexResponsesRequest{
		Model:             model,
		Instructions:      codexBaseInstructions,
		Input:             input,
		Tools:             []any{},
		ToolChoice:        "auto",
		ParallelToolCalls: false,
		Reasoning:         reasoning,
		Store:             false,
		Stream:            true,
		Include:           include,
		PromptCacheKey:    threadID,
		Text:              &codexText{Verbosity: "medium"},
		ClientMetadata: map[string]string{
			"x-codex-installation-id": installationID,
		},
	}
}

// reasoningForModel returns the reasoning config Codex sends for the given
// model slug. Codex enables reasoning for the gpt-5 / o-series families.
func reasoningForModel(model string) *codexReasoning {
	if model == "" {
		return nil
	}
	lower := strings.ToLower(model)
	switch {
	case strings.HasPrefix(lower, "gpt-5"),
		strings.HasPrefix(lower, "o1"),
		strings.HasPrefix(lower, "o3"),
		strings.HasPrefix(lower, "o4"):
		return &codexReasoning{Effort: "medium", Summary: "auto"}
	default:
		return nil
	}
}

func codexMessage(role string, text string) codexInputItem {
	return codexInputItem{
		Type: "message",
		Role: role,
		Content: []codexContentItem{{
			Type: "input_text",
			Text: text,
		}},
	}
}

func setCodexHeaders(req *http.Request, groupJID, sessionID, threadID, installationID string) {
	req.Header.Set("X-Kraclaw-Group", groupJID)
	req.Header.Set("X-Kraclaw-Provider", "openai")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("version", codexClientVersion)
	req.Header.Set("session-id", sessionID)
	req.Header.Set("thread-id", threadID)
	req.Header.Set("x-client-request-id", threadID)
	req.Header.Set("x-codex-installation-id", installationID)
	req.Header.Set("x-codex-window-id", threadID+":0")
}

func parseSSEText(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var dataLines []string
	var out strings.Builder

	flush := func() (bool, error) {
		if len(dataLines) == 0 {
			return false, nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if strings.TrimSpace(data) == "[DONE]" {
			return true, nil
		}
		var event struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return false, err
		}
		switch event.Type {
		case "response.output_text.delta", "response.text.delta":
			out.WriteString(event.Delta)
		}
		return false, nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			done, err := flush()
			if done || err != nil {
				return out.String(), err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok || field != "data" {
			continue
		}
		dataLines = append(dataLines, strings.TrimPrefix(value, " "))
	}
	if err := scanner.Err(); err != nil {
		return out.String(), err
	}
	_, err := flush()
	return out.String(), err
}
