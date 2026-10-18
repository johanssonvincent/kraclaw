package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/johanssonvincent/kraclaw/internal/mcp"
	"github.com/johanssonvincent/kraclaw/internal/store"
	"github.com/johanssonvincent/kraclaw/pkg/agent"
)

type slogAdapter struct{}

func (s slogAdapter) Info(msg string, fields ...any) {
	slog.Info(msg, fields...)
}

func (s slogAdapter) Error(msg string, fields ...any) {
	slog.Error(msg, fields...)
}

func (s slogAdapter) Debug(msg string, fields ...any) {
	slog.Debug(msg, fields...)
}

type mcpToolClient struct {
	stdioClients []*mcp.Client
	httpClients  []*mcp.HTTPClient
	mu           sync.RWMutex
	tools        []mcp.ToolInfo
}

func newMCPToolClient(ctx context.Context) (*mcpToolClient, error) {
	serversJSON := os.Getenv("KRACLAW_MCP_SERVERS")
	if serversJSON == "" {
		return &mcpToolClient{}, nil
	}

	servers, err := store.UnmarshalJSONFromEnv(serversJSON)
	if err != nil {
		return nil, fmt.Errorf("parse mcp servers: %w", err)
	}

	client := &mcpToolClient{}
	logger := slogAdapter{}

	for _, srv := range servers {
		if !srv.Enabled {
			continue
		}

		if err := srv.Validate(); err != nil {
			slog.Warn("mcp server config invalid, skipping", "id", srv.ID, "error", err)

			continue
		}

		switch srv.Transport {
		case "stdio":
			transport := mcp.NewStdioTransport(srv.Command, srv.Args, nil, logger)

			c := mcp.New(transport, logger)
			if err := c.Connect(ctx); err != nil {
				slog.Warn("mcp stdio connect failed, skipping", "id", srv.ID, "error", err)

				continue
			}

			client.stdioClients = append(client.stdioClients, c)

			slog.Info("mcp stdio connected", "id", srv.ID, "name", srv.Name)
		case "http":
			c := mcp.NewHTTPClient(srv.URL, srv.Headers, logger)
			if err := c.Connect(ctx); err != nil {
				slog.Warn("mcp http connect failed, skipping", "id", srv.ID, "error", err)

				continue
			}

			client.httpClients = append(client.httpClients, c)

			slog.Info("mcp http connected", "id", srv.ID, "name", srv.Name)
		}
	}

	// Discover all tools.
	if err := client.discoverTools(ctx); err != nil {
		slog.Error("mcp tool discovery failed", "error", err)
	}

	slog.Info("mcp tools loaded", "count", len(client.tools))

	return client, nil
}

func (c *mcpToolClient) discoverTools(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var allTools []mcp.ToolInfo

	for _, client := range c.stdioClients {
		tools, err := client.ListTools(ctx)
		if err != nil {
			slog.Warn("mcp list tools failed", "error", err)

			continue
		}

		allTools = append(allTools, tools...)
	}

	for _, client := range c.httpClients {
		tools, err := client.ListTools(ctx)
		if err != nil {
			slog.Warn("mcp list tools failed", "error", err)

			continue
		}

		allTools = append(allTools, tools...)
	}

	c.tools = allTools

	return nil
}

func (c *mcpToolClient) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.ToolResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var lastErr error

	// Try stdio clients first.
	for _, client := range c.stdioClients {
		result, err := client.CallTool(ctx, name, args)
		if err == nil {
			return result, nil
		}
		// Store last error but keep trying other servers.

		lastErr = err
	}

	// Try HTTP clients.
	for _, client := range c.httpClients {
		result, err := client.CallTool(ctx, name, args)
		if err == nil {
			return result, nil
		}

		lastErr = err
	}

	if lastErr != nil {
		return nil, fmt.Errorf("tool %q failed on all mcp servers: %w", name, lastErr)
	}

	return nil, fmt.Errorf("tool %q not found on any mcp server", name)
}

func (c *mcpToolClient) Close() {
	for _, client := range c.stdioClients {
		_ = client.Disconnect()
	}

	for _, client := range c.httpClients {
		_ = client.Disconnect()
	}
}

