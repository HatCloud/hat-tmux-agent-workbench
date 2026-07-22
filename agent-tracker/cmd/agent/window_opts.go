package main

import "strings"

// Batched window-option access for the sync-names pass. agentWindowName and its
// reconcile helpers read ~25 window options per window per tick, and each
// tmuxWindowOption call used to be its own `tmux show-options` fork — the
// dominant per-tick cost (fork count ≈ windows × 25 / poll interval). During a
// pass the memo serves all listed options from ONE `display-message` fork per
// window (values joined by 0x1F, which tmux passes through verbatim — verified
// against quotes/emoji/backslashes). Writes go through setWindowOption /
// unsetWindowOption, which keep the memo coherent: the pass has real
// read-after-write dependencies (e.g. the @agent_error_* stamps agentWindowName
// writes and reconcileClaudeErrorRetry reads within the same pass).
//
// The memo is package state rather than a threaded parameter because the agent
// CLI is a short-lived single-goroutine process; only runTmuxSyncNames turns it
// on. An option missing from memoedWindowOptions is still correct — it just
// falls back to the per-option slow path.

var memoedWindowOptions = []string{
	"@agent_client", "@agent_provider", "@agent_model", "@agent_dir",
	"@agent_title", "@agent_notify_name", "@agent_window_name_auto",
	optManualWindowName, optResolvedDisplayTitle,
	"@agent_orientation", "@agent_orientation_mode",
	"@agent_ssh_host", "@agent_ssh_border_off", "@agent_remote_status",
	"@agent_limit_reset_at", "@agent_last_busy_at",
	optGeneratedName, optGeneratedNameSession, optAutoNameState,
	optAutoNameAttemptAt, optAutoNameNative,
	optErrorAt, optErrorType, optRetryCount, optRetryNextAt,
}

var windowOptMemo map[string]map[string]string

func beginWindowOptMemo() { windowOptMemo = make(map[string]map[string]string) }
func endWindowOptMemo()   { windowOptMemo = nil }

// memoFetchWindowOptions reads every memoed option of one window in a single
// tmux call. Returns nil on failure so the caller falls back to the slow path
// (an empty map would wrongly claim every option is unset).
func memoFetchWindowOptions(windowID string) map[string]string {
	parts := make([]string, len(memoedWindowOptions))
	for i, opt := range memoedWindowOptions {
		parts[i] = "#{" + opt + "}"
	}
	out, err := runTmuxOutput("display-message", "-p", "-t", windowID, strings.Join(parts, "\x1f"))
	if err != nil {
		return nil
	}
	values := strings.Split(strings.TrimSuffix(out, "\n"), "\x1f")
	if len(values) != len(memoedWindowOptions) {
		return nil
	}
	opts := make(map[string]string, len(values))
	for i, opt := range memoedWindowOptions {
		opts[opt] = values[i]
	}
	return opts
}

// memoWindowOption resolves opt from the memo. The second result reports
// whether the memo could answer (false → caller uses the slow path).
func memoWindowOption(windowID, opt string) (string, bool) {
	if windowOptMemo == nil {
		return "", false
	}
	opts, ok := windowOptMemo[windowID]
	if !ok {
		opts = memoFetchWindowOptions(windowID)
		if opts == nil {
			return "", false
		}
		windowOptMemo[windowID] = opts
	}
	v, known := opts[opt]
	return v, known
}

// setWindowOption writes a window option and keeps the pass memo coherent.
func setWindowOption(windowID, opt, value string) {
	_ = runTmux("set", "-w", "-t", windowID, opt, value)
	if m, ok := windowOptMemo[windowID]; ok {
		m[opt] = value
	}
}

// unsetWindowOption removes a window option (memo records it as empty, which is
// how an unset option reads back through tmux formats). Skips the tmux call when
// the option already reads empty — with the memo active that check is free.
func unsetWindowOption(windowID, opt string) {
	if tmuxWindowOption(windowID, opt) == "" {
		return
	}
	_ = runTmux("set", "-w", "-u", "-t", windowID, opt)
	if m, ok := windowOptMemo[windowID]; ok {
		m[opt] = ""
	}
}
