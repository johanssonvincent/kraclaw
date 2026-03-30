package discord

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/johanssonvincent/kraclaw/internal/channel"
)

const jidPrefix = "discord:"

func init() {
	channel.DefaultRegistry.Register("discord", func(cfg channel.ChannelConfig) (channel.Channel, error) {
		token := os.Getenv("DISCORD_TOKEN")
		if token == "" {
			return nil, nil
		}
		return New(token, cfg), nil
	})
}

// Discord implements the channel.Channel interface for Discord.
type Discord struct {
	session   *discordgo.Session
	token     string
	connected bool
	cfg       channel.ChannelConfig
	mu        sync.RWMutex
	log       *slog.Logger
	cancel    context.CancelFunc
}

// New creates a new Discord channel.
func New(token string, cfg channel.ChannelConfig) *Discord {
	return &Discord{
		token: token,
		cfg:   cfg,
		log:   slog.With("channel", "discord"),
	}
}

func (d *Discord) Name() string { return "discord" }

func (d *Discord) Connect(ctx context.Context) error {
	session, err := discordgo.New("Bot " + d.token)
	if err != nil {
		return fmt.Errorf("create discord session: %w", err)
	}

	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent

	session.AddHandler(d.handleMessage)

	if err := session.Open(); err != nil {
		return fmt.Errorf("open discord websocket: %w", err)
	}

	_, cancel := context.WithCancel(ctx)

	d.mu.Lock()
	d.session = session
	d.connected = true
	d.cancel = cancel
	d.mu.Unlock()

	d.log.Info("connected to Discord")
	return nil
}

func (d *Discord) handleMessage(_ *discordgo.Session, m *discordgo.MessageCreate) {
	// Skip messages from the bot itself.
	if m.Author.ID == d.session.State.User.ID {
		return
	}

	chatJID := jidPrefix + m.ChannelID

	isGroup := m.GuildID != ""

	msg := &channel.InboundMessage{
		ID:         m.ID,
		ChatJID:    chatJID,
		Sender:     m.Author.ID,
		SenderName: m.Author.Username,
		Content:    m.Content,
		Timestamp:  m.Timestamp,
		IsGroup:    isGroup,
	}

	if d.cfg.OnMessage != nil {
		d.cfg.OnMessage(chatJID, msg)
	}

	if d.cfg.OnChatMeta != nil {
		channelName := ""
		ch, err := d.session.Channel(m.ChannelID)
		if err == nil {
			channelName = ch.Name
		}
		d.cfg.OnChatMeta(chatJID, time.Now(), channelName, "discord", isGroup)
	}
}

func (d *Discord) SendMessage(_ context.Context, jid string, text string) error {
	d.mu.RLock()
	session := d.session
	d.mu.RUnlock()

	if session == nil {
		return fmt.Errorf("discord not connected")
	}

	channelID := strings.TrimPrefix(jid, jidPrefix)
	_, err := session.ChannelMessageSend(channelID, text)
	if err != nil {
		return fmt.Errorf("send discord message: %w", err)
	}
	return nil
}

func (d *Discord) SetTyping(_ context.Context, jid string, typing bool) error {
	if !typing {
		return nil
	}

	d.mu.RLock()
	session := d.session
	d.mu.RUnlock()

	if session == nil {
		return fmt.Errorf("discord not connected")
	}

	channelID := strings.TrimPrefix(jid, jidPrefix)
	return session.ChannelTyping(channelID)
}

func (d *Discord) IsConnected() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.connected
}

func (d *Discord) OwnsJID(jid string) bool {
	return strings.HasPrefix(jid, jidPrefix)
}

func (d *Discord) Disconnect(_ context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cancel != nil {
		d.cancel()
	}

	if d.session != nil {
		if err := d.session.Close(); err != nil {
			return fmt.Errorf("close discord session: %w", err)
		}
	}

	d.connected = false
	d.log.Info("disconnected from Discord")
	return nil
}