func formatMCPToolsAsPrompt(tools []mcp.ToolInfo) string {
	if len(tools) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Available MCP Tools\n\n")
	sb.WriteString("You have access to the following tools via MCP servers. Use them when relevant to the user's request.\n\n")

	for _, tool := range tools {
		fmt.Fprintf(&sb, "### %s\n", tool.Name)

		if tool.Description != "" {
			fmt.Fprintf(&sb, "%s\n\n", tool.Description)
		}

		if len(tool.InputSchema) > 0 {
			sb.WriteString("Input schema:\n")
			sb.WriteString(string(tool.InputSchema))
			sb.WriteString("\n\n")
		}
	}

	return sb.String()
}

func main() {
	if err := agent.Run(runAnthropic); err != nil {
		slog.Error("agent failed", "error", err)
		os.Exit(1)
	}
}

func runAnthropic(ctx context.Context, ipc *agent.IPCClient, log *slog.Logger) error {
	model := os.Getenv("ANTHROPIC_MODEL")
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	proxyURL := os.Getenv("KRACLAW_PROXY_URL")
	if proxyURL == "" {
		return fmt.Errorf("KRACLAW_PROXY_URL is required")
	}

	groupJID := os.Getenv("KRACLAW_GROUP")
	if groupJID == "" {
		return fmt.Errorf("KRACLAW_GROUP is required")
	}

	maxTokens := int64(8192)

	// Initialize MCP tool client.
	mcpClient, err := newMCPToolClient(ctx)
	if err != nil {
		return fmt.Errorf("init mcp client: %w", err)
	}
	defer mcpClient.Close()

	// Create Anthropic client pointing at the credential proxy.
	client := anthropic.NewClient(
		option.WithAPIKey("placeholder"), // Proxy injects real key.
		option.WithBaseURL(proxyURL),
		option.WithHeader("X-Kraclaw-Group", groupJID),
	)

	log.Info("anthropic agent ready", "model", model, "proxy", proxyURL)

	var history []anthropic.MessageParam

	// Build system prompt with MCP tools info if available.
	mcpToolsPrompt := formatMCPToolsAsPrompt(mcpClient.tools)

	systemPrompt := "You are an AI assistant running in a Kraclaw sandbox."
	if mcpToolsPrompt != "" {
		systemPrompt += "\n\nWhen you need to use a tool, respond ONLY with a tool call line and nothing else. Use this exact format:\nTOOL_CALL:<tool_name>:<json_args>\n\nExample: TOOL_CALL:get_weather:{\"city\": \"London\"}\n\nDo not include any other text before or after the tool call.\n\n" + mcpToolsPrompt
	}

	inputCh, ipcErrCh, err := ipc.ReadInput(ctx)
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-ipcErrCh:
			return fmt.Errorf("ipc read failure: %w", err)
		case msg, ok := <-inputCh:
			if !ok {
				return fmt.Errorf("ipc input channel closed unexpectedly")
			}

			switch msg.Type {
			case "message":
				text, err := extractMessageText(msg.Payload)
				if err != nil {
					log.Warn("failed to extract message text", "error", err)

					continue
				}

				// Tool result callbacks are handled by the conversation flow.
				// A more sophisticated implementation would track pending tool calls.

				msgs := make([]anthropic.MessageParam, len(history)+1)
				copy(msgs, history)
				msgs[len(history)] = anthropic.NewUserMessage(anthropic.NewTextBlock(text))

				stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
					Model:     model,
					MaxTokens: maxTokens,
					Messages:  msgs,
					System: []anthropic.TextBlockParam{
						{Type: "text", Text: systemPrompt},
					},
				})

				var buf strings.Builder

				for stream.Next() {
					event := stream.Current()
					switch delta := event.AsAny().(type) {
					case anthropic.ContentBlockDeltaEvent:
						if textDelta, ok := delta.Delta.AsAny().(anthropic.TextDelta); ok {
							buf.WriteString(textDelta.Text)
						}
					}
				}

				fullResponse := buf.String()

				if err := stream.Err(); err != nil {
					log.Error("anthropic stream error", "error", err)

					if sendErr := ipc.SendOutput(ctx, &agent.OutboundMessage{
						Type: "message",
						Text: "I encountered an error processing your message. Please try again.",
					}); sendErr != nil {
						log.Error("failed to send error message", "error", sendErr)
					}

					continue
				}

				// Check for MCP tool call.
				if toolCall, ok := extractToolCall(fullResponse); ok {
					log.Info("calling mcp tool", "name", toolCall.Name, "args", toolCall.Args)

					result, err := mcpClient.CallTool(ctx, toolCall.Name, toolCall.Args)
					if err != nil {
						log.Error("mcp tool call failed", "tool", toolCall.Name, "error", err)

						if sendErr := ipc.SendOutput(ctx, &agent.OutboundMessage{
							Type: "message",
							Text: fmt.Sprintf("Tool %s failed: %v", toolCall.Name, err),
						}); sendErr != nil {
							log.Error("failed to send error message", "error", sendErr)
						}

						continue
					}

					// Format tool result.
					resultText := formatToolResult(result)

					// Send result back to user.
					if err := ipc.SendOutput(ctx, &agent.OutboundMessage{
						Type: "message",
						Text: resultText,
					}); err != nil {
						log.Error("failed to send tool result", "error", err)

						continue
					}

					// Append tool interaction to history.
					history = append(history, anthropic.NewUserMessage(anthropic.NewTextBlock(text)))
					history = append(history, anthropic.NewAssistantMessage(anthropic.NewTextBlock(fullResponse)))
					history = append(history, anthropic.NewUserMessage(anthropic.NewTextBlock("Tool result: "+resultText)))

					continue
				}

				if fullResponse == "" {
					log.Warn("anthropic returned empty response", "model", model)

					fullResponse = "I received an empty response from the model. Please try again."
				}

				if err := ipc.SendOutput(ctx, &agent.OutboundMessage{
					Type: "message",
					Text: fullResponse,
				}); err != nil {
					log.Error("failed to send response, discarding from history", "error", err)

					continue
				}
				// Only append to history after successful send.
				history = append(history, anthropic.NewUserMessage(anthropic.NewTextBlock(text)))
				history = append(history, anthropic.NewAssistantMessage(anthropic.NewTextBlock(fullResponse)))

			case "set_model":
				var payload struct {
					Model string `json:"model"`
				}
				if err := json.Unmarshal(msg.Payload, &payload); err != nil {
					log.Error("failed to unmarshal set_model payload", "error", err)
				} else if payload.Model == "" {
					log.Warn("set_model received with empty model")
				} else {
					model = payload.Model
					log.Info("model updated", "model", model)
				}

			case "shutdown":
				log.Info("shutdown signal received")

				return nil

			default:
				log.Debug("unknown message type", "type", msg.Type)
			}
		}
	}
}

