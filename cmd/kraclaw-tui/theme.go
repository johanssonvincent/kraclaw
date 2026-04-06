// Package main provides the kraclaw TUI client theme system.
// All color definitions and package-level style vars live here.
package main

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// chatTheme holds all named colors for the TUI. Fields are grouped by area:
// chat messages, general UI, panels, sidebar, and status bar.
type chatTheme struct {
	// Chat messages
	UserBorder    color.Color // #5C8FFF - user message left border
	AgentBorder   color.Color // #C77DFF - agent message left border
	SystemBorder  color.Color // #6C7086 - system message left border
	UserLabel     color.Color // #5C8FFF - "You" sender label
	AgentLabel    color.Color // #C77DFF - "Agent" sender label
	InputBorder   color.Color // #7C3AED - input box border
	InputPrompt   color.Color // #7C3AED - ">" prompt character

	// General UI
	Accent        color.Color // #7C3AED - primary accent
	Muted         color.Color // #6C7086 - de-emphasized text
	Text          color.Color // #CDD6F4 - primary foreground
	TextSecondary color.Color // #A6ADC8 - secondary foreground

	// Panels
	PanelBorder  color.Color // #313244 - subtle border between panels
	ActiveBorder color.Color // #7C3AED - focused panel border

	// Sidebar
	SidebarTitle color.Color // #CDD6F4 - sidebar section titles
	SidebarLabel color.Color // #6C7086 - sidebar key labels
	SidebarValue color.Color // #A6ADC8 - sidebar values

	// Status bar
	StatusBg color.Color // #1E1E2E - dark surface
	StatusFg color.Color // #A6ADC8 - secondary foreground
}

var defaultChatTheme = chatTheme{
	UserBorder:    lipgloss.Color("#5C8FFF"),
	AgentBorder:   lipgloss.Color("#C77DFF"),
	SystemBorder:  lipgloss.Color("#6C7086"),
	UserLabel:     lipgloss.Color("#5C8FFF"),
	AgentLabel:    lipgloss.Color("#C77DFF"),
	InputBorder:   lipgloss.Color("#7C3AED"),
	InputPrompt:   lipgloss.Color("#7C3AED"),
	Accent:        lipgloss.Color("#7C3AED"),
	Muted:         lipgloss.Color("#6C7086"),
	Text:          lipgloss.Color("#CDD6F4"),
	TextSecondary: lipgloss.Color("#A6ADC8"),
	PanelBorder:   lipgloss.Color("#313244"),
	ActiveBorder:  lipgloss.Color("#7C3AED"),
	SidebarTitle:  lipgloss.Color("#CDD6F4"),
	SidebarLabel:  lipgloss.Color("#6C7086"),
	SidebarValue:  lipgloss.Color("#A6ADC8"),
	StatusBg:      lipgloss.Color("#1E1E2E"),
	StatusFg:      lipgloss.Color("#A6ADC8"),
}

// Package-level style vars. All colors reference defaultChatTheme fields.

var (
	tabBorder = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(defaultChatTheme.Accent)

	activeTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#1E1E2E")).
			Background(defaultChatTheme.Accent).
			Padding(0, 2)

	inactiveTabStyle = lipgloss.NewStyle().
				Foreground(defaultChatTheme.TextSecondary).
				Padding(0, 2)

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(defaultChatTheme.Accent).
			MarginBottom(1)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(defaultChatTheme.StatusFg).
			Background(defaultChatTheme.StatusBg).
			Padding(0, 1)

	okStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))
	errStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F38BA8"))
	dimStyle = lipgloss.NewStyle().Foreground(defaultChatTheme.Muted)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(defaultChatTheme.Accent).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(defaultChatTheme.PanelBorder)

	evenRowStyle = lipgloss.NewStyle().Foreground(defaultChatTheme.Text)
	oddRowStyle  = lipgloss.NewStyle().Foreground(defaultChatTheme.TextSecondary)

	messageBlockStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				PaddingLeft(1).
				PaddingRight(1)

	inputBoxStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(defaultChatTheme.InputBorder).
			Padding(0, 1)
)

// Sender label styles extracted from formatChatMessages inline creation.

var (
	userLabelStyle   = lipgloss.NewStyle().Bold(true).Foreground(defaultChatTheme.UserLabel)
	agentLabelStyle  = lipgloss.NewStyle().Bold(true).Foreground(defaultChatTheme.AgentLabel)
	systemLabelStyle = lipgloss.NewStyle().Foreground(defaultChatTheme.SystemBorder)
)

// spinnerStyle replaces the inline lipgloss.Color("39") used for the spinner.
var spinnerStyle = lipgloss.NewStyle().Foreground(defaultChatTheme.Accent)
