package email

import (
	"context"
	"fmt"
	"log/slog"
	"net/smtp"
	"os"
	"strings"
	"sync"

	"github.com/johanssonvincent/kraclaw/internal/channel"
)

const jidPrefix = "email:"

func init() {
	channel.DefaultRegistry.Register("email", func(cfg channel.ChannelConfig) (channel.Channel, error) {
		smtpHost := os.Getenv("EMAIL_SMTP_HOST")
		if smtpHost == "" {
			return nil, nil
		}

		return New(cfg), nil
	})
}

// Email implements the channel.Channel interface for Email (outbound only).
type Email struct {
	connected bool
	cfg       channel.ChannelConfig
	mu        sync.RWMutex
	log       *slog.Logger
}

// New creates a new Email channel.
func New(cfg channel.ChannelConfig) *Email {
	return &Email{
		cfg: cfg,
		log: slog.With("channel", "email"),
	}
}

func (e *Email) Name() string { return "email" }

func (e *Email) Connect(_ context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.connected = true
	e.log.Info("email channel ready")

	return nil
}

func (e *Email) SendMessage(_ context.Context, jid string, text string) error {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.connected {
		return fmt.Errorf("email not connected")
	}

	to := strings.TrimPrefix(jid, jidPrefix)
	if to == "" {
		return fmt.Errorf("no recipient specified")
	}

	from := os.Getenv("EMAIL_FROM")
	if from == "" {
		from = "kraclaw@localhost"
	}

	smtpHost := os.Getenv("EMAIL_SMTP_HOST")
	smtpPort := os.Getenv("EMAIL_SMTP_PORT")

	if smtpPort == "" {
		smtpPort = "587"
	}

	smtpUser := os.Getenv("EMAIL_SMTP_USER")
	smtpPass := os.Getenv("EMAIL_SMTP_PASS")

	subject := os.Getenv("EMAIL_SUBJECT")
	if subject == "" {
		subject = "Message from Kraclaw"
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		from, to, subject, text)

	auth := smtp.PlainAuth("", smtpUser, smtpPass, smtpHost)

	addr := fmt.Sprintf("%s:%s", smtpHost, smtpPort)

	if err := smtp.SendMail(addr, auth, from, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("send email: %w", err)
	}

	e.log.Info("email sent", "to", to)

	return nil
}

func (e *Email) SetTyping(_ context.Context, _ string, _ bool) error {
	// No typing indicator for email.
	return nil
}

func (e *Email) IsConnected() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.connected
}

func (e *Email) OwnsJID(jid string) bool {
	return strings.HasPrefix(jid, jidPrefix)
}

func (e *Email) Disconnect(_ context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.connected = false
	e.log.Info("email channel disconnected")

	return nil
}