type toolCall struct {
	Name string
	Args map[string]any
}

// extractToolCall finds a TOOL_CALL: line in the response and parses it.
// Returns the parsed tool call and true if found, or nil and false if not.
func extractToolCall(response string) (*toolCall, bool) {
	for _, line := range strings.Split(response, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "TOOL_CALL:") {
			continue
		}

		tc, err := parseToolCall(line)
		if err != nil {
			return nil, false
		}

		return tc, true
	}

	return nil, false
}

func parseToolCall(text string) (*toolCall, error) {
	// Format: TOOL_CALL:<name>:<json_args>
	// Trim whitespace first.
	text = strings.TrimSpace(text)

	// Check prefix.
	const prefix = "TOOL_CALL:"
	if !strings.HasPrefix(text, prefix) {
		return nil, fmt.Errorf("invalid tool call format: missing prefix")
	}

	rest := strings.TrimPrefix(text, prefix)

	// Split on first colon to get name.
	colonIdx := strings.Index(rest, ":")
	if colonIdx < 0 {
		return nil, fmt.Errorf("invalid tool call format: missing arguments")
	}

	name := rest[:colonIdx]
	argsJSON := rest[colonIdx+1:]

	if name == "" {
		return nil, fmt.Errorf("invalid tool call format: empty name")
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return nil, fmt.Errorf("parse tool args: %w", err)
	}

	return &toolCall{Name: name, Args: args}, nil
}

func formatToolResult(result *mcp.ToolResult) string {
	var sb strings.Builder
	if result.IsError {
		sb.WriteString("Error: ")
	}

	for i, block := range result.Content {
		if i > 0 {
			sb.WriteString("\n\n")
		}

		sb.WriteString(block.Text)
	}

	return sb.String()
}

func extractMessageText(payload json.RawMessage) (string, error) {
	var p struct {
		Messages string `json:"messages"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", err
	}

	if p.Messages != "" {
		return p.Messages, nil
	}

	if p.Text != "" {
		return p.Text, nil
	}

	return "", fmt.Errorf("no text content in payload")
}
