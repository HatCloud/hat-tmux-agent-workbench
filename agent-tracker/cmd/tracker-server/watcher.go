package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Event-driven detection: Claude Code writes one <pid>.json per live session
// under ~/.claude/sessions and rewrites it on every status change. Watching that
// directory lets the daemon pick up a busy↔idle / asking / error transition in
// ~ms instead of waiting for the fixed status-bar poll (3s+). The periodic poll
// stays as a safety net; the watcher only adds immediacy and costs nothing while
// idle (no writes → no events → no work).

func claudeSessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions")
}

// agentBinaryPath locates the sibling `agent` binary (next to this daemon), with
// a fallback to the deployed path, so the daemon can drive a sync-names pass.
func agentBinaryPath() string {
	if self, err := os.Executable(); err == nil {
		if cand := filepath.Join(filepath.Dir(self), "agent"); isExecutableFile(cand) {
			return cand
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		cand := filepath.Join(home, ".hat-config", "agent-tracker", "bin", "agent")
		if isExecutableFile(cand) {
			return cand
		}
	}
	return ""
}

func isExecutableFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// triggerSyncNames runs one non-throttled sync-names pass (event-driven
// detection). sync-names' own cross-process flock single-flights it against the
// periodic status-bar run, so overlapping triggers can't stack. Invoked by
// detectCoalescer, so a burst of session-file writes collapses into one pass.
func (s *server) triggerSyncNames() {
	bin := agentBinaryPath()
	if bin == "" {
		return
	}
	// Bound the run so a wedged sync-names can't stall the coalescer loop (and
	// thus all further event-driven detection). sync-names has its own 20s cap.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, bin, "tmux", "sync-names", "--wait").Run(); err != nil {
		log.Printf("event sync-names: %v", err)
	}
}

// watchSessionFilesLoop supervises watchSessionFiles: if the watcher fails to
// start or its event stream closes, it retries after a pause so a transient
// failure doesn't permanently drop event-driven detection (the periodic poll
// covers the gap meanwhile).
func (s *server) watchSessionFilesLoop() {
	for {
		s.watchSessionFiles()
		time.Sleep(30 * time.Second)
	}
}

// sessionStatusOf reads just the `status` field of a session file. Claude
// rewrites <pid>.json ~8×/s while a turn runs (heartbeat: only statusUpdatedAt
// moves), so triggering on every write would run sync-names continuously during
// active work. We instead trigger only when the status VALUE changes, which is
// the transition the UI cares about (busy↔idle↔asking). A partial/mid-write read
// fails to parse and is skipped — the next event (or the periodic poll) catches
// up. ok=false means "no usable status", distinct from a real empty status.
func sessionStatusOf(path string) (status string, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var meta struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return "", false
	}
	return meta.Status, true
}

// watchSessionFiles watches the Claude sessions dir and, on a real status
// transition, coalesces a sync-names run. Best-effort: any setup failure logs and
// returns, leaving the periodic poll as the fallback. Blocks — run in a goroutine.
func (s *server) watchSessionFiles() {
	dir := claudeSessionsDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("session watch: mkdir %s: %v", dir, err)
		return
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("session watch: %v", err)
		return
	}
	defer w.Close()
	if err := w.Add(dir); err != nil {
		log.Printf("session watch add %s: %v", dir, err)
		return
	}
	// Last-seen status per session file, so heartbeat rewrites that don't change
	// status produce no work. Owned solely by this goroutine — no lock needed.
	lastStatus := map[string]string{}
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(ev.Name, ".json") {
				continue
			}
			switch {
			case ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
				// A session ended (window likely closed) — reflect it once.
				if _, tracked := lastStatus[ev.Name]; tracked {
					delete(lastStatus, ev.Name)
					s.detectCoalescer.trigger()
				}
			case ev.Op&(fsnotify.Write|fsnotify.Create) != 0:
				st, readable := sessionStatusOf(ev.Name)
				if !readable {
					continue // partial write; skip, next event catches up
				}
				if lastStatus[ev.Name] != st {
					lastStatus[ev.Name] = st
					s.detectCoalescer.trigger() // real transition
				}
			}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			log.Printf("session watch error: %v", err)
		}
	}
}
