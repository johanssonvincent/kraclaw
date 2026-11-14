package slack

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/johanssonvincent/kraclaw/internal/channel"
	"github.com/slack-go/slack"
)

const jidPrefix = "slack:"

func init() {
	channel.DefaultRegistry.Register("slack", func(cfg channel.ChannelConfig) (channel.Channel, error) {
		token := os.Getenv("SLACK_TOKEN")
		if token == "" {
			return nil, nil
		}

		return New(token, cfg), nil
	})
}

// Slack implements the channel.Channel interface for Slack.
type Slack struct {
	api       *slack.Client
	token     string
	connected bool
	cfg       channel.ChannelConfig
	mu        sync.RWMutex
	log       *slog.Logger
	cancel    context.CancelFunc
	botID     string
}

// New creates a new Slack channel.
func New(token string, cfg channel.ChannelConfig) *Slack {
	return &Slack{
		token: token,
		cfg:   cfg,
		log:   slog.With("channel", "slack"),
	}
}

func (s *Slack) Name() string { return "slack" }

func (s *Slack) Connect(ctx context.Context) error {
	api := slack.New(s.token)

	// Get bot info first.
	info, err := api.AuthTest()
	if err != nil {
		return fmt.Errorf("slack auth test: %w", err)
	}

	_, cancel := context.WithCancel(ctx)

	s.mu.Lock()
	s.api = api
	s.connected = true
	s.cancel = cancel
	s.botID = info.UserID
	s.mu.Unlock()

	s.log.Info("connected to Slack", "bot", info.BotID)

	return nil
}

func (s *Slack) SendMessage(_ context.Context, jid string, text string) error {
	s.mu.RLock()
	api := s.api
	s.mu.RUnlock()

	if api == nil {
		return fmt.Errorf("slack not connected")
	}

	channelID := strings.TrimPrefix(jid, jidPrefix)

	_, _, err := api.PostMessageContext(context.Background(), channelID,
		slack.MsgOptionText(text, false),
		slack.MsgOptionAsUser(true),
	)
	if err != nil {
		return fmt.Errorf("send slack message: %w", err)
	}

	return nil
}

func (s *Slack) SetTyping(_ context.Context, jid string, typing bool) error {
	if !typing {
		return nil
	}

	// Slack doesn't have a direct typing indicator API for bots.
	// Return nil silently.
	return nil
}

func (s *Slack) IsConnected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.connected
}

func (s *Slack) OwnsJID(jid string) bool {
	return strings.HasPrefix(jid, jidPrefix)
}

func (s *Slack) Disconnect(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
	}

	s.connected = false
	s.log.Info("disconnected from Slack")

	return nil
}
