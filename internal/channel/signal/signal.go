package signal

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
	"time"

	"github.com/johanssonvincent/kraclaw/internal/channel"
)

const jidPrefix = "signal:"

func init() {
	channel.DefaultRegistry.Register("signal", func(cfg channel.ChannelConfig) (channel.Channel, error) {
		apiURL := os.Getenv("SIGNAL_API_URL")
		if apiURL == "" {
			return nil, nil
		}

		return New(cfg), nil
	})
}

// Signal implements the channel.Channel interface for Signal.
type Signal struct {
	apiURL    string
	connected bool
	cfg       channel.ChannelConfig
	mu        sync.RWMutex
	log       *slog.Logger
}

// New creates a new Signal channel.
func New(cfg channel.ChannelConfig) *Signal {
	return &Signal{
		apiURL: os.Getenv("SIGNAL_API_URL"),
		cfg:    cfg,
		log:    slog.With("channel", "signal"),
	}
}

func (s *Signal) Name() string { return "signal" }

func (s *Signal) Connect(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.connected = true
	s.log.Info("signal channel ready")

	return nil
}

// HandleWebhook processes an incoming webhook from Signal.
// This should be wired into the server's HTTP router at /webhooks/signal.
func (s *Signal) HandleWebhook(req *http.Request) error {
	if req.Method != http.MethodPost {
		return fmt.Errorf("unsupported method: %s", req.Method)
	}

	var msg struct {
		Type      string `json:"type"`
		Timestamp int64  `json:"timestamp"`
		Envelope  struct {
			Type        string `json:"type"`
			Source      string `json:"source"`
			SourceName  string `json:"source_name"`
			DataMessage struct {
				Body string `json:"body"`
			} `json:"data_message"`
		} `json:"envelope"`
	}

	if err := json.NewDecoder(req.Body).Decode(&msg); err != nil {
		return fmt.Errorf("decode signal webhook: %w", err)
	}

	if msg.Type != "message" || msg.Envelope.Type != "whispertext" {
		return nil
	}

	chatJID := jidPrefix + msg.Envelope.Source

	inbound := &channel.InboundMessage{
		ID:         fmt.Sprintf("%d", msg.Timestamp),
		ChatJID:    chatJID,
		Sender:     msg.Envelope.Source,
		SenderName: msg.Envelope.SourceName,
		Content:    msg.Envelope.DataMessage.Body,
		Timestamp:  time.Unix(msg.Timestamp/1000, 0),
		IsGroup:    false,
	}

	if s.cfg.OnMessage != nil {
		s.cfg.OnMessage(chatJID, inbound)
	}

	return nil
}

type signalPayload struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func (s *Signal) SendMessage(_ context.Context, jid string, text string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.connected {
		return fmt.Errorf("signal not connected")
	}

	phoneNumber := strings.TrimPrefix(jid, jidPrefix)
	if phoneNumber == "" {
		return fmt.Errorf("no phone number specified")
	}

	payload := signalPayload{
		Message: text,
		Type:    "text",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal signal payload: %w", err)
	}

	url := fmt.Sprintf("%s/v2/messages/%s", s.apiURL, phoneNumber)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create signal request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send signal message: %w", err)
	}

	defer func() {
		if err := resp.Body.Close(); err != nil {
			s.log.Warn("failed to close response body", "err", err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(resp.Body); err != nil {
			return fmt.Errorf("read response body: %w", err)
		}

		return fmt.Errorf("signal API returned %d: %s", resp.StatusCode, buf.String())
	}

	return nil
}

func (s *Signal) SetTyping(_ context.Context, _ string, _ bool) error {
	// No typing indicator for Signal REST API.
	return nil
}

func (s *Signal) IsConnected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.connected
}

func (s *Signal) OwnsJID(jid string) bool {
	return strings.HasPrefix(jid, jidPrefix)
}

func (s *Signal) Disconnect(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.connected = false
	s.log.Info("signal channel disconnected")

	return nil
}
