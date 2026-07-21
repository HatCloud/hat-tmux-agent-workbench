package main

import (
	"github.com/david/agent-tracker/internal/agentclient"
	"github.com/david/agent-tracker/internal/agentclient/claude"
)

// busyShellPatternsSetting resolves the configured busy-shell allowlist with
// three-state semantics (AC-2):
//   - field absent/nil  → built-in default (claude.DefaultBusyShellPatterns)
//   - non-empty slice    → that slice verbatim, fully REPLACING the default
//   - explicit empty []  → empty (feature disabled; no pattern can match)
//
// Returns a fresh copy so callers can't mutate the shared default var.
func busyShellPatternsSetting(cfg appConfig) []string {
	src := claude.DefaultBusyShellPatterns
	if cfg.BusyShellPatterns != nil {
		src = *cfg.BusyShellPatterns
	}
	// make (not append-to-nil) so an explicit empty [] stays a non-nil empty
	// slice — the adapter distinguishes "disabled" (key present, empty) from
	// "absent" (fall back to default).
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// injectBusyShellPatterns publishes the resolved allowlist into the per-sync
// Index SideCar so the claude adapter's Detect can read it without importing
// cmd/agent config. Absent key would make the adapter fall back to its default;
// this makes the configured value authoritative on the sync path.
func injectBusyShellPatterns(idx *agentclient.Index, cfg appConfig) {
	if idx == nil {
		return
	}
	if idx.SideCar == nil {
		idx.SideCar = map[string]any{}
	}
	idx.SideCar[claude.BusyShellSideCarKey] = busyShellPatternsSetting(cfg)
}
