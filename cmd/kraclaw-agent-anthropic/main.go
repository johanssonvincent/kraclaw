package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

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

	closeTicker := time.NewTicker(5 * time.Second)
	defer closeTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-closeTicker.C:
			closed, err := ipc.CheckCloseSignal(ctx)
			if err != nil {
				log.Warn("close signal check failed", "error", err)
			}
			if closed {
				log.Info("close signal detected")
				return nil
			}
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
