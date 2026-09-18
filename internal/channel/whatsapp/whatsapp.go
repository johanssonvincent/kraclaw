package whatsapp

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

const jidPrefix = "whatsapp:"

func init() {
	channel.DefaultRegistry.Register("whatsapp", func(cfg channel.ChannelConfig) (channel.Channel, error) {
		accessToken := os.Getenv("WHATSAPP_ACCESS_TOKEN")
		if accessToken == "" {
			return nil, nil
		}

		return New(cfg), nil
	})
}

// WhatsApp implements the channel.Channel interface for WhatsApp Business API.
type WhatsApp struct {
	accessToken string
	phoneNumberID string
	apiVersion  string
	connected   bool
	cfg         channel.ChannelConfig
	mu          sync.RWMutex
	log         *slog.Logger
}

// New creates a new WhatsApp channel.
func New(cfg channel.ChannelConfig) *WhatsApp {
	return &WhatsApp{
		accessToken: os.Getenv("WHATSAPP_ACCESS_TOKEN"),
		phoneNumberID: os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
		apiVersion:  os.Getenv("WHATSAPP_API_VERSION"),
		cfg:         cfg,
		log:         slog.With("channel", "whatsapp"),
	}
}

func (w *WhatsApp) Name() string { return "whatsapp" }

func (w *WhatsApp) Connect(_ context.Context) error {
	if w.apiVersion == "" {
		w.apiVersion = "v21.0"
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	w.connected = true
	w.log.Info("whatsapp channel ready", "api_version", w.apiVersion)

	return nil
}

// HandleWebhook processes an incoming webhook from Meta.
// This should be wired into the server's HTTP router at /webhooks/whatsapp.
func (w *WhatsApp) HandleWebhook(req *http.Request) error {
	// Verify request signature.
	verifyToken := os.Getenv("WHATSAPP_VERIFY_TOKEN")
	mode := req.URL.Query().Get("hub.mode")
	token := req.URL.Query().Get("hub.verify_token")
	challenge := req.URL.Query().Get("hub.challenge")

	if mode == "subscribe" && token == verifyToken {
		w.log.Info("whatsapp webhook verified", "challenge", challenge)
		return nil
	}

	if req.Method != http.MethodPost {
		return fmt.Errorf("unsupported method: %s", req.Method)
	}

	var entry struct {
		Entry []struct {
			Messaging []struct {
				Sender struct {
					ID string `json:"id"`
				} `json:"sender"`
				Contact struct {
					Profile struct {
						Name string `json:"name"`
					} `json:"profile"`
				} `json:"contact"`
				Messages []struct {
					ID    string `json:"id"`
					Timestamp string `json:"timestamp"`
					Type  string `json:"type"`
					Text  struct {
						Body string `json:"body"`
					} `json:"text"`
				} `json:"messages"`
			} `json:"messaging"`
		} `json:"entry"`
	}

	if err := json.NewDecoder(req.Body).Decode(&entry); err != nil {
		return fmt.Errorf("decode webhook payload: %w", err)
	}

	for _, e := range entry.Entry {
		for _, msg := range e.Messaging {
			for _, m := range msg.Messages {
				if m.Type != "text" {
					continue
				}

				chatJID := jidPrefix + msg.Sender.ID

				inbound := &channel.InboundMessage{
					ID:         m.ID,
					ChatJID:    chatJID,
					Sender:     msg.Sender.ID,
					SenderName: msg.Contact.Profile.Name,
					Content:    m.Text.Body,
					Timestamp:  time.Now(),
					IsGroup:    false,
				}

				if w.cfg.OnMessage != nil {
					w.cfg.OnMessage(chatJID, inbound)
				}
			}
		}
	}

	return nil
}

type whatsappPayload struct {
	MessagingProduct string      `json:"messaging_product"`
	To               string      `json:"to"`
	Type             string      `json:"type"`
	Text             textContent `json:"text"`
}

type textContent struct {
	Body string `json:"body"`
}

func (w *WhatsApp) SendMessage(_ context.Context, jid string, text string) error {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if !w.connected {
		return fmt.Errorf("whatsapp not connected")
	}

	phoneNumber := strings.TrimPrefix(jid, jidPrefix)
	if phoneNumber == "" {
		return fmt.Errorf("no phone number specified")
	}

	payload := whatsappPayload{
		MessagingProduct: "whatsapp",
		To:               phoneNumber,
		Type:             "text",
		Text: textContent{Body: text},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal whatsapp payload: %w", err)
	}

	url := fmt.Sprintf("https://graph.facebook.com/%s/%s/messages", w.apiVersion, w.phoneNumberID)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create whatsapp request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.accessToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send whatsapp message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		return fmt.Errorf("whatsapp API returned %d: %s", resp.StatusCode, buf.String())
	}

	return nil
}

func (w *WhatsApp) SetTyping(_ context.Context, _ string, _ bool) error {
	// No typing indicator for WhatsApp Business API.
	return nil
}

func (w *WhatsApp) IsConnected() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.connected
}

func (w *WhatsApp) OwnsJID(jid string) bool {
	return strings.HasPrefix(jid, jidPrefix)
}

func (w *WhatsApp) Disconnect(_ context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.connected = false
	w.log.Info("whatsapp channel disconnected")

	return nil
}
