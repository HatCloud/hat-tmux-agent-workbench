// Package claude implements agentclient.Adapter for Claude Code interactive sessions.
package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/david/agent-tracker/internal/agentclient"
)

const clientID = "claude"

// Adapter detects Claude Code sessions via ~/.claude/sessions/<pid>.json.
type Adapter struct {
	Home string // empty → os.UserHomeDir
}

func (a *Adapter) ID() string { return clientID }

func (a *Adapter) home() string {
	if strings.TrimSpace(a.Home) != "" {
		return a.Home
	}
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return os.Getenv("HOME")
	}
	return h
}

func (a *Adapter) sessionsDir() string {
	return filepath.Join(a.home(), ".claude", "sessions")
}

type sessionMeta struct {
	PID        int    `json:"pid"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	SessionID  string `json:"sessionId"`
	CWD        string `json:"cwd"`
	Entrypoint string `json:"entrypoint"`
}

func isWindowAgentSession(meta sessionMeta) bool {
	return meta.Entrypoint == "" || meta.Entrypoint == "cli"
}

func (a *Adapter) loadByPID() map[int]sessionMeta {
	out := map[int]sessionMeta{}
	entries, err := os.ReadDir(a.sessionsDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(a.sessionsDir(), e.Name()))
		if err != nil {
			continue
		}
		var meta sessionMeta
		if json.Unmarshal(data, &meta) != nil {
			continue
		}
		meta.Name = strings.TrimSpace(meta.Name)
		if !isWindowAgentSession(meta) {
			continue
		}
		if meta.PID == 0 {
			meta.PID = pid
		}
		out[pid] = meta
	}
	return out
}

// Detect finds a Claude session in the pane process tree.
func (a *Adapter) Detect(idx *agentclient.Index, panePID int) (agentclient.LiveSession, bool) {
	if idx == nil || panePID <= 0 {
		return agentclient.LiveSession{}, false
	}
	byPID := a.loadByPID()
	for _, pid := range idx.WalkSubtree(panePID) {
		meta, ok := byPID[pid]
		if !ok {
			continue
		}
		// Live process check: pid must appear in index commands (or be pane root).
		if _, known := idx.Commands[pid]; !known && pid != panePID {
			// Still accept if session file names this pid — process may be short-lived in index race.
		}
		status := strings.TrimSpace(meta.Status)
		if status == "" {
			status = agentclient.StatusIdle
		}
		s := agentclient.LiveSession{
			Client:     clientID,
			Title:      meta.Name,
			Status:     status,
			SessionKey: meta.SessionID,
			PID:        pid,
		}
		if m, t := a.modelAndTitleFromJSONL(meta); m != "" || t != "" {
			if m != "" {
				s.Model = m
			}
			if s.Title == "" && t != "" {
				s.Title = t
			}
		}
		return s, true
	}
	return agentclient.LiveSession{}, false
}

func (a *Adapter) jsonlPath(meta sessionMeta) string {
	if meta.SessionID == "" || meta.CWD == "" {
		return ""
	}
	slug := strings.NewReplacer("/", "-", "_", "-", ".", "-").Replace(meta.CWD)
	return filepath.Join(a.home(), ".claude", "projects", slug, meta.SessionID+".jsonl")
}

func (a *Adapter) modelAndTitleFromJSONL(meta sessionMeta) (model, aiTitle string) {
	path := a.jsonlPath(meta)
	if path == "" {
		return "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	// Tail ~256KB like existing liveModelFromSession.
	const tail = 256 << 10
	info, err := f.Stat()
	if err != nil {
		return "", ""
	}
	start := int64(0)
	if info.Size() > tail {
		start = info.Size() - tail
	}
	if start > 0 {
		if _, err := f.Seek(start, 0); err != nil {
			return "", ""
		}
	}
	buf := make([]byte, info.Size()-start)
	n, _ := f.Read(buf)
	buf = buf[:n]
	// Skip partial first line when tailed.
	if start > 0 {
		if i := strings.IndexByte(string(buf), '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	for _, line := range strings.Split(string(buf), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		var typ string
		_ = json.Unmarshal(raw["type"], &typ)
		if typ == "assistant" {
			var msg struct {
				Message struct {
					Model string `json:"model"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(line), &msg) == nil && msg.Message.Model != "" {
				model = msg.Message.Model
			}
		}
		if typ == "ai-title" || typ == "title" {
			var t struct {
				Title string `json:"title"`
				Name  string `json:"name"`
			}
			if json.Unmarshal([]byte(line), &t) == nil {
				if t.Title != "" {
					aiTitle = t.Title
				} else if t.Name != "" {
					aiTitle = t.Name
				}
			}
		}
	}
	return model, aiTitle
}

// FirstPrompt returns the first user message text when available.
func (a *Adapter) FirstPrompt(s agentclient.LiveSession) string {
	// Minimal: reopen session meta via SessionKey is insufficient without cwd;
	// consumers may still use legacy path until full migrate.
	return ""
}

// WatchHints for daemon fsnotify (Claude sessions dir + status field dedupe).
func (a *Adapter) WatchHints() []agentclient.WatchSource {
	return []agentclient.WatchSource{{
		Path:              a.sessionsDir(),
		StatusFieldDedupe: "status",
	}}
}

// ResumeArgv for workspace restore.
func (a *Adapter) ResumeArgv(sessionKey string) []string {
	if strings.TrimSpace(sessionKey) == "" {
		return []string{"claude", "--resume"}
	}
	return []string{"claude", "--resume", sessionKey}
}

// RetryPolicy enables Claude auto-retry inject.
func (a *Adapter) RetryPolicy() agentclient.RetryPolicy {
	return agentclient.RetryPolicy{
		Enabled: true,
		Message: "Continue where you left off.",
	}
}

// Register installs into the default registry (idempotent).
func Register() {
	agentclient.RegisterDefault(&Adapter{})
}

// Ensure interface compliance.
var (
	_ agentclient.Adapter      = (*Adapter)(nil)
	_ agentclient.WatchHinter  = (*Adapter)(nil)
	_ agentclient.ResumeArgver = (*Adapter)(nil)
	_ agentclient.RetryPolicier = (*Adapter)(nil)
)

// IdleSince is unused helper placeholder for future quota.
var _ = time.Time{}
