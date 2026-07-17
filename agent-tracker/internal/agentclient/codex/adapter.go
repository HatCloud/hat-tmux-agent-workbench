// Package codex implements agentclient.Adapter for the Codex CLI.
package codex

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/david/agent-tracker/internal/agentclient"
)

const clientID = "codex"

// Adapter detects Codex CLI threads via process tree + SQLite / rollout.
type Adapter struct {
	Home string
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

func (a *Adapter) stateDB() string {
	return filepath.Join(a.home(), ".codex", "state_5.sqlite")
}

// CommandLooksLikeCodex reports interactive codex (excludes exec / app-server).
func CommandLooksLikeCodex(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" || strings.Contains(command, " app-server") {
		return false
	}
	fields := strings.Fields(command)
	for i, f := range fields {
		if filepath.Base(f) != "codex" {
			continue
		}
		if i+1 < len(fields) && fields[i+1] == "exec" {
			return false
		}
		return true
	}
	return false
}

func (a *Adapter) Detect(idx *agentclient.Index, panePID int) (agentclient.LiveSession, bool) {
	if idx == nil || panePID <= 0 {
		return agentclient.LiveSession{}, false
	}
	codexPID := 0
	for _, pid := range idx.WalkSubtree(panePID) {
		if CommandLooksLikeCodex(idx.CommandFor(pid)) {
			codexPID = pid
			break
		}
	}
	if codexPID == 0 {
		return agentclient.LiveSession{}, false
	}
	// Prefer rollout via lsof when available.
	rollouts := openRollouts(codexPID)
	meta, ok := a.threadForRollouts(rollouts)
	if !ok {
		// Weak fallback: no thread meta yet but process is live.
		return agentclient.LiveSession{
			Client: clientID,
			Status: agentclient.StatusBusy,
			PID:    codexPID,
		}, true
	}
	status := a.statusFromRollout(meta.RolloutPath)
	if status == "" {
		status = agentclient.StatusIdle
	}
	return agentclient.LiveSession{
		Client:     clientID,
		Title:      meta.Title,
		Model:      meta.Model,
		Status:     status,
		SessionKey: meta.ID,
		PID:        codexPID,
	}, true
}

type threadMeta struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Model       string `json:"model"`
	RolloutPath string `json:"rollout_path"`
}

func shellSQLString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (a *Adapter) threadForRollouts(paths []string) (threadMeta, bool) {
	var quoted []string
	seen := map[string]bool{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		quoted = append(quoted, shellSQLString(p))
	}
	if len(quoted) == 0 {
		return threadMeta{}, false
	}
	query := `select id, title, coalesce(model, '') as model, rollout_path ` +
		`from threads where rollout_path in (` + strings.Join(quoted, ",") +
		`) and source = 'cli' order by updated_at_ms desc limit 1;`
	out, err := exec.Command("sqlite3", "-json", a.stateDB(), query).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return threadMeta{}, false
	}
	var rows []threadMeta
	if json.Unmarshal(out, &rows) != nil || len(rows) == 0 {
		return threadMeta{}, false
	}
	return rows[0], true
}

func openRollouts(pid int) []string {
	out, err := exec.Command("lsof", "-Fn", "-p", strconv.Itoa(pid)).CombinedOutput()
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "n") {
			continue
		}
		path := strings.TrimPrefix(line, "n")
		if strings.Contains(path, string(filepath.Separator)+".codex"+string(filepath.Separator)+"sessions"+string(filepath.Separator)) &&
			strings.HasPrefix(filepath.Base(path), "rollout-") &&
			strings.HasSuffix(path, ".jsonl") {
			paths = append(paths, path)
		}
	}
	return paths
}

// statusFromRollout is a simplified busy/idle scan (full quiet-asking remains in cmd until parity tests move).
func (a *Adapter) statusFromRollout(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return agentclient.StatusUnknown
	}
	// Tail last 512KB
	const capN = 512 << 10
	if len(data) > capN {
		data = data[len(data)-capN:]
		if i := strings.IndexByte(string(data), '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	status := agentclient.StatusIdle
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "task_started") && !strings.Contains(line, "task_complete") &&
			!strings.Contains(line, "turn_aborted") && !strings.Contains(line, "request_user_input") {
			continue
		}
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry.Type == "event_msg" {
			switch entry.Payload.Type {
			case "task_started":
				status = agentclient.StatusBusy
			case "task_complete", "turn_aborted", "thread_rolled_back":
				status = agentclient.StatusIdle
			}
		}
		if entry.Payload.Name == "request_user_input" || entry.Payload.Type == "request_user_input" {
			status = agentclient.StatusAsking
		}
	}
	return status
}

func (a *Adapter) WatchHints() []agentclient.WatchSource { return nil }

func (a *Adapter) ResumeArgv(sessionKey string) []string {
	if strings.TrimSpace(sessionKey) == "" {
		return []string{"codex", "resume"}
	}
	return []string{"codex", "resume", sessionKey}
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

var _ = time.Time{}
