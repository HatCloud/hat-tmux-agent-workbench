package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/david/agent-tracker/internal/ipc"
	"github.com/david/agent-tracker/internal/paths"
	"github.com/david/agent-tracker/internal/statustag"
)

// Remote-bell mirroring: when the user is ssh'd into another machine from a tmux
// window, that machine's own 🔔 (a completed/asking task in its tracker) is
// invisible locally — its notifications fire on the remote host's screen. This
// poller bridges that gap. cmd/agent stamps each ssh window's destination host
// into @agent_ssh_host; here the daemon reads each remote machine's tracker cache
// over a reused ssh connection and, when ANY remote window has an unread 🔔,
// lights the local ssh window's tab 🔔 (via @agent_remote_bell, folded into
// @agent_icon by reconcileWindowIcons and read by Window Nav) and raises one
// local system notification.
// State and bell clear in lock-step the moment the remote bell goes away.

const remoteBellPollInterval = 3 * time.Second

func (s *server) pollRemoteBells() {
	ticker := time.NewTicker(remoteBellPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.reconcileRemoteBells()
	}
}

type sshWindow struct {
	windowID, sessionID, paneID, host, remoteBellOpt, remoteStatusOpt string
}

// remoteStatusRank orders aggregate remote states; the highest across a host's
// windows is mirrored onto the local ssh window's name prefix. Attention states
// outrank plain busy so a window needing input shows [?] rather than [B].
func remoteStatusRank(status string) int {
	switch status {
	case "error":
		return 4
	case "asking", "waiting", "paused":
		return 3
	case "limited":
		return 2
	case "busy":
		return 1
	default: // idle / shell / ""
		return 0
	}
}

// remoteStatusCanon collapses a per-task status to the canonical form used for
// the ssh window prefix (statusTag understands busy/asking/limited/error).
func remoteStatusCanon(status string) string {
	switch status {
	case "waiting", "paused":
		return "asking"
	case "shell":
		return "idle"
	default:
		return status
	}
}

// remoteWindowStatusPrefix reads the [B]/[I]/[?]/[L]/[E] prefix a remote window
// name carries (written by that machine's own sync-names) and maps it to a
// status. This mirrors exactly what the user would see on the remote window.
func remoteWindowStatusPrefix(name string) string {
	// The prefix vocabulary is shared with cmd/agent via internal/statustag —
	// this parses back exactly what the remote's agent rendered.
	return statustag.StatusOf(name)
}

// remoteTaskStatus resolves one remote task's live status, preferring the window
// name's own prefix (ground truth for what the remote shows) and falling back to
// the structured task fields when the remote hides status prefixes.
func remoteTaskStatus(t ipc.Task) string {
	if st := remoteWindowStatusPrefix(strings.TrimSpace(t.Window)); st != "" {
		return st
	}
	switch {
	case t.Attention == "error":
		return "error"
	case t.Asking || t.Attention == "asking" || t.Attention == "waiting" || t.Attention == "paused":
		return "asking"
	case t.Attention == "limited":
		return "limited"
	case t.Status == "in_progress":
		return "busy"
	default:
		return ""
	}
}

func (s *server) reconcileRemoteBells() {
	out, err := tmuxOutput("list-windows", "-a", "-F",
		"#{window_id}|#{session_id}|#{pane_id}|#{@agent_ssh_host}|#{@agent_remote_bell}|#{@agent_remote_status}")
	if err != nil {
		return
	}
	var wins []sshWindow
	hosts := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "|")
		if len(f) < 5 {
			continue
		}
		host := strings.TrimSpace(f[3])
		// Skip windows without an ssh host, and never feed a destination ssh would
		// read as an option flag.
		if host == "" || strings.HasPrefix(host, "-") {
			continue
		}
		statusOpt := ""
		if len(f) >= 6 {
			statusOpt = strings.TrimSpace(f[5])
		}
		wins = append(wins, sshWindow{f[0], f[1], f[2], host, strings.TrimSpace(f[4]), statusOpt})
		hosts[host] = true
	}

	// Query each unique host once per tick (multiple ssh windows to one host share
	// the aggregate result).
	bellByHost := map[string]bool{}
	statusByHost := map[string]string{}
	reachableByHost := map[string]bool{}
	for host := range hosts {
		bellByHost[host], statusByHost[host], reachableByHost[host] = remoteHostState(host)
	}

	live := map[string]bool{}
	statusChanged := false
	for _, w := range wins {
		live[w.windowID] = true
		// Unreachable this tick (host down, ssh still connecting): leave existing
		// state untouched so transient failures don't flap the bell/status.
		if !reachableByHost[w.host] {
			continue
		}
		bell := bellByHost[w.host]
		want := ""
		if bell {
			want = "1"
		}
		if w.remoteBellOpt != want {
			if want == "" {
				_ = runTmux("set", "-w", "-u", "-t", w.windowID, "@agent_remote_bell")
			} else {
				_ = runTmux("set", "-w", "-t", w.windowID, "@agent_remote_bell", "1")
			}
			// @agent_icon derives from the remote bell — reconcile it promptly.
			s.broadcastStateAsync()
		}
		// Mirror the remote's aggregate live status onto the ssh window's name
		// prefix ([B]/[?]/[E]/…) via @agent_remote_status; sync-names reads it when
		// naming the 🌐 window. A change triggers a coalesced sync-names so the
		// prefix updates promptly instead of at the next periodic tick.
		wantStatus := statusByHost[w.host]
		if w.remoteStatusOpt != wantStatus {
			if wantStatus == "" {
				_ = runTmux("set", "-w", "-u", "-t", w.windowID, "@agent_remote_status")
			} else {
				_ = runTmux("set", "-w", "-t", w.windowID, "@agent_remote_status", wantStatus)
			}
			statusChanged = true
		}
		s.updateRemoteNotification(w.windowID, w.sessionID, w.paneID, w.host, bell)
	}
	if statusChanged {
		s.detectCoalescer.trigger()
	}

	// A window that closed while its remote notification was up never gets a
	// falling-edge tick — sweep those orphans here.
	s.mu.Lock()
	var orphans []string
	for wid := range s.remoteBellNotified {
		if !live[wid] {
			orphans = append(orphans, wid)
		}
	}
	s.mu.Unlock()
	for _, wid := range orphans {
		s.clearRemoteNotification(wid)
	}
}

