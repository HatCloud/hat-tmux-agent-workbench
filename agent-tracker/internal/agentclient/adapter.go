package agentclient

import (
	"context"
	"errors"
	"time"
)

// FirstPrompter is optional: Window Nav "p" prompt preview.
type FirstPrompter interface {
	FirstPrompt(s LiveSession) string
}

// SessionNameSource distinguishes a user-controlled native session name from
// an agent-generated default title. The orchestrator uses this distinction to
// keep the precedence stable: native user name > tmux manual name > tracker
// generated name > agent default title.
type SessionNameSource string

const (
	SessionNameNone      SessionNameSource = "none"
	SessionNameUser      SessionNameSource = "user"
	SessionNameGenerated SessionNameSource = "generated"
	SessionNameUnknown   SessionNameSource = "unknown"
)

// SessionNameState is the adapter-owned view of native naming support.
// Writable=false means the adapter can read a name/default title but has no
// safe external write path; callers may fall back to a tracker-owned alias.
type SessionNameState struct {
	Value    string
	Source   SessionNameSource
	Writable bool
}

var (
	ErrSessionNameUnsupported = errors.New("agent session naming is unsupported")
	ErrSessionAlreadyNamed    = errors.New("agent session already has a user name")
)

// SessionNamer keeps provider-specific storage and protocols in the adapter;
// orchestration only consumes this normalized capability. Adapters without a
// safe native write path still implement it and report Writable=false /
// ErrSessionNameUnsupported, making naming research an explicit integration
// requirement instead of an easy-to-forget optional feature.
type SessionNamer interface {
	SessionName(s LiveSession) (SessionNameState, error)
	SetSessionName(ctx context.Context, s LiveSession, name string) error
}

// Adapter detects a live interactive agent session under a pane process tree
// and must explicitly describe that client's native naming capability.
type Adapter interface {
	SessionNamer
	ID() string
	// Detect returns a session only on strict success (live process + consistent sidecars).
	Detect(idx *Index, panePID int) (LiveSession, bool)
}

// QuotaProvider is optional: [L] limited + reset timer.
type QuotaProvider interface {
	QuotaReset(s LiveSession, now time.Time) (at time.Time, exact bool, ok bool)
}

// WatchSource describes a path the daemon may fsnotify-watch.
type WatchSource struct {
	Path string
	// StatusFieldDedupe: when set, only fire when this JSON field changes (Claude sessions).
	StatusFieldDedupe string
}

// WatchHinter is optional. Empty / nil → poll-only for that client.
type WatchHinter interface {
	WatchHints() []WatchSource
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
