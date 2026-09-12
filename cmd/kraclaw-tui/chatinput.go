package main

// renderInputBox renders the composer line as a coral "▌" p...
func renderInputBox(inputView string, width int) string {
	prompt := composerPromptStyle.Render("▌ ")

	return inputBoxStyle.Width(width).Render(prompt + inputView)
}
