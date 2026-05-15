package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"

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
		model = "gpt-5.4"
	}
	proxyURL := os.Getenv("KRACLAW_PROXY_URL")
	if proxyURL == "" {
		return fmt.Errorf("KRACLAW_PROXY_URL is required")
	}
	groupJID := os.Getenv("KRACLAW_GROUP")
	if groupJID == "" {
		return fmt.Errorf("KRACLAW_GROUP is required")
	}

	// Create OpenAI client pointing at the credential proxy.
	opts := []option.RequestOption{
		option.WithAPIKey("placeholder"), // Proxy injects real key.
	}
	opts = append(opts, option.WithBaseURL(proxyURL+"/v1"))
	opts = append(opts, option.WithHeader("X-Kraclaw-Group", groupJID))

	client := openai.NewClient(opts...)

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

				input := responseInput(history, text)
				stream := client.Responses.NewStreaming(ctx, responses.ResponseNewParams{
					Model: shared.ResponsesModel(model),
					Input: responses.ResponseNewParamsInputUnion{OfString: openai.String(input)},
				})

				var buf strings.Builder
				for stream.Next() {
					event := stream.Current()
					if delta, ok := event.AsAny().(responses.ResponseTextDeltaEvent); ok {
						buf.WriteString(delta.Delta)
					}
				}
				fullResponse := buf.String()
				if err := stream.Err(); err != nil {
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

func responseInput(history []conversationTurn, text string) string {
	var b strings.Builder
	for _, msg := range history {
		b.WriteString(strings.ToUpper(msg.role[:1]))
		b.WriteString(msg.role[1:])
		b.WriteString(": ")
		b.WriteString(msg.text)
		b.WriteString("\n\n")
	}
	b.WriteString("User: ")
	b.WriteString(text)
	return b.String()
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
