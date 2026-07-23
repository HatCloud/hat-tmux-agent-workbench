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

// sessionMeta mirrors ~/.claude/sessions/<pid>.json.
type sessionMeta struct {
	PID        int    `json:"pid"`
	Name       string `json:"name"`
	NameSource string `json:"nameSource"`
	Status     string `json:"status"`
	SessionID  string `json:"sessionId"`
	CWD        string `json:"cwd"`
	// Entrypoint distinguishes the interactive TUI ("cli") from headless runs
	// ("sdk-cli" for `claude -p`); kind is "interactive" for BOTH, so this is
	// the only usable discriminator.
	Entrypoint string `json:"entrypoint"`
}

func sessionNameStateFromMeta(meta sessionMeta) agentclient.SessionNameState {
	state := agentclient.SessionNameState{
		Value: strings.TrimSpace(meta.Name), Source: agentclient.SessionNameNone, Writable: true,
	}
	if state.Value == "" {
		return state
	}
	if strings.EqualFold(strings.TrimSpace(meta.NameSource), "derived") {
		state.Source = agentclient.SessionNameGenerated
	} else {
		state.Source = agentclient.SessionNameUser
	}
	return state
}

// isWindowAgentSession reports whether a session should drive window features
// (naming, status prefix, task tracking, auto-retry). Only the interactive TUI
// qualifies: headless `claude -p` children (e.g. agent-hl workers spawned under
// a pane) also write session files, and matching them via the pane process tree
// used to hijack the window's state. Empty entrypoint is accepted for older
// Claude versions that predate the field.
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
			continue // headless (sdk-cli) sessions must not drive window state
		}
		if meta.PID == 0 {
			meta.PID = pid
		}
		out[pid] = meta
	}
	return out
}

// sessionsByPID caches the sessions-dir load for one sync pass.
func (a *Adapter) sessionsByPID(idx *agentclient.Index) map[int]sessionMeta {
	v := idx.Memo("claude.sessions", func() any { return a.loadByPID() })
	m, _ := v.(map[int]sessionMeta)
	return m
}

func (a *Adapter) jsonlPath(meta sessionMeta) string {
	if meta.SessionID == "" || meta.CWD == "" {
		return ""
	}
	slug := strings.NewReplacer("/", "-", "_", "-", ".", "-").Replace(meta.CWD)
	return filepath.Join(a.home(), ".claude", "projects", slug, meta.SessionID+".jsonl")
}

