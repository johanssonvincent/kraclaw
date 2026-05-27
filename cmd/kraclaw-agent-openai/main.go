package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/johanssonvincent/kraclaw/pkg/agent"
)

type conversationTurn struct {
	role string
	text string
}

func main() {
	if err := agent.Run(runOpenAI); err != nil {
		slog.Error("agent failed", "error", err)
		os.Exit(1)
	}
}

func runOpenAI(ctx context.Context, ipc *agent.IPCClient, log *slog.Logger) error {
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-5.5"
	}
	proxyURL := os.Getenv("KRACLAW_PROXY_URL")
	if proxyURL == "" {
		return fmt.Errorf("KRACLAW_PROXY_URL is required")
	}
	groupJID := os.Getenv("KRACLAW_GROUP")
	if groupJID == "" {
		return fmt.Errorf("KRACLAW_GROUP is required")
	}

	client := newCodexClient(proxyURL, groupJID)

	log.Info("openai agent ready", "model", model, "proxy", proxyURL)

	var history []conversationTurn

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

				fullResponse, err := client.streamResponse(ctx, model, history, text)
				if err != nil {
					log.Error("openai stream error", "error", err)
					if sendErr := ipc.SendOutput(ctx, &agent.OutboundMessage{
						Type: "message",
						Text: "I encountered an error processing your message. Please try again.",
					}); sendErr != nil {
						log.Error("failed to send error message", "error", sendErr)
					}
					continue
				}

				if fullResponse == "" {
					log.Warn("openai returned empty response", "model", model)
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
				history = append(history, conversationTurn{role: "user", text: text})
				history = append(history, conversationTurn{role: "assistant", text: fullResponse})

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
