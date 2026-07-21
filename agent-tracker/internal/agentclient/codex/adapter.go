// Package codex implements agentclient.Adapter for the Codex CLI.
package codex

import (
	"encoding/json"
	"os"
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

func (a *Adapter) logsDB() string {
	return filepath.Join(a.home(), ".codex", "logs_2.sqlite")
}

// CommandLooksLikeCodex reports interactive codex. Headless `codex exec` runs
// (agent-hl workers etc.) must not drive window naming/status — mirrors the
// Claude sdk-cli entrypoint filter — and `codex app-server` is not a session.
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

// procFiles is the per-pass lsof sidecar: open rollouts + cwd per codex pid.
type procFiles struct {
	Rollouts map[int][]string
	CWDs     map[int]string
}

// codexProcFiles batches ONE lsof over every codex pid in the index, so a pass
// over N windows costs one subprocess instead of N.
func (a *Adapter) codexProcFiles(idx *agentclient.Index) procFiles {
	v := idx.Memo("codex.procfiles", func() any {
		var pids []string
		for pid, command := range idx.Commands {
			if CommandLooksLikeCodex(command) {
				pids = append(pids, strconv.Itoa(pid))
			}
		}
		if len(pids) == 0 {
			return procFiles{Rollouts: map[int][]string{}, CWDs: map[int]string{}}
		}
		out, err := agentclient.RunOutput(3*time.Second, "lsof", "-Ffn", "-p", strings.Join(pids, ","))
		if err != nil {
			// lsof exits non-zero when any pid is gone; its partial output is
			// still usable.
			if len(out) == 0 {
				return procFiles{Rollouts: map[int][]string{}, CWDs: map[int]string{}}
			}
		}
		return parseProcFilesFromLsof(string(out))
	})
	pf, _ := v.(procFiles)
	return pf
}

// parseProcFilesFromLsof extracts per-pid rollout paths and the process cwd
// from `lsof -Ffn` output (p<pid> / f<fd> / n<name> records).
func parseProcFilesFromLsof(out string) procFiles {
	pf := procFiles{Rollouts: map[int][]string{}, CWDs: map[int]string{}}
	pid := 0
	fd := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(strings.TrimPrefix(line, "p"))
			fd = ""
		case strings.HasPrefix(line, "f"):
			fd = strings.TrimPrefix(line, "f")
		case strings.HasPrefix(line, "n") && pid > 0:
			path := strings.TrimPrefix(line, "n")
			if fd == "cwd" {
				pf.CWDs[pid] = path
				continue
			}
			if strings.Contains(path, string(filepath.Separator)+".codex"+string(filepath.Separator)+"sessions"+string(filepath.Separator)) &&
				strings.HasPrefix(filepath.Base(path), "rollout-") &&
				strings.HasSuffix(path, ".jsonl") {
				pf.Rollouts[pid] = append(pf.Rollouts[pid], path)
			}
		}
	}
	return pf
}

type threadMeta struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	CWD         string `json:"cwd"`
	Model       string `json:"model"`
	RolloutPath string `json:"rollout_path"`
}

func shellSQLString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (a *Adapter) queryThreads(where string) (threadMeta, bool) {
	query := `select id, title, cwd, coalesce(model, '') as model, rollout_path ` +
		`from threads where ` + where +
		` and source = 'cli' order by updated_at_ms desc limit 1;`
	out, err := agentclient.RunOutput(3*time.Second, "sqlite3", "-json", a.stateDB(), query)
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return threadMeta{}, false
	}
	var rows []threadMeta
	if json.Unmarshal(out, &rows) != nil || len(rows) == 0 {
		return threadMeta{}, false
	}
	return rows[0], true
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
	return a.queryThreads(`rollout_path in (` + strings.Join(quoted, ",") + `)`)
}

