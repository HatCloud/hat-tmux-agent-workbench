// Package agentclient defines the live agent-session adapter surface used by
// window naming, sync-names, quota, retry, and workspace resume.
package agentclient

import "time"

// Canonical status values shared with internal/statustag and daemon reconcile.
const (
	StatusBusy    = "busy"
	StatusIdle    = "idle"
	StatusShell   = "shell"
	StatusAsking  = "asking"
	StatusWaiting = "waiting"
	StatusPaused  = "paused"
	StatusLimited = "limited"
	StatusError   = "error"
	StatusUnknown = "unknown"
)

// TurnError is an optional terminal-turn failure surface for [E] / auto-retry.
type TurnError struct {
	Type      string
	Status    int
	At        time.Time
	Retryable bool
	Message   string
}

// LiveSession is the normalized snapshot consumers use. Status==unknown must
// not drive finish_task / completion notifications.
type LiveSession struct {
	Client   string
	Provider string
	Model    string
	Title    string
	// PersistTitle is what the orchestrator may write into the window's
	// @agent_title option. Claude sets it only for a user-set session name —
	// persisting the auto ai-title would overwrite a typed `prefix ]` title and
	// outlive the session. Codex/Grok persist their (auto) titles, matching the
	// pre-adapter behavior.
	PersistTitle string
	Status       string
	LimitResetAt *time.Time
	Error        *TurnError
	SessionKey   string
	PID          int
	CWD          string
	// SourcePath is the adapter's primary on-disk transcript for this session
	// (Claude: project JSONL; Codex: rollout JSONL; Grok: session dir), used by
	// FirstPrompter / QuotaProvider without re-deriving it.
	SourcePath string
	// Name is the native session naming state reported by the adapter. Title may
	// still contain an agent-generated default when Name.Source is none.
	Name SessionNameState
}
