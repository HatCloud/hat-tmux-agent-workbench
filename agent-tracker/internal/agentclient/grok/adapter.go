// Package grok implements agentclient.Adapter for Grok Build.
package grok

import (
	"bufio"
	"context"
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
// Matches `grok` and platform binaries like `grok-macos-aarc` (pane_current_command).
func CommandLooksLikeGrokInteractive(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	fields := strings.Fields(command)
	for i, f := range fields {
		base := filepath.Base(f)
		// "grok", "grok-macos-aarc", "grok-linux-x64", …
		if base != "grok" && !strings.HasPrefix(base, "grok-") {
			continue
		}
		// headless single-turn / agent subcommand after the binary token
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
	entries, ok := a.activeEntries(idx)
	if !ok {
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
	summary, summaryOK := readSummaryData(filepath.Join(sessionDir, "summary.json"))
	title, model := summary.title(), strings.TrimSpace(summary.CurrentModelID)
	nameState := sessionNameState(summary, summaryOK)
	status := statusFromEvents(filepath.Join(sessionDir, "events.jsonl"))
	return agentclient.LiveSession{
		Client:       clientID,
		Title:        title,
		PersistTitle: title,
		Model:        model,
		Status:       status,
		SessionKey:   ent.SessionID,
		PID:          grokPID,
		CWD:          ent.CWD,
		SourcePath:   sessionDir,
		Name:         nameState,
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

// activeEntries caches the active_sessions.json load for one sync pass.
func (a *Adapter) activeEntries(idx *agentclient.Index) ([]activeEntry, bool) {
	type cached struct {
		entries []activeEntry
		ok      bool
	}
	v := idx.Memo("grok.active", func() any {
		entries, err := a.loadActive()
		return cached{entries: entries, ok: err == nil}
	})
	c, _ := v.(cached)
	return c.entries, c.ok
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

type summaryData struct {
	GeneratedTitle string `json:"generated_title"`
	SessionSummary string `json:"session_summary"`
	CurrentModelID string `json:"current_model_id"`
}

func (s summaryData) title() string {
	if title := strings.TrimSpace(s.GeneratedTitle); title != "" {
		return title
	}
	return strings.TrimSpace(s.SessionSummary)
}

func readSummaryData(path string) (summaryData, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return summaryData{}, false
	}
	var s summaryData
	if json.Unmarshal(data, &s) != nil {
		return summaryData{}, false
	}
	s.GeneratedTitle = strings.TrimSpace(s.GeneratedTitle)
	s.SessionSummary = strings.TrimSpace(s.SessionSummary)
	return s, true
}

func sessionNameState(summary summaryData, valid bool) agentclient.SessionNameState {
	if !valid || (summary.GeneratedTitle != "" && summary.SessionSummary == "") {
		return agentclient.SessionNameState{Value: summary.GeneratedTitle, Source: agentclient.SessionNameUnknown}
	}
	if summary.GeneratedTitle == "" {
		return agentclient.SessionNameState{Source: agentclient.SessionNameNone}
	}
	if summary.GeneratedTitle != summary.SessionSummary {
		return agentclient.SessionNameState{Value: summary.GeneratedTitle, Source: agentclient.SessionNameUser}
	}
	return agentclient.SessionNameState{Value: summary.GeneratedTitle, Source: agentclient.SessionNameGenerated}
}

// SessionName distinguishes Grok's default title (generated_title equals
// session_summary) from /rename (generated_title differs). This is the on-disk
// shape used by Grok 0.2.x. Grok exposes interactive /rename but no verified
// external rename API, so generated names stay tracker-owned.
func (a *Adapter) SessionName(s agentclient.LiveSession) (agentclient.SessionNameState, error) {
	summary, ok := readSummaryData(filepath.Join(s.SourcePath, "summary.json"))
	return sessionNameState(summary, ok), nil
}

func (a *Adapter) SetSessionName(context.Context, agentclient.LiveSession, string) error {
	return agentclient.ErrSessionNameUnsupported
}

func (a *Adapter) FirstPrompt(s agentclient.LiveSession) string {
	f, err := os.Open(filepath.Join(s.SourcePath, "chat_history.jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "user" {
			continue
		}
		var parts []string
		for _, part := range entry.Content {
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				parts = append(parts, strings.TrimSpace(part.Text))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return ""
}

// statusFromEvents maps events.jsonl tail → busy|asking|idle|unknown.
//
// Idle only after an explicit turn_ended with no subsequent busy signal.
// Residual phase text after turn_ended does not keep the session busy.
// Parse failure / empty → unknown (not idle — avoid false completion 🔔).
//
// Asking is ONLY the *current* unresolved permission gate:
// every Grok tool call briefly emits permission_prompt → permission_requested
// → permission_resolved (often wait_ms:0 auto-allow). A sticky "asking" flag
// would leave the window on [?] for the rest of the turn — which users hit
// when they queue a message (Enter while busy) mid-tooling.
func statusFromEvents(path string) string {
	// Tail-only read (align Claude 256KB cap) — never load multi-MB jsonl fully.
	const capN = 256 << 10
	f, err := os.Open(path)
	if err != nil {
		return agentclient.StatusUnknown
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return agentclient.StatusUnknown
	}
	start := int64(0)
	if info.Size() > capN {
		start = info.Size() - capN
	}
	if start > 0 {
		if _, err := f.Seek(start, 0); err != nil {
			return agentclient.StatusUnknown
		}
	}
	buf := make([]byte, info.Size()-start)
	n, _ := f.Read(buf)
	data := buf[:n]
	if start > 0 {
		// Drop partial first line after seek.
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
	active := false
	ended := false
	// pendingPermission: true only while waiting on a human/auto gate that has
	// not yet resolved. Cleared on permission_resolved and on any post-gate
	// busy work (tool_execution / streaming / tool_started).
	pendingPermission := false
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
			pendingPermission = false
		case "turn_ended":
			ended = true
			active = false
			pendingPermission = false
		case "phase_changed":
			switch {
			case e.Phase == "permission_prompt":
				// Gate opened (may auto-resolve in the same millisecond).
				pendingPermission = true
				active = true
				ended = false
			case busyPhases[e.Phase]:
				// Left the permission gate for real work — not asking.
				pendingPermission = false
				if !ended {
					active = true
				}
			}
		case "permission_requested":
			pendingPermission = true
			active = true
			ended = false
		case "permission_resolved":
			// Auto-allow (wait_ms:0) or user click — gate closed.
			pendingPermission = false
			if !ended {
				active = true
			}
		case "tool_started", "tool_completed", "first_token":
			pendingPermission = false
			if !ended {
				active = true
			}
		}
	}
	if !sawAny {
		return agentclient.StatusUnknown
	}
	// Only [?] when the *latest* state is still sitting on an open permission.
	if pendingPermission {
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

func Register() {
	agentclient.RegisterDefault(&Adapter{})
}

var _ agentclient.SessionNamer = (*Adapter)(nil)
var _ agentclient.FirstPrompter = (*Adapter)(nil)

var _ agentclient.Adapter = (*Adapter)(nil)