// Detect finds a Claude session in the pane process tree and returns the full
// normalized snapshot: title (user-set name → latest ai-title), live model
// (JSONL tail → --model arg → provider env), provider, and the limited ([L]) /
// error ([E]) overlays with their reset/retry payloads.
func (a *Adapter) Detect(idx *agentclient.Index, panePID int) (agentclient.LiveSession, bool) {
	if idx == nil || panePID <= 0 {
		return agentclient.LiveSession{}, false
	}
	byPID := a.sessionsByPID(idx)
	if len(byPID) == 0 {
		return agentclient.LiveSession{}, false
	}
	subtree := idx.WalkSubtree(panePID)
	for _, pid := range subtree {
		meta, ok := byPID[pid]
		if !ok {
			continue
		}
		status := strings.TrimSpace(meta.Status)
		if status == "" {
			status = agentclient.StatusIdle
		}
		jsonl := a.jsonlPath(meta)
		s := agentclient.LiveSession{
			Client:       clientID,
			Title:        meta.Name,
			PersistTitle: meta.Name,
			Status:       status,
			SessionKey:   meta.SessionID,
			PID:          pid,
			CWD:          meta.CWD,
			SourcePath:   jsonl,
			Name:         sessionNameStateFromMeta(meta),
		}
		// Model + ai-title in one bounded read; the full-scan fallback only
		// triggers while data is actually missing from the tail.
		model, aiTitle, customTitle := probeSessionJSONL(jsonl,
			meta.Name == "" || s.Name.Source == agentclient.SessionNameGenerated)
		if customTitle != "" {
			s.Title = customTitle
			s.PersistTitle = customTitle
			s.Name = agentclient.SessionNameState{
				Value: customTitle, Source: agentclient.SessionNameUser, Writable: true,
			}
		}
		if s.Name.Source != agentclient.SessionNameUser && aiTitle != "" {
			s.Title = aiTitle
		}
		s.Provider = a.providerForPID(idx, pid)
		switch {
		case model != "":
			s.Model = model
		default:
			// No assistant message yet: fall back to the launch-time --model arg,
			// then to the provider env's ANTHROPIC_MODEL (e.g. minimax).
			if m := modelFromArgs(idx.CommandFor(pid)); m != "" {
				s.Model = m
			} else if m := a.modelFromProviderEnv(s.Provider); m != "" {
				s.Model = m
			}
		}
		now := time.Now()
		// A session whose latest turn died on a usage-limit 429 is its own
		// "limited" status ([L]): the dialog blocks input and no timer/idle
		// semantics apply. Only probed while not busy.
		if !strings.EqualFold(s.Status, agentclient.StatusBusy) {
			if resetAt, ok := limitResetFromJSONL(jsonl, now); ok {
				s.Status = agentclient.StatusLimited
				s.LimitResetAt = &resetAt
			} else if terr, ok := turnErrorFromJSONL(jsonl); ok {
				// A turn that stopped on an API error (5xx/529, auth, …) is the
				// "error" status ([E]); Retryable gates the auto-retry engine.
				s.Status = agentclient.StatusError
				s.Error = &agentclient.TurnError{
					Type:      terr.Type,
					Status:    terr.Status,
					At:        terr.At,
					Retryable: terr.retryable(),
				}
			}
		}
		// Busy-shell allowlist: a turn that ended into shell/idle but whose pane
		// subtree still runs an allowlisted background task (default: agent-hl
		// launchers) reads as busy ([B]) instead of idle. Applied AFTER the
		// limited/error overlay and only while still shell/idle, so the precedence
		// stays error > limited > bg-busy > idle. Patterns come from the SideCar
		// (orchestrator-injected config) or the built-in default.
		s.Status = resolveBusyShell(s.Status, subtreeCommands(idx, subtree), busyShellPatternsFromIndex(idx))
		return s, true
	}
	return agentclient.LiveSession{}, false
}

// FirstPrompt returns the first user message text of the session transcript.
func (a *Adapter) FirstPrompt(s agentclient.LiveSession) string {
	return firstPromptFromJSONL(s.SourcePath)
}

// QuotaReset resolves when the usage window resets: exact from an unresolved
// 429 stamp in the session JSONL, else estimated from the statusline
// rate-limits cache (exact=false → caller keeps the timer upgrade-eligible).
func (a *Adapter) QuotaReset(s agentclient.LiveSession, now time.Time) (time.Time, bool, bool) {
	if s.SourcePath != "" {
		if at, ok := limitResetFromJSONL(s.SourcePath, now); ok {
			return at, true, true
		}
	}
	if at, ok := fallbackResetAt(now); ok {
		return at, false, true
	}
	return time.Time{}, false, false
}

// WatchHints for daemon fsnotify (Claude sessions dir + status field dedupe).
func (a *Adapter) WatchHints() []agentclient.WatchSource {
	return []agentclient.WatchSource{{
		Path:              a.sessionsDir(),
		StatusFieldDedupe: "status",
	}}
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

var (
	_ agentclient.Adapter       = (*Adapter)(nil)
	_ agentclient.WatchHinter   = (*Adapter)(nil)
	_ agentclient.RetryPolicier = (*Adapter)(nil)
	_ agentclient.FirstPrompter = (*Adapter)(nil)
	_ agentclient.QuotaProvider = (*Adapter)(nil)
	_ agentclient.SessionNamer  = (*Adapter)(nil)
)
