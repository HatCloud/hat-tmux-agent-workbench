package main

import (
	"fmt"
	"os/exec"
	"strings"
	"unicode"
)

func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{""}
	}
	if text == "" {
		return []string{""}
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	current := words[0]
	for _, word := range words[1:] {
		candidate := current + " " + word
		if len([]rune(candidate)) <= width {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = word
	}
	lines = append(lines, current)
	return lines
}

func truncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width == 1 {
		return string(runes[:1])
	}
	return string(runes[:width-1]) + "…"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func previousWordBoundary(runes []rune, cursor int) int {
	i := cursor
	for i > 0 && unicode.IsSpace(runes[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(runes[i-1]) {
		i--
	}
	return i
}

// readClipboard reads text from the system clipboard via pbpaste (macOS).
func readClipboard() string {
	out, err := exec.Command("pbpaste").Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

func writeClipboard(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("nothing to copy")
	}
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(value)
	if output, err := cmd.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return err
		}
		return fmt.Errorf("clipboard copy failed: %s", message)
	}
	return nil
}

// insertRunes inserts runes into text at cursor, advancing cursor past them.
func insertRunes(runes []rune, text *[]rune, cursor *int) {
	newText := make([]rune, 0, len(*text)+len(runes))
	newText = append(newText, (*text)[:*cursor]...)
	newText = append(newText, runes...)
	newText = append(newText, (*text)[*cursor:]...)
	*text = newText
	*cursor += len(runes)
}
