package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// sidebarModel holds the sidebar panel state and data for session context display.
type sidebarModel struct {
	width        int
	height       int
	sessionID    string
	messageCount int
	uptime       string
	model        string
	processing   string
}

func newSidebarModel() sidebarModel {
	return sidebarModel{}
}

// coalesce returns s if non-empty, otherwise fallback.
func coalesce(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// View renders the sidebar content with session context key-value pairs.
// Returns empty string when dimensions are too small to display anything useful.
func (m sidebarModel) View() string {
	if m.width < 10 || m.height < 5 {
		return ""
	}

	label := lipgloss.NewStyle().Foreground(defaultChatTheme.SidebarLabel)
	val := lipgloss.NewStyle().Foreground(defaultChatTheme.SidebarValue)
	title := lipgloss.NewStyle().Bold(true).Foreground(defaultChatTheme.SidebarTitle)

	var b strings.Builder
	b.WriteString(title.Render("Session"))
	b.WriteString("\n\n")

	pairs := []struct{ k, v string }{
		{"Session ID", coalesce(m.sessionID, "-")},
		{"Messages", fmt.Sprintf("%d", m.messageCount)},
		{"Uptime", coalesce(m.uptime, "-")},
		{"Model", coalesce(m.model, "default")},
		{"Processing", coalesce(m.processing, "Idle")},
	}
	for i, p := range pairs {
		b.WriteString(label.Render(p.k))
		b.WriteString("\n")
		b.WriteString(val.Render(p.v))
		if i < len(pairs)-1 {
			b.WriteString("\n\n")
		}
	}

	return lipgloss.NewStyle().
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(defaultChatTheme.PanelBorder).
		Width(m.width).
		Height(m.height).
		PaddingLeft(1).
		PaddingRight(1).
		Render(b.String())
}
