package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 布局朝向 reconcile：auto-resize 开关、滞回带、reflow 防抖、
// pane role 常量。从 claude_session.go 拆出。

// Pane roles stamped into @agent_pane_role by tmux/scripts/build_agent_layout.sh.
// The shell side writes these exact literals; there is no compile-time bridge
// across the process boundary, so a change here must be mirrored in that script
// (and vice versa). reconcileWindowOrientation logs role-set drift so a typo on
// either side surfaces instead of silently disabling reflow.
const (
	paneRoleAI  = "ai"
	paneRoleGit = "git"
	paneRoleRun = "run"
)

// paneRoleWarned throttles pane-role drift logs to once per distinct signature
// per window per process (the sync pass is a short-lived CLI, so this resets
// naturally each run; it exists to keep one pass from logging N times).
var paneRoleWarned = map[string]string{}

func warnPaneRoleDrift(windowID, rolesOut string) {
	sig := strings.Join(strings.Fields(rolesOut), ",")
	if paneRoleWarned[windowID] == sig {
		return
	}
	paneRoleWarned[windowID] = sig
	fmt.Fprintf(os.Stderr, "agent: window %s pane roles %q don't match ai/git[/run]; skipping reflow\n", windowID, sig)
}

// reconcileWindowOrientation follows the window's dimensions only when the global
// auto-resize feature is enabled. It never corrects pane proportions: a manual drag
// must remain untouched while the orientation stays the same.
// Called from the configured name-sync poll and on focus/resize (agent tmux reflow-focus),
// so switching the terminal between portrait/landscape reflows every window the
// moment it's selected.
func reconcileWindowOrientation(windowID string) {
	if !layoutAutoResizeSetting(loadAppConfig()) {
		return
	}
	// Skip while the window is zoomed or any pane is in a tmux mode (copy-mode,
	// choose-tree, etc.). list-panes reports the zoomed pane at full window size,
	// so the layout looks "wrong" and triggers a reflow; doing break-pane/join-pane
	// on a pane that's currently hosting an interactive picker (e.g. `prefix W`'s
	// `choose-tree -Zw`) destabilizes the tmux state and can kill the server.
	if z, err := runTmuxOutput("display-message", "-p", "-t", windowID, "#{window_zoomed_flag}"); err == nil && strings.TrimSpace(z) == "1" {
		return
	}
	if m, err := runTmuxOutput("list-panes", "-t", windowID, "-F", "#{pane_in_mode}"); err == nil {
		for _, f := range strings.Fields(m) {
			if f == "1" {
				return
			}
		}
	}
	// Only ever touch a standard 2-pane ai/git or 3-pane ai/git/run layout.
	out, err := runTmuxOutput("list-panes", "-t", windowID, "-F", "#{@agent_pane_role}")
	if err != nil {
		return
	}
	roles := map[string]bool{}
	n := 0
	for _, r := range strings.Fields(out) {
		roles[r] = true
		n++
	}
	validTwoPane := n == 2 && roles[paneRoleAI] && roles[paneRoleGit]
	validThreePane := n == 3 && roles[paneRoleAI] && roles[paneRoleGit] && roles[paneRoleRun]
	if !validTwoPane && !validThreePane {
		// Roles present but not an expected pair/trio = drift between this
		// constant set and build_agent_layout.sh (or a half-rebuilt layout). Log it
		// once per distinct signature so the skip is traceable instead of silent.
		if n > 0 {
			warnPaneRoleDrift(windowID, out)
		}
		return
	}
	current := tmuxWindowOption(windowID, "@agent_orientation")
	dim, err := runTmuxOutput("display-message", "-p", "-t", windowID, "#{window_width} #{window_height}")
	if err != nil {
		return
	}
	var w, h int
	if _, err := fmt.Sscanf(strings.TrimSpace(dim), "%d %d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return
	}
	desired := desiredOrientation(w, h, current)
	if !shouldReflowOrientation(true, current, desired) {
		return
	}
	script := filepath.Join(homeDir(), ".hat-config", "tmux", "scripts", "reflow_agent_layout.sh")
	_, _ = runCommandOutput(10*time.Second, script, windowID, desired)
}

func shouldReflowOrientation(autoResize bool, current, desired string) bool {
	return autoResize && desired != "" && desired != current
}

// reflowDebounceDelay is the trailing-debounce wait. Dragging a terminal to
// fullscreen emits a burst of window-resized events, each spawning its own
// `agent tmux reflow-focus` process; without debouncing they reflow 4-5 times in
// sequence before the size settles. The window only needs the LAST one.
const reflowDebounceDelay = 450 * time.Millisecond

// reflowDebouncePath is the per-window file holding the latest reflow request's
// token (a UnixNano timestamp). Keyed by the window id's digits.
func reflowDebouncePath(windowID string) string {
	id := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, windowID)
	return filepath.Join(os.TempDir(), "agent_reflow_debounce_"+id)
}

// reflowDebounceClaim records this caller as the latest reflow request, waits the
// debounce delay, then reports whether it is still the latest (no newer request
// arrived) — i.e. the trailing-debounce winner that should perform the reflow.
// Returns true (proceed) if the debounce file is unusable, to never deadlock.
func reflowDebounceClaim(windowID string) bool {
	path := reflowDebouncePath(windowID)
	token := strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.WriteFile(path, []byte(token), 0o644); err != nil {
		return true
	}
	time.Sleep(reflowDebounceDelay)
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(data)) == token
}

// reflowDebouncePending reports whether a reflow-focus debounce is currently in
// flight (a request was registered within the last delay). The 1s poll checks
// this to yield to the debounced winner instead of reflowing a transient layout.
func reflowDebouncePending(windowID string) bool {
	data, err := os.ReadFile(reflowDebouncePath(windowID))
	if err != nil {
		return false
	}
	token, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(0, token)) < reflowDebounceDelay
}

// desiredOrientation maps window dimensions to landscape/portrait with a hysteresis
// dead-band, so a window hovering near square doesn't flip-flop every poll. Terminal
// cells are ~2x taller than wide, so a visually square window has width == 2*height.
// tmux/scripts/orientation_for_window.sh shares that physical assumption with a
// deliberately DIFFERENT threshold (hard 2.0 for one-shot window creation vs this
// 2.2/1.8 runtime dead-band) — change the assumption in both places, but do NOT
// unify the numbers (see docs/audit 2026-07-15 I-7).
func desiredOrientation(w, h int, current string) string {
	switch {
	case w*10 >= h*22: // clearly wide
		return "landscape"
	case w*10 <= h*18: // clearly tall
		return "portrait"
	default:
		return current // dead-band: keep current orientation
	}
}