// remoteHostState reads host's tracker cache once over a reused ssh connection
// and reports (1) whether any remote window has an unread 🔔 (completed or
// asking, not acknowledged) and (2) the highest-priority live status across its
// windows (error>asking>limited>busy), to mirror onto the local ssh window's
// name prefix. reachable is false only when the host is unreachable, so callers
// can distinguish "no bell / idle" from "couldn't check".
func remoteHostState(host string) (bell bool, status string, reachable bool) {
	// A literal, short ControlPath: ssh's %C token expands to a 40-char hash that
	// pushes the full socket path past macOS's ~104-char sun_path limit
	// ("unix_listener: path too long"), which fails the whole connection. Hash the
	// host ourselves to a fixed 8 chars so the path stays short for any hostname.
	controlPath := filepath.Join(paths.StateDir(), "cm-"+shortHostHash(host))
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + controlPath,
		"-o", "ControlPersist=120s",
		host,
		"cat ~/.hat-config/state/agent-tracker/tmux-tracker-cache.json 2>/dev/null",
	}
	out, err := exec.Command("ssh", args...).Output()
	if err != nil {
		return false, "", false
	}
	var env ipc.Envelope
	if json.Unmarshal(out, &env) != nil {
		// Reachable but no/garbage cache (remote daemon not running yet) → idle.
		return false, "", true
	}
	bell, status = aggregateRemoteState(env.Tasks)
	return bell, status, true
}

// aggregateRemoteState folds a remote host's tasks into (bell, status): the 🔔
// flag if any window is completed-unread / asking / [?] / [E], and the
// highest-priority live status for the ssh window name prefix. Pure, so the fold
// logic is unit-tested without an ssh round-trip.
func aggregateRemoteState(tasks []ipc.Task) (bell bool, status string) {
	bestRank := 0
	for _, t := range tasks {
		if st := remoteStatusCanon(remoteTaskStatus(t)); remoteStatusRank(st) > bestRank {
			bestRank = remoteStatusRank(st)
			status = st
		}
		windowName := strings.TrimSpace(t.Window)
		if strings.HasPrefix(windowName, "[?]") || strings.HasPrefix(windowName, "[E]") {
			bell = true
			continue
		}
		if t.Acknowledged {
			continue
		}
		if t.Status == statusCompleted || t.Asking {
			bell = true
		}
	}
	return bell, status
}

// updateRemoteNotification raises or removes the single local system notification
// mirroring a remote machine's 🔔 for one ssh window, on the same rising/falling
// edges as the bell. Suppressed while the user is actively watching the ssh window
// (they already see the remote status inside the pane).
func (s *server) updateRemoteNotification(windowID, sessionID, paneID, host string, bell bool) {
	s.mu.Lock()
	already := s.remoteBellNotified[windowID]
	enabled := s.notificationsEnabled
	s.mu.Unlock()

	if !bell {
		if already {
			s.clearRemoteNotification(windowID)
		}
		return
	}
	if already || !enabled || windowIsBeingWatched(windowID) {
		return
	}
	title := "🌐 " + host
	action := notificationActionForTarget(tmuxTarget{SessionID: sessionID, WindowID: windowID, PaneID: paneID})
	if err := sendSystemNotification(title, "🔔 远程有任务需要处理", action, "agent-tracker-"+windowID); err != nil {
		log.Printf("remote notify error: %v", err)
		return
	}
	s.mu.Lock()
	s.remoteBellNotified[windowID] = true
	s.mu.Unlock()
}

func shortHostHash(host string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(host))
	return fmt.Sprintf("%08x", h.Sum32())
}

func (s *server) clearRemoteNotification(windowID string) {
	s.mu.Lock()
	had := s.remoteBellNotified[windowID]
	delete(s.remoteBellNotified, windowID)
	s.mu.Unlock()
	if had {
		removeNotificationGroup("agent-tracker-" + windowID)
	}
}
