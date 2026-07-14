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
)

// Remote-bell mirroring: when the user is ssh'd into another machine from a tmux
// window, that machine's own 🔔 (a completed/asking task in its tracker) is
// invisible locally — its notifications fire on the remote host's screen. This
// poller bridges that gap. cmd/agent stamps each ssh window's destination host
// into @agent_ssh_host; here the daemon reads each remote machine's tracker cache
// over a reused ssh connection and, when ANY remote window has an unread 🔔,
// lights the local ssh window's tab 🔔 (via @agent_remote_bell, honored by
// window_task_icon.sh and Window Nav) and raises one local system notification.
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
	windowID, sessionID, paneID, host, remoteBellOpt string
}

func (s *server) reconcileRemoteBells() {
	out, err := tmuxOutput("list-windows", "-a", "-F",
		"#{window_id}|#{session_id}|#{pane_id}|#{@agent_ssh_host}|#{@agent_remote_bell}")
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
		wins = append(wins, sshWindow{f[0], f[1], f[2], host, strings.TrimSpace(f[4])})
		hosts[host] = true
	}

	// Query each unique host once per tick (multiple ssh windows to one host share
	// the aggregate result).
	bellByHost := map[string]bool{}
	reachableByHost := map[string]bool{}
	for host := range hosts {
		bellByHost[host], reachableByHost[host] = remoteHostHasBell(host)
	}

	live := map[string]bool{}
	for _, w := range wins {
		live[w.windowID] = true
		// Unreachable this tick (host down, ssh still connecting): leave existing
		// state untouched so transient failures don't flap the bell.
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
		}
		s.updateRemoteNotification(w.windowID, w.sessionID, w.paneID, w.host, bell)
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

// remoteHostHasBell reads host's tracker cache over a reused ssh connection and
// reports whether any remote window has an unread 🔔 (completed or asking, not
// acknowledged). The second return is false only when the host is unreachable, so
// callers can distinguish "no bell" from "couldn't check".
func remoteHostHasBell(host string) (bell bool, reachable bool) {
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
		return false, false
	}
	var env ipc.Envelope
	if json.Unmarshal(out, &env) != nil {
		// Reachable but no/garbage cache (remote daemon not running yet) → no bell.
		return false, true
	}
	for _, t := range env.Tasks {
		// Attention: the remote window name carries "[?]" or "[E]" while the agent
		// needs input or has stopped on an error. This is ground truth (set by the remote's own window
		// naming) and independent of the remote's acknowledge bookkeeping — what
		// matters here is that an agent over there needs input while we're local.
		windowName := strings.TrimSpace(t.Window)
		if strings.HasPrefix(windowName, "[?]") || strings.HasPrefix(windowName, "[E]") {
			return true, true
		}
		if t.Acknowledged {
			continue
		}
		// Completed-unread (🔔) and the asking flag still count, when present.
		if t.Status == statusCompleted || t.Asking {
			return true, true
		}
	}
	return false, true
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
