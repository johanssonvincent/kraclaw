package router

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/johanssonvincent/kraclaw/internal/channel"
	"github.com/johanssonvincent/kraclaw/internal/store"
)

// Router handles outbound message routing and message formatting.
type Router struct {
	channels []channel.Channel
	store    store.Store
	log      *slog.Logger
}

// New creates a new Router.
func New(channels []channel.Channel, s store.Store) (*Router, error) {
	if s == nil {
		return nil, fmt.Errorf("router: store is required")
	}
	return &Router{
		channels: channels,
		store:    s,
		log:      slog.Default(),
	}, nil
}

// RouteOutbound sends a text message to the correct channel for the given JID.
func (r *Router) RouteOutbound(ctx context.Context, chatJID string, text string) error {
	stripped := stripInternalTags(text)
	if stripped == "" {
		return nil
	}

	for _, ch := range r.channels {
		if ch.OwnsJID(chatJID) && ch.IsConnected() {
			return ch.SendMessage(ctx, chatJID, stripped)
		}
	}
	return fmt.Errorf("no channel for JID: %s", chatJID)
}

// MatchesTrigger checks if message content starts with the trigger pattern (case-insensitive).
func (r *Router) MatchesTrigger(content string, pattern string) bool {
	if pattern == "" {
		return true
	}
	return strings.HasPrefix(strings.ToLower(content), strings.ToLower(pattern))
}

// FormatMessagesForAgent formats stored messages as XML for agent consumption.
func (r *Router) FormatMessagesForAgent(messages []store.Message, assistantName string) string {
	if len(messages) == 0 {
		return "<messages>\n</messages>"
	}

	var b strings.Builder
	b.WriteString("<messages>\n")
	for _, m := range messages {
		name := m.SenderName
		if m.IsBotMessage {
			name = assistantName
		}
		b.WriteString(fmt.Sprintf("<message sender=%q timestamp=%q>%s</message>\n",
			escapeXML(name),
			m.Timestamp.Format("2006-01-02T15:04:05"),
			escapeXML(m.Content),
		))
	}
	b.WriteString("</messages>")
	return b.String()
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

func stripInternalTags(text string) string {
	for {
		start := strings.Index(text, "<internal>")
		if start == -1 {
			break
		}
		end := strings.Index(text, "</internal>")
		if end == -1 {
			break
		}
		text = text[:start] + text[end+len("</internal>"):]
	}
	return strings.TrimSpace(text)
}
