package claude

import (
	"strings"

	"github.com/david/agent-tracker/internal/agentclient"
)

// BusyShellSideCarKey is the Index.SideCar slot ([]string) through which the
// orchestrator (cmd/agent sync path) passes the configured busy-shell patterns
// to this adapter. Absent key → the adapter falls back to DefaultBusyShellPatterns.
const BusyShellSideCarKey = "claude.busy_shell_patterns"

// DefaultBusyShellPatterns are the built-in command substrings whose presence in
// a pane's process subtree marks an otherwise-idle (shell) session as busy.
// agent-hl headless workers are always launched through these dispatchers, and
// their provider children (claude -p / codex exec / grok) descend from them, so
// matching the launcher name alone is an unambiguous signal. This is the single
// source of truth for the default list (referenced by cmd/agent's config reader).
var DefaultBusyShellPatterns = []string{"hl-run", "hl-dispatch"}

// resolveBusyShell upgrades a shell/idle status to busy when any command in the
// pane's process subtree matches one of the patterns (case-insensitive
// substring). Only shell/idle are eligible — busy/asking/limited/error/anything
// else is returned unchanged, preserving the error > limited > bg-busy > idle
// precedence applied by the caller. Empty/whitespace-only pattern elements are
// ignored so they can never substring-match every command and flip all shells.
func resolveBusyShell(status string, subtreeCommands, patterns []string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case agentclient.StatusShell, agentclient.StatusIdle:
		// eligible
	default:
		return status
	}
	for _, cmd := range subtreeCommands {
		lc := strings.ToLower(cmd)
		for _, p := range patterns {
			p = strings.ToLower(strings.TrimSpace(p))
			if p == "" {
				continue
			}
			if strings.Contains(lc, p) {
				return agentclient.StatusBusy
			}
		}
	}
	return status
}

// busyShellPatternsFromIndex returns the patterns the orchestrator injected into
// the Index SideCar, or DefaultBusyShellPatterns when the slot is absent. A
// present-but-empty slice means the feature was explicitly disabled by config
// and is returned as-is (no pattern can match).
func busyShellPatternsFromIndex(idx *agentclient.Index) []string {
	if idx != nil && idx.SideCar != nil {
		if v, ok := idx.SideCar[BusyShellSideCarKey]; ok {
			if p, ok := v.([]string); ok {
				return p
			}
		}
	}
	return DefaultBusyShellPatterns
}

// subtreeCommands collects the non-empty command lines for a pane subtree.
func subtreeCommands(idx *agentclient.Index, subtree []int) []string {
	if idx == nil {
		return nil
	}
	out := make([]string, 0, len(subtree))
	for _, pid := range subtree {
		if c := idx.CommandFor(pid); c != "" {
			out = append(out, c)
		}
	}
	return out
}
