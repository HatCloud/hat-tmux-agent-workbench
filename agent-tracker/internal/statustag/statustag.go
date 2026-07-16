// Package statustag is the single source of truth for the window-name status
// prefixes ([B]/[I]/[?]/[L]/[E]). cmd/agent renders them into window names;
// cmd/tracker-server parses them back off remote machines' window names when
// mirroring ssh-window state. The same business vocabulary used to be
// hardcoded in four places across the two binaries — adding a state or
// changing the prefix format now means editing ONE table.
package statustag

import "strings"

type entry struct {
	Tag    string
	Status string
}

// table pairs each display tag with its canonical status. Parse helpers check
// entries in order; render goes through ForStatus (which also folds aliases).
var table = []entry{
	{"[E] ", "error"},
	{"[?] ", "asking"},
	{"[L] ", "limited"},
	{"[B] ", "busy"},
	{"[I] ", "idle"},
}

// ForStatus returns the display tag for a live status, folding aliases:
// "shell" (turn ended, background work still running) renders as idle — the
// agent accepts input in that state, and a long-lived background job (dev
// server, watch) would otherwise pin [B] forever with no way to tell it apart
// from real work (the session file carries no per-task signal to discriminate);
// "waiting"/"paused" render as asking. Empty for unknown states.
func ForStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "busy":
		return "[B] "
	case "idle", "shell":
		return "[I] "
	case "asking", "waiting", "paused":
		return "[?] "
	case "limited":
		return "[L] "
	case "error":
		return "[E] "
	default:
		return ""
	}
}

// Prefix returns name's leading status tag (including trailing space), or "".
func Prefix(name string) string {
	for _, e := range table {
		if strings.HasPrefix(name, e.Tag) {
			return e.Tag
		}
	}
	return ""
}

// Strip removes name's leading status tag if present.
func Strip(name string) string {
	return strings.TrimPrefix(name, Prefix(name))
}

// StatusOf returns the canonical status for name's leading tag ("" if none).
func StatusOf(name string) string {
	for _, e := range table {
		if strings.HasPrefix(name, e.Tag) {
			return e.Status
		}
	}
	return ""
}
