package agentclient

import "time"

// Adapter detects a live interactive agent session under a pane process tree.
type Adapter interface {
	ID() string
	// Detect returns a session only on strict success (live process + consistent sidecars).
	Detect(idx *Index, panePID int) (LiveSession, bool)
}

// FirstPrompter is optional: Window Nav "p" prompt preview.
type FirstPrompter interface {
	FirstPrompt(s LiveSession) string
}

// QuotaProvider is optional: [L] limited + reset timer.
type QuotaProvider interface {
	QuotaReset(s LiveSession, now time.Time) (at time.Time, exact bool, ok bool)
}

// WatchSource describes a path the daemon may fsnotify-watch.
type WatchSource struct {
	Path           string
	// StatusFieldDedupe: when set, only fire when this JSON field changes (Claude sessions).
	StatusFieldDedupe string
}

// WatchHinter is optional. Empty / nil → poll-only for that client.
type WatchHinter interface {
	WatchHints() []WatchSource
}

// ResumeArgver is optional: argv for workspace restore prefill (no Enter).
type ResumeArgver interface {
	ResumeArgv(sessionKey string) []string
}

// RetryPolicy describes whether auto-retry may inject a continue message.
type RetryPolicy struct {
	Enabled bool
	Message string // e.g. "Continue where you left off."
}

// RetryPolicier is optional. Missing or Enabled=false → no auto-retry.
type RetryPolicier interface {
	RetryPolicy() RetryPolicy
}
