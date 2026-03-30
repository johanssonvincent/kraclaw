package main

import (
	"image/color"
	"strings"

	"charm.land/glamour/v2"
)

type chatMessage struct {
	sender  string // "you" or "agent"
	content string
}

func (m model) formatChatMessages() string {
	w := m.chatViewport.Width()
	if w < 10 {
		w = 10
	}

	var b strings.Builder
	for i, msg := range m.chatMessages {
		var borderColor color.Color
		var label string
		var content string

		switch msg.sender {
		case "you":
			borderColor = defaultChatTheme.UserBorder
			label = userLabelStyle.Render("You")
			content = msg.content
		case "agent":
			borderColor = defaultChatTheme.AgentBorder
			label = agentLabelStyle.Render("Sentia")
			content = renderMarkdown(m.mdRenderer, msg.content)
		default:
			borderColor = defaultChatTheme.SystemBorder
			label = systemLabelStyle.Render(msg.sender)
			content = msg.content
		}

		block := label + "\n" + content
		bubble := messageBlockStyle.
			BorderForeground(borderColor).
			Width(w).
			Render(block)

		b.WriteString(bubble)
		if i < len(m.chatMessages)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func renderMarkdown(r *glamour.TermRenderer, content string) string {
	if r == nil {
		return content
	}
	rendered, err := r.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimRight(rendered, "\n")
}
