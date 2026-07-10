package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const footerHintToggleKey = "?"

func isAltFooterToggleKey(msg tea.KeyMsg) bool {
	return msg.String() == footerHintToggleKey || (msg.Alt && msg.Type == tea.KeyEscape)
}

func renderShortcutPairs(renderKey func(string) string, renderText func(string) string, gap string, pairs [][2]string) string {
	segments := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		segments = append(segments, renderKey(pair[0])+renderText(" "+pair[1]))
	}
	return strings.Join(segments, gap)
}

// renderWrappedShortcutFooter renders the hint pairs on a single line when they
// fit, otherwise greedily wraps them onto up to maxLines lines (extra pairs are
// dropped). Returns the rendered text and its line count so callers can reserve
// body height. Put the most important pairs first — wrapping drops from the tail.
func renderWrappedShortcutFooter(width int, render func([][2]string) string, maxLines int, pairs [][2]string) (string, int) {
	if width < 1 {
		width = 1
	}
	if maxLines < 1 {
		maxLines = 1
	}
	if lipgloss.Width(render(pairs)) <= width {
		return render(pairs), 1
	}
	var lines []string
	var cur [][2]string
	for _, p := range pairs {
		trial := make([][2]string, len(cur)+1)
		copy(trial, cur)
		trial[len(cur)] = p
		if len(cur) > 0 && lipgloss.Width(render(trial)) > width {
			lines = append(lines, render(cur))
			cur = [][2]string{p}
		} else {
			cur = trial
		}
	}
	if len(cur) > 0 {
		lines = append(lines, render(cur))
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n"), len(lines)
}

func pickRenderedShortcutFooter(width int, render func([][2]string) string, candidates ...[][2]string) string {
	if len(candidates) == 0 {
		return ""
	}
	footer := render(candidates[len(candidates)-1])
	for _, candidate := range candidates {
		rendered := render(candidate)
		if lipgloss.Width(rendered) <= maxInt(1, width) {
			return rendered
		}
		footer = rendered
	}
	return footer
}
