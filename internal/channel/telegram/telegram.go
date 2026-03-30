package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
	tgmodels "github.com/go-telegram/bot/models"
	"github.com/johanssonvincent/kraclaw/internal/channel"
)

const jidPrefix = "telegram:"

func init() {
	channel.DefaultRegistry.Register("telegram", func(cfg channel.ChannelConfig) (channel.Channel, error) {
		token := os.Getenv("TELEGRAM_TOKEN")
		if token == "" {
			return nil, nil
		}
		return New(token, cfg), nil
	})
}

// Telegram implements the channel.Channel interface for Telegram.
type Telegram struct {
	bot       *tgbot.Bot
	token     string
	connected bool
	cfg       channel.ChannelConfig
	mu        sync.RWMutex
	log       *slog.Logger
	cancel    context.CancelFunc
	botID     int64
}

// New creates a new Telegram channel.
func New(token string, cfg channel.ChannelConfig) *Telegram {
	return &Telegram{
		token: token,
		cfg:   cfg,
		log:   slog.With("channel", "telegram"),
	}
}

func (t *Telegram) Name() string { return "telegram" }

func (t *Telegram) Connect(ctx context.Context) error {
	b, err := tgbot.New(t.token, tgbot.WithDefaultHandler(t.handleUpdate))
	if err != nil {
		return fmt.Errorf("create telegram bot: %w", err)
	}

	botCtx, cancel := context.WithCancel(ctx)

	t.mu.Lock()
	t.bot = b
	t.connected = true
	t.cancel = cancel
	t.mu.Unlock()

	go b.Start(botCtx)

	t.log.Info("connected to Telegram")
	return nil
}

func (t *Telegram) handleUpdate(ctx context.Context, b *tgbot.Bot, update *tgmodels.Update) {
	if update.Message == nil {
		return
	}

	msg := update.Message

	// Skip messages from the bot itself.
	if msg.From != nil && msg.From.IsBot && msg.From.ID == t.botID {
		return
	}

	chatJID := jidPrefix + strconv.FormatInt(msg.Chat.ID, 10)

	senderName := ""
	senderID := ""
	if msg.From != nil {
		senderID = strconv.FormatInt(msg.From.ID, 10)
		senderName = strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName)
	}

	isGroup := msg.Chat.Type == tgmodels.ChatTypeGroup || msg.Chat.Type == tgmodels.ChatTypeSupergroup

	inbound := &channel.InboundMessage{
		ID:         strconv.Itoa(msg.ID),
		ChatJID:    chatJID,
		Sender:     senderID,
		SenderName: senderName,
		Content:    msg.Text,
		Timestamp:  time.Unix(int64(msg.Date), 0),
		IsGroup:    isGroup,
	}

	if t.cfg.OnMessage != nil {
		t.cfg.OnMessage(chatJID, inbound)
	}

	if t.cfg.OnChatMeta != nil {
		chatName := msg.Chat.Title
		if chatName == "" && msg.Chat.Username != "" {
			chatName = msg.Chat.Username
		}
		t.cfg.OnChatMeta(chatJID, time.Now(), chatName, "telegram", isGroup)
	}
}

func (t *Telegram) SendMessage(ctx context.Context, jid string, text string) error {
	t.mu.RLock()
	b := t.bot
	t.mu.RUnlock()

	if b == nil {
		return fmt.Errorf("telegram not connected")
	}

	chatID, err := chatIDFromJID(jid)
	if err != nil {
		return err
	}

	_, err = b.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	})
	if err != nil {
		return fmt.Errorf("send telegram message: %w", err)
	}
	return nil
}

func (t *Telegram) SetTyping(ctx context.Context, jid string, typing bool) error {
	if !typing {
		return nil
	}

	t.mu.RLock()
	b := t.bot
	t.mu.RUnlock()

	if b == nil {
		return fmt.Errorf("telegram not connected")
	}

	chatID, err := chatIDFromJID(jid)
	if err != nil {
		return err
	}

	_, err = b.SendChatAction(ctx, &tgbot.SendChatActionParams{
		ChatID: chatID,
		Action: tgmodels.ChatActionTyping,
	})
	return err
}

func (t *Telegram) IsConnected() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.connected
}

func (t *Telegram) OwnsJID(jid string) bool {
	return strings.HasPrefix(jid, jidPrefix)
}

func (t *Telegram) Disconnect(_ context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.cancel != nil {
		t.cancel()
	}

	t.connected = false
	t.log.Info("disconnected from Telegram")
	return nil
}

// chatIDFromJID extracts the int64 chat ID from a telegram JID.
func chatIDFromJID(jid string) (int64, error) {
	raw := strings.TrimPrefix(jid, jidPrefix)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid telegram chat ID %q: %w", raw, err)
	}
	return id, nil
}
