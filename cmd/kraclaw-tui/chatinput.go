package main

import "charm.land/lipgloss/v2"

// renderInputBox renders the chat input area with themed styling.
// The inputBoxStyle and prompt styling come from theme.go.
func renderInputBox(inputView string, width int) string {
	prompt := lipgloss.NewStyle().
		Foreground(defaultChatTheme.InputPrompt).
		Bold(true).
		Render("> ")
	return inputBoxStyle.
		Width(width).
		Render(prompt + inputView)
}
