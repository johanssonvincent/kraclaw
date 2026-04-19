package main

// renderInputBox renders the composer line as a coral "▌" prompt glyph
// followed by the input view. The design omits the rounded border used in
// the previous theme — a single-line bar is the whole composer.
func renderInputBox(inputView string, width int) string {
	prompt := composerPromptStyle.Render("▌ ")
	return inputBoxStyle.Width(width).Render(prompt + inputView)
}