func (a *Adapter) latestThreadForCWD(cwd string) (threadMeta, bool) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return threadMeta{}, false
	}
	return a.queryThreads(`cwd = ` + shellSQLString(cwd))
}

// threadStatus resolves the rollout status + SQLite turn-error overlay, cached
// per (thread, rollout) for one sync pass — the scan walks the whole rollout.
func (a *Adapter) threadStatus(idx *agentclient.Index, meta threadMeta) string {
	cache, _ := idx.Memo("codex.statuses", func() any { return map[string]string{} }).(map[string]string)
	key := meta.ID + "\x00" + meta.RolloutPath
	if cache != nil {
		if s, ok := cache[key]; ok {
			return s
		}
	}
	snapshot := statusSnapshotFromRolloutAt(meta.RolloutPath, time.Now())
	errorAt, _ := latestTurnErrorFromDB(a.logsDB(), meta.ID, snapshot.LastTaskStartedAt)
	status := resolveStatus(snapshot, errorAt)
	if cache != nil {
		cache[key] = status
	}
	return status
}

// Detect finds an interactive codex process in the pane subtree and resolves
// its thread meta (rollout → SQLite, falling back to the process cwd's latest
// thread), live status, and the limited ([L]) overlay.
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
	pf := a.codexProcFiles(idx)
	var rollouts []string
	for _, pid := range idx.WalkSubtree(panePID) {
		rollouts = append(rollouts, pf.Rollouts[pid]...)
	}
	meta, ok := a.threadForRollouts(rollouts)
	if !ok {
		// No rollout binding yet (thread just starting): fall back to the
		// process cwd's most recent thread.
		meta, _ = a.latestThreadForCWD(pf.CWDs[codexPID])
	}
	status := a.threadStatus(idx, meta)
	if status == "" {
		status = agentclient.StatusUnknown
	}
	s := agentclient.LiveSession{
		Client:       clientID,
		Title:        meta.Title,
		PersistTitle: meta.Title,
		Model:        meta.Model,
		Status:       status,
		SessionKey:   meta.ID,
		PID:          codexPID,
		CWD:          meta.CWD,
		SourcePath:   meta.RolloutPath,
	}
	if status == "error" {
		// Codex reports no error class; the SQLite probe only proves the turn
		// stopped. Not retryable — auto-retry stays a per-adapter policy.
		s.Error = &agentclient.TurnError{At: time.Now(), Retryable: false}
	}
	// A thread whose latest rate_limits snapshot shows an exhausted window is
	// its own "limited" status ([L]), mirroring the Claude 429 probe.
	if !strings.EqualFold(s.Status, agentclient.StatusBusy) {
		if resetAt, ok := a.exhaustedResetAt(meta.RolloutPath, time.Now()); ok {
			s.Status = agentclient.StatusLimited
			s.LimitResetAt = &resetAt
			s.Error = nil
		}
	}
	return s, true
}

// FirstPrompt returns the first user message in the thread's rollout.
func (a *Adapter) FirstPrompt(s agentclient.LiveSession) string {
	return promptFromRollout(s.SourcePath)
}

// QuotaReset picks the reset instant to wait for from the thread's rollout
// rate_limits snapshot (any fresh rollout works — quota is account-wide).
func (a *Adapter) QuotaReset(s agentclient.LiveSession, now time.Time) (time.Time, bool, bool) {
	path := strings.TrimSpace(s.SourcePath)
	if path == "" {
		path = a.latestRolloutPath()
	}
	rl, ok := rateLimitsFromRollout(path)
	if !ok {
		return time.Time{}, false, false
	}
	at, ok := agentclient.PickReset(rl.windows(), now)
	return at, true, ok
}

func Register() {
	agentclient.RegisterDefault(&Adapter{})
}

var (
	_ agentclient.Adapter       = (*Adapter)(nil)
	_ agentclient.FirstPrompter = (*Adapter)(nil)
	_ agentclient.QuotaProvider = (*Adapter)(nil)
)
