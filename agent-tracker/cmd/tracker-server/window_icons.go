package main

import "strings"

// Window tab icons were computed by window_task_icon.sh — a #() inside
// window-status-format that forked tmux+cat+jq per window on every status
// redraw, re-parsing the whole tracker cache each time. Every live input of
// that decision (local task 🔔, remote @agent_remote_bell) is daemon state, so
// the daemon now stamps the ready-made icon into the window option
// @agent_icon whenever state changes; window-status-format expands the option
// natively with zero subprocesses per redraw, and the icon updates the moment
// the daemon knows about a change instead of at the next 3s redraw.

// desiredWindowIcon folds the bell inputs into the icon text shown before the
// window name. Kept pure for testability.
func desiredWindowIcon(localBell, remoteBell bool) string {
	if localBell || remoteBell {
		return "🔔 "
	}
	return ""
}

// reconcileWindowIcons diffs every window's @agent_icon against the desired
// value and rewrites only the changed ones. One batch list-windows read per
// (coalesced) state change; writes are rare edges, so steady state costs a
// single fork per broadcast. Reading the current value from tmux (instead of a
// lastIcons map) also self-heals stale icons left by a previous daemon.
func (s *server) reconcileWindowIcons() {
	out, err := tmuxOutput("list-windows", "-a", "-F", "#{window_id}|#{@agent_icon}|#{@agent_remote_bell}")
	if err != nil {
		return
	}
	changed := false
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "|")
		if len(f) < 3 {
			continue
		}
		wid, current, remoteBell := f[0], f[1], f[2]
		s.mu.Lock()
		localBell := s.windowHasBellLocked(wid)
		s.mu.Unlock()
		want := desiredWindowIcon(localBell, remoteBell == "1")
		if current == want {
			continue
		}
		changed = true
		if want == "" {
			_ = runTmux("set", "-w", "-u", "-t", wid, "@agent_icon")
		} else {
			_ = runTmux("set", "-w", "-t", wid, "@agent_icon", want)
		}
	}
	if changed {
		s.statusRefreshAsync()
	}
}
