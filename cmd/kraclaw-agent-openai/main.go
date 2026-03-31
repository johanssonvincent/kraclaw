package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/johanssonvincent/kraclaw/pkg/agent"
)

func main() {
	if err := agent.Run(runOpenAI); err != nil {
		slog.Error("agent failed", "error", err)
		os.Exit(1)
	}
}

func runOpenAI(ctx context.Context, ipc *agent.IPCClient, log *slog.Logger) error {
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-5.4"
	}
	proxyURL := os.Getenv("KRACLAW_PROXY_URL")
	groupJID := os.Getenv("KRACLAW_GROUP")

	// Create OpenAI client pointing at the credential proxy.
	opts := []option.RequestOption{
		option.WithAPIKey("placeholder"), // Proxy injects real key.
	}
	if proxyURL != "" {
		opts = append(opts, option.WithBaseURL(proxyURL+"/v1"))
	}
	opts = append(opts, option.WithHeader("X-Kraclaw-Group", groupJID))

	client := openai.NewClient(opts...)

	log.Info("openai agent ready", "model", model, "proxy", proxyURL)

	var history []openai.ChatCompletionMessageParamUnion

	inputCh, err := ipc.ReadInput(ctx)
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-inputCh:
			if !ok {
				return nil
			}

			switch msg.Type {
			case "message":
				text, err := extractMessageText(msg.Payload)
				if err != nil {
					log.Warn("failed to extract message text", "error", err)
					continue
				}

				history = append(history, openai.UserMessage(text))

				stream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
					Model:    model,
					Messages: history,
				})

				var fullResponse string
				for stream.Next() {
					chunk := stream.Current()
					for _, choice := range chunk.Choices {
						if choice.Delta.Content != "" {
							fullResponse += choice.Delta.Content
						}
					}
				}
				if err := stream.Err(); err != nil {
					log.Error("openai stream error", "error", err)
					if sendErr := ipc.SendOutput(ctx, &agent.OutboundMessage{
						Type: "message",
						Text: fmt.Sprintf("Error: %v", err),
					}); sendErr != nil {
						log.Error("failed to send error message", "error", sendErr)
					}
					continue
				}

				history = append(history, openai.AssistantMessage(fullResponse))

				if err := ipc.SendOutput(ctx, &agent.OutboundMessage{
					Type: "message",
					Text: fullResponse,
				}); err != nil {
					log.Error("failed to send response", "error", err)
				}

			case "set_model":
				var payload struct {
					Model string `json:"model"`
				}
				if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.Model != "" {
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

		// Check close signal.
		closed, err := ipc.CheckCloseSignal(ctx)
		if err != nil {
			log.Warn("close signal check failed", "error", err)
		}
		if closed {
			log.Info("close signal detected")
			return nil
		}
	}
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
