package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/johanssonvincent/kraclaw/internal/tools"
	"github.com/johanssonvincent/kraclaw/pkg/agent"
)

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

	// Initialize web search client.
	searchClient := initSearchClient()
	if searchClient != nil {
		log.Info("web search enabled", "provider", searchClient.Provider())
	}

	// Build system prompt with tools info.
	systemPrompt := buildSystemPrompt(searchClient != nil)

	// Create Anthropic client pointing at the credential proxy.
	client := anthropic.NewClient(
		option.WithAPIKey("placeholder"), // Proxy injects real key.
		option.WithBaseURL(proxyURL),
		option.WithHeader("X-Kraclaw-Group", groupJID),
	)

	log.Info("anthropic agent ready", "model", model, "proxy", proxyURL)

	var history []anthropic.MessageParam

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

				// Check for tool call.
				if tc, ok := extractToolCall(fullResponse); ok {
					if resultText, handled := handleToolCall(ctx, tc, searchClient, log); handled {
						if sendErr := ipc.SendOutput(ctx, &agent.OutboundMessage{
							Type: "message",
							Text: resultText,
						}); sendErr != nil {
							log.Error("failed to send tool result", "error", sendErr)

							continue
						}

						// Append tool interaction to history.
						history = append(history, anthropic.NewUserMessage(anthropic.NewTextBlock(text)))
						history = append(history, anthropic.NewAssistantMessage(anthropic.NewTextBlock(fullResponse)))
						history = append(history, anthropic.NewUserMessage(anthropic.NewTextBlock("Tool result: "+resultText)))

						continue
					}
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

// initSearchClient creates a search client from environment variables.
func initSearchClient() *tools.SearchClient {
	provider := strings.ToLower(os.Getenv("KRACLAW_SEARCH_PROVIDER"))
	apiKey := os.Getenv("KRACLAW_SEARCH_API_KEY")

	if apiKey == "" {
		return nil
	}

	if provider == "" {
		provider = "tavily" // Default to Tavily.
	}

	engineID := os.Getenv("KRACLAW_SEARCH_ENGINE_ID") // For Google Custom Search.

	return tools.NewSearchClient(tools.Provider(provider), apiKey, engineID)
}

// buildSystemPrompt builds the system prompt with tools info.
func buildSystemPrompt(hasSearch bool) string {
	prompt := "You are an AI assistant running in a Kraclaw sandbox."

	if hasSearch {
		prompt += "\n\nWhen you need to use a tool, respond ONLY with a tool call line and nothing else. Use this exact format:\nTOOL_CALL:<tool_name>:<json_args>\n\nExample: TOOL_CALL:web_search:{\"query\": \"latest news\"}\n\nDo not include any other text before or after the tool call.\n\n"
		prompt += "## Available Tools\n\n"
		prompt += tools.FormatToolPrompt()
	}

	return prompt
}

// handleToolCall processes a tool call and returns the result text and whether it was handled.
func handleToolCall(ctx context.Context, tc *toolCall, searchClient *tools.SearchClient, log *slog.Logger) (string, bool) {
	switch tc.Name {
	case "web_search":
		return handleWebSearch(ctx, tc, searchClient, log)
	default:
		return "", false
	}
}

// handleWebSearch processes a web_search tool call.
func handleWebSearch(ctx context.Context, tc *toolCall, searchClient *tools.SearchClient, log *slog.Logger) (string, bool) {
	if searchClient == nil {
		return "Web search is not configured.", true
	}

	var params tools.SearchParams
	if err := json.Unmarshal(tc.ArgsJSON, &params); err != nil {
		return fmt.Sprintf("Invalid search parameters: %v", err), true
	}

	if params.Query == "" {
		return "Search query is required.", true
	}

	log.Info("web search", "query", params.Query, "max_results", params.MaxResults)

	results, err := searchClient.Search(ctx, params)
	if err != nil {
		return fmt.Sprintf("Search failed: %v", err), true
	}

	return tools.FormatAsPrompt(results), true
}

type toolCall struct {
	Name     string
	ArgsJSON json.RawMessage
}

// extractToolCall finds a TOOL_CALL: line in the response and parses it.
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
	text = strings.TrimSpace(text)

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
	argsJSON := json.RawMessage(rest[colonIdx+1:])

	if name == "" {
		return nil, fmt.Errorf("invalid tool call format: empty name")
	}

	return &toolCall{Name: name, ArgsJSON: argsJSON}, nil
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
