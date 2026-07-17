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

	"github.com/david/agent-tracker/internal/agentclient"
	"github.com/fsnotify/fsnotify"
)

// Event-driven detection: adapters declare WatchHints (Claude: sessions dir with
// status-field dedupe). Watching those paths lets the daemon pick up transitions
// in ~ms instead of waiting for the fixed status-bar poll. The periodic poll
// stays as a safety net; Grok intentionally returns no hints (poll-only).

func watchSources() []agentclient.WatchSource {
	var out []agentclient.WatchSource
	seen := map[string]bool{}
	for _, a := range agentclient.DefaultRegistry().Adapters {
		h, ok := a.(agentclient.WatchHinter)
		if !ok {
			continue
		}
		for _, src := range h.WatchHints() {
			if src.Path == "" || seen[src.Path] {
				continue
			}
			seen[src.Path] = true
			out = append(out, src)
		}
	}
	return out
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

// watchSessionFiles watches adapter WatchHints paths and, on a real status
// transition (Claude) or any write (generic), coalesces a sync-names run.
// Best-effort: setup failure logs and returns; periodic poll remains fallback.
func (s *server) watchSessionFiles() {
	sources := watchSources()
	if len(sources) == 0 {
		return
	}
	// Map path → whether to dedupe on JSON status field.
	dedupeByStatus := map[string]bool{}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("session watch: %v", err)
		return
	}
	defer w.Close()
	for _, src := range sources {
		if err := os.MkdirAll(src.Path, 0o755); err != nil {
			log.Printf("session watch: mkdir %s: %v", src.Path, err)
			continue
		}
		if err := w.Add(src.Path); err != nil {
			log.Printf("session watch add %s: %v", src.Path, err)
			continue
		}
		if src.StatusFieldDedupe == "status" {
			dedupeByStatus[src.Path] = true
		}
	}
	// Last-seen status per session file (Claude heartbeat dedupe).
	lastStatus := map[string]string{}
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			dir := filepath.Dir(ev.Name)
			useDedupe := dedupeByStatus[dir]
			if useDedupe && !strings.HasSuffix(ev.Name, ".json") {
				continue
			}
			switch {
			case ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
				if _, tracked := lastStatus[ev.Name]; tracked {
					delete(lastStatus, ev.Name)
					s.detectCoalescer.trigger()
				} else if !useDedupe {
					s.detectCoalescer.trigger()
				}
			case ev.Op&(fsnotify.Write|fsnotify.Create) != 0:
				if useDedupe {
					st, readable := sessionStatusOf(ev.Name)
					if !readable {
						continue
					}
					if lastStatus[ev.Name] != st {
						lastStatus[ev.Name] = st
						s.detectCoalescer.trigger()
					}
				} else {
					s.detectCoalescer.trigger()
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
