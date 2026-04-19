package main

import (
	"strings"

	"charm.land/glamour/v2"
)

type chatMessage struct {
	sender  string // "you", "agent", "system"
	content string
}

// formatChatMessages groups consecutive messages from the same sender, prints
// a coral/kraken label line on cluster boundaries, and renders content as
// markdown for agent replies. Lines starting with "» " are treated as
// tool-use sub-lines and rendered dim to match the design.
func (m model) formatChatMessages() string {
	w := m.chatViewport.Width()
	if w < 10 {
		w = 10
	}

	var b strings.Builder
	var prevSender string
	for i, msg := range m.chatMessages {
		if msg.sender != prevSender {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString("  " + renderSenderLabel(msg.sender) + "\n")
		}
		body := msg.content
		if msg.sender == "agent" {
			body = renderMarkdown(m.mdRenderer, body)
		}
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "» ") {
				b.WriteString("    " + dimStyle.Render(line) + "\n")
			} else if line == "" {
				b.WriteString("\n")
			} else {
				b.WriteString("    " + line + "\n")
			}
		}
		prevSender = msg.sender
	}

	return strings.TrimRight(b.String(), "\n")
}

func renderSenderLabel(sender string) string {
	switch sender {
	case "you":
		return userLabelStyle.Render("you")
	case "agent":
		return agentLabelStyle.Render("kraclaw")
	default:
		return systemLabelStyle.Render(sender)
	}
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
