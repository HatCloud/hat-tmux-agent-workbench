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
	Client       string
	Provider     string
	Model        string
	Title        string
	Status       string
	LimitResetAt *time.Time
	Error        *TurnError
	SessionKey   string
	PID          int
}
