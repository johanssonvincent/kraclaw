package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// sectionRule renders a full-width horizontal rule with an optional coral
// label inset at column 2, mirroring the design's hr(width, label) helper.
func sectionRule(width int, label string) string {
	if width <= 0 {
		return ""
	}
	if label == "" {
		return sectionRuleStyle.Render(strings.Repeat("─", width))
	}
	const leftPad = 2
	labelText := " " + label + " "
	lblLen := len(labelText)
	if leftPad+lblLen+1 >= width {
		return sectionRuleStyle.Render(strings.Repeat("─", width))
	}
	right := width - leftPad - lblLen
	return sectionRuleStyle.Render(strings.Repeat("─", leftPad)) +
		coralStyle.Render(labelText) +
		sectionRuleStyle.Render(strings.Repeat("─", right))
}

// keyBar renders a single-line footer of [key, label] hints with coral keys
// and dim labels. It does not pad to width; the caller decides background fill.
func keyBar(hints [][2]string, width int) string {
	if len(hints) == 0 {
		return ""
	}
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		parts = append(parts, keyHintKeyStyle.Render(h[0])+" "+keyHintLabelStyle.Render(h[1]))
	}
	joined := " " + strings.Join(parts, "  ")
	if width > 0 && lipgloss.Width(joined) < width {
		joined += strings.Repeat(" ", width-lipgloss.Width(joined))
	}
	return joined
}

// statusLine renders a width-filling inverse status bar with "│" separators
// between cells. Cells may contain lipgloss-styled segments.
func statusLine(cells []string, width int) string {
	if len(cells) == 0 || width <= 0 {
		return ""
	}
	sep := statusLineSepStyle.Render("│")
	segs := make([]string, 0, len(cells))
	for _, c := range cells {
		segs = append(segs, statusLineStyle.Render(" "+c+" "))
	}
	joined := strings.Join(segs, sep)
	visible := lipgloss.Width(joined)
	if visible < width {
		joined += statusLineStyle.Render(strings.Repeat(" ", width-visible))
	}
	return joined
}

// bigMetric renders a two-line metric tile: a bold value with an optional
// dim unit, with a dim caption beneath. Width is advisory; the returned
// string is not padded.
func bigMetric(value, unit, caption string) string {
	v := bigMetricValueStyle.Render(value)
	if unit != "" {
		v += bigMetricUnitStyle.Render(unit)
	}
	return lipgloss.JoinVertical(lipgloss.Left, v, dimStyle.Render(caption))
}

// dotIndicator renders a colored "●" for the given health state.
// Known states: "ok", "warn", "err"; anything else is dim.
func dotIndicator(state string) string {
	switch state {
	case "ok":
		return okStyle.Render("●")
	case "warn":
		return warnStyle.Render("●")
	case "err":
		return errStyle.Render("●")
	default:
		return dimStyle.Render("●")
	}
}

// boolState returns "ok" for true, "err" for false — a thin wrapper so
// renderers can write dotIndicator(boolState(x)).
func boolState(ok bool) string {
	if ok {
		return "ok"
	}
	return "err"
}

// padRight right-pads s with spaces to n visible cells. If s is already
// at least n cells wide, it is returned unchanged.
func padRight(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

// truncateWidth hard-truncates s at n visible cells, appending "…" when
// truncation happens. Returns "" for n <= 0.
func truncateWidth(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	runes := []rune(s)
	if len(runes) > n-1 {
		runes = runes[:n-1]
	}
	return string(runes) + "…"
}

// formatCount returns a compact human-friendly count, e.g. "4", "12", "1.2k".
func formatCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000.0)
}
