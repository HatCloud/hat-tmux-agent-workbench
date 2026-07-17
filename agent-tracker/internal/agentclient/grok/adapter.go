// Package grok implements agentclient.Adapter for Grok Build.
package grok

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/david/agent-tracker/internal/agentclient"
)

const clientID = "grok"

// Adapter binds panes to ~/.grok active sessions + summary/events.
type Adapter struct {
	Home string // GROK_HOME override; empty → ~/.grok
}

func (a *Adapter) ID() string { return clientID }

func (a *Adapter) grokHome() string {
	if env := strings.TrimSpace(os.Getenv("GROK_HOME")); env != "" && a.Home == "" {
		return env
	}
	if strings.TrimSpace(a.Home) != "" {
		return a.Home
	}
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		h = os.Getenv("HOME")
	}
	return filepath.Join(h, ".grok")
}

type activeEntry struct {
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
	CWD       string `json:"cwd"`
}

// CommandLooksLikeGrokInteractive excludes headless -p / agent.
func CommandLooksLikeGrokInteractive(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	fields := strings.Fields(command)
	for i, f := range fields {
		base := filepath.Base(f)
		if base != "grok" {
			continue
		}
		// headless single-turn
		for j := i + 1; j < len(fields); j++ {
			if fields[j] == "-p" || fields[j] == "--single" || fields[j] == "--prompt-file" || fields[j] == "--prompt-json" {
				return false
			}
			if fields[j] == "agent" {
				return false
			}
		}
		return true
	}
	return false
}

func (a *Adapter) Detect(idx *agentclient.Index, panePID int) (agentclient.LiveSession, bool) {
	if idx == nil || panePID <= 0 {
		return agentclient.LiveSession{}, false
	}
	home := a.grokHome()
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		return agentclient.LiveSession{}, false
	}
	// Find interactive grok pid in subtree.
	var grokPID int
	for _, pid := range idx.WalkSubtree(panePID) {
		if CommandLooksLikeGrokInteractive(idx.CommandFor(pid)) {
			grokPID = pid
			break
		}
	}
	if grokPID == 0 {
		return agentclient.LiveSession{}, false
	}
	entries, err := a.loadActive()
	if err != nil {
		return agentclient.LiveSession{}, false
	}
	var ent *activeEntry
	for i := range entries {
		if entries[i].PID == grokPID {
			ent = &entries[i]
			break
		}
	}
	if ent == nil {
		// Process looks like grok but not in active_sessions → unknown binding
		return agentclient.LiveSession{}, false
	}
	// Path must stay under grok home.
	sessionDir, ok := a.sessionDir(home, ent)
	if !ok {
		return agentclient.LiveSession{}, false
	}
	title, model := readSummary(filepath.Join(sessionDir, "summary.json"))
	status := statusFromEvents(filepath.Join(sessionDir, "events.jsonl"))
	return agentclient.LiveSession{
		Client:     clientID,
		Title:      title,
		Model:      model,
		Status:     status,
		SessionKey: ent.SessionID,
		PID:        grokPID,
	}, true
}

func (a *Adapter) loadActive() ([]activeEntry, error) {
	path := filepath.Join(a.grokHome(), "active_sessions.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []activeEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (a *Adapter) sessionDir(home string, ent *activeEntry) (string, bool) {
	// Prefer encoded cwd group under sessions/
	sessionsRoot := filepath.Join(home, "sessions")
	// Walk for session id dir (bounded)
	var found string
	_ = filepath.WalkDir(sessionsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return err
		}
		if d.IsDir() && d.Name() == ent.SessionID {
			// ensure under home
			rel, err := filepath.Rel(home, path)
			if err != nil || strings.HasPrefix(rel, "..") {
				return nil
			}
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		return "", false
	}
	return found, true
}

func readSummary(path string) (title, model string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var s struct {
		GeneratedTitle  string `json:"generated_title"`
		SessionSummary  string `json:"session_summary"`
		CurrentModelID  string `json:"current_model_id"`
	}
	if json.Unmarshal(data, &s) != nil {
		return "", ""
	}
	title = strings.TrimSpace(s.GeneratedTitle)
	if title == "" {
		title = strings.TrimSpace(s.SessionSummary)
	}
	return title, strings.TrimSpace(s.CurrentModelID)
}

// statusFromEvents maps events.jsonl tail → busy|asking|idle|unknown.
// Idle only after an explicit turn_ended with no subsequent busy signal
// (turn_started / busy phase / tool activity). Residual phase text after
// turn_ended does not keep the session busy (design: avoid false busy).
// Parse failure / empty → unknown (not idle — avoid false completion 🔔).
func statusFromEvents(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return agentclient.StatusUnknown
	}
	const capN = 256 << 10
	if len(data) > capN {
		data = data[len(data)-capN:]
		if i := strings.IndexByte(string(data), '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	type ev struct {
		Type  string `json:"type"`
		Phase string `json:"phase"`
	}
	busyPhases := map[string]bool{
		"waiting_for_model":   true,
		"streaming_reasoning": true,
		"streaming_text":      true,
		"tool_execution":      true,
	}
	// activity after last turn_ended?
	active := false
	ended := false
	asking := false
	sawAny := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e ev
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		sawAny = true
		switch e.Type {
		case "turn_started", "loop_started":
			active = true
			ended = false
			asking = false
		case "turn_ended":
			ended = true
			active = false
			asking = false
		case "phase_changed":
			if e.Phase == "permission_prompt" {
				asking = true
				active = true
				ended = false
			} else if busyPhases[e.Phase] {
				// Only count as activity if we have not just ended, or a new turn started.
				// Residual streaming_* lines after turn_ended are ignored.
				if !ended {
					active = true
				}
			}
		case "tool_started", "first_token":
			if !ended {
				active = true
			}
		case "permission_requested":
			asking = true
			active = true
			ended = false
		}
	}
	if !sawAny {
		return agentclient.StatusUnknown
	}
	if asking {
		return agentclient.StatusAsking
	}
	if active {
		return agentclient.StatusBusy
	}
	if ended {
		return agentclient.StatusIdle
	}
	return agentclient.StatusUnknown
}

func (a *Adapter) WatchHints() []agentclient.WatchSource {
	// Default poll-only per design (no events.jsonl watch).
	return nil
}

func (a *Adapter) ResumeArgv(sessionKey string) []string {
	if strings.TrimSpace(sessionKey) == "" {
		return []string{"grok", "--resume"}
	}
	// reject obvious injection
	if strings.ContainsAny(sessionKey, " \t\n\r\"'`#$") {
		return []string{"grok", "--resume"}
	}
	return []string{"grok", "--resume", sessionKey}
}

func (a *Adapter) RetryPolicy() agentclient.RetryPolicy {
	return agentclient.RetryPolicy{Enabled: false}
}

func Register() {
	agentclient.RegisterDefault(&Adapter{})
}

var (
	_ agentclient.Adapter       = (*Adapter)(nil)
	_ agentclient.WatchHinter   = (*Adapter)(nil)
	_ agentclient.ResumeArgver  = (*Adapter)(nil)
	_ agentclient.RetryPolicier = (*Adapter)(nil)
)
