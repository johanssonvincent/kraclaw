package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/johanssonvincent/kraclaw/internal/channel"
)

const jidPrefix = "teams:"

func init() {
	channel.DefaultRegistry.Register("teams", func(cfg channel.ChannelConfig) (channel.Channel, error) {
		webhookURL := os.Getenv("TEAMS_WEBHOOK_URL")
		if webhookURL == "" {
			return nil, nil
		}

		return New(webhookURL, cfg), nil
	})
}

// Teams implements the channel.Channel interface for Microsoft Teams.
type Teams struct {
	webhookURL string
	connected  bool
	cfg        channel.ChannelConfig
	mu         sync.RWMutex
	log        *slog.Logger
}

// New creates a new Teams channel.
func New(webhookURL string, cfg channel.ChannelConfig) *Teams {
	return &Teams{
		webhookURL: webhookURL,
		cfg:        cfg,
		log:        slog.With("channel", "teams"),
	}
}

func (t *Teams) Name() string { return "teams" }

func (t *Teams) Connect(_ context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.connected = true
	t.log.Info("teams channel ready")

	return nil
}

type adaptiveCard struct {
	Type        string `json:"type"`
	Version     string `json:"version"`
	Body        []bodyElement `json:"body"`
}

type bodyElement struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Wrap bool   `json:"wrap"`
}

type teamsPayload struct {
	Type      string       `json:"type"`
	Contents  []adaptiveCard `json:"contents"`
}

func (t *Teams) SendMessage(_ context.Context, jid string, text string) error {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if !t.connected {
		return fmt.Errorf("teams not connected")
	}

	// Parse webhook URL from JID if different, otherwise use default.
	webhookURL := t.webhookURL
	if jid != "" && jid != jidPrefix {
		// Support multiple webhooks via JID format: teams:<webhook_name>
		envKey := "TEAMS_WEBHOOK_" + strings.ToUpper(strings.TrimPrefix(jid, jidPrefix))
		if url := os.Getenv(envKey); url != "" {
			webhookURL = url
		}
	}

	payload := teamsPayload{
		Type: "message",
		Contents: []adaptiveCard{
			{
				Type:    "AdaptiveCard",
				Version: "1.6",
				Body: []bodyElement{
					{
						Type: "TextBlock",
						Text: text,
						Wrap: true,
					},
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal teams payload: %w", err)
	}

	resp, err := http.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("send teams message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("teams webhook returned %d", resp.StatusCode)
	}

	return nil
}

func (t *Teams) SetTyping(_ context.Context, _ string, _ bool) error {
	// No typing indicator for Teams webhooks.
	return nil
}

func (t *Teams) IsConnected() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.connected
}

func (t *Teams) OwnsJID(jid string) bool {
	return strings.HasPrefix(jid, jidPrefix)
}

func (t *Teams) Disconnect(_ context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.connected = false
	t.log.Info("teams channel disconnected")

	return nil
}
