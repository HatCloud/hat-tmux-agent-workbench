package main

// Claude error detection + auto-retry.
//
// Codex surfaces a stopped-on-error turn via its SQLite "Turn error:" log
// (resolveCodexStatus → "error"). Claude has no such status in its sessions
// JSON: when a turn dies on an API error (429/5xx/529 overloaded) Claude Code
// exhausts its own internal backoff, then writes a synthetic assistant record
// into the project session JSONL with error/apiErrorStatus/isApiErrorMessage
// and stops. We detect that terminal record here — mirroring the 429 "limited"
// probe in quota.go — and surface it as the "error" status ([E]).
//
// When auto-retry is enabled, a recoverable error (5xx server-side, incl. 529)
// drives a bounded retry: after an exponential backoff we `tmux send-keys` a
// continuation message into the agent pane. Backoff/attempt state lives in
// window options so it survives across sync passes and daemon restarts and
// needs no in-memory bookkeeping.

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"
)

// jitterFraction returns a pseudo-random value in [0,1) for backoff jitter.
func jitterFraction() float64 { return rand.Float64() }

// claudeRetryMessage is the continuation text injected into the agent pane to
// re-drive a turn that died on a recoverable API error.
const claudeRetryMessage = "Continue where you left off."

// autoRetryBackoff is the wait before each successive retry attempt (attempt 0
// waits 30s, attempt 1 waits 60s, …); attempts past the end reuse the last.
var autoRetryBackoff = []time.Duration{
	30 * time.Second,
	60 * time.Second,
	120 * time.Second,
	240 * time.Second,
	300 * time.Second,
}

// autoRetryJitterPct is the ±% randomness applied to each backoff so multiple
// windows hit by the same account-wide overload don't retry in lockstep.
const autoRetryJitterPct = 15

func retryBackoff(count int) time.Duration {
	if count < 0 {
		count = 0
	}
	if count >= len(autoRetryBackoff) {
		return autoRetryBackoff[len(autoRetryBackoff)-1]
	}
	return autoRetryBackoff[count]
}

// claudeTurnError describes the terminal API error a Claude turn died on.
type claudeTurnError struct {
	Type   string // the JSONL `error` field, e.g. "server_error", "rate_limit"
	Status int    // apiErrorStatus (HTTP code)
	At     time.Time
}

// Retryable reports whether auto-retry should attempt this error. Keyed on the
// JSONL `error` category, because transient failures often carry NO HTTP status
// (apiErrorStatus absent → Status==0): e.g. "Connection closed mid-response",
// "Server error mid-response", "socket connection closed", "operation timed out".
// Retryable (transient):
//   - server_error at any status — 500/529 overloaded and the status-less
//     connection/mid-response drops.
//   - unknown WITHOUT a status — socket-closed / timeout network blips.
//   - any explicit 5xx (defensive).
//
// Not retryable: 429 (→ "limited", handled separately), authentication_failed,
// invalid_request, model_not_found, max_output_tokens, and unknown WITH a 4xx
// status (402 billing / 400 bad request) — retrying can't fix those.
func (e claudeTurnError) Retryable() bool {
	if e.Status == 429 {
		return false
	}
	switch e.Type {
	case "server_error":
		return true
	case "unknown":
		return e.Status == 0 // socket closed / timeout; exclude 4xx billing/bad-request
	}
	return e.Status >= 500
}

// scanClaudeError walks JSONL records (oldest→newest within the scanned tail)
// and returns the terminal API error the latest turn ended on, if any. A later
// non-error assistant or user message supersedes an earlier error (a subsequent
// turn got through, or the user already retried), so the error is only returned
// when it is the last meaningful turn outcome. 429 rate-limit records are
// ignored here — they are the "limited" status, not a stop-on-error.
func scanClaudeError(lines [][]byte) (claudeTurnError, bool) {
	var (
		cur  claudeTurnError
		have bool
	)
	for _, line := range lines {
		var entry struct {
			Type           string `json:"type"`
			Timestamp      string `json:"timestamp"`
			Error          string `json:"error"`
			APIErrorStatus int    `json:"apiErrorStatus"`
			IsAPIError     bool   `json:"isApiErrorMessage"`
		}
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		if entry.Type != "assistant" && entry.Type != "user" {
			continue
		}
		isErr := entry.IsAPIError || (entry.Error != "" && entry.APIErrorStatus != 0)
		if isErr && entry.APIErrorStatus != 429 {
			at, err := time.Parse(time.RFC3339, entry.Timestamp)
			if err != nil {
				continue
			}
			cur = claudeTurnError{Type: entry.Error, Status: entry.APIErrorStatus, At: at}
			have = true
		} else {
			// A normal message (or a 429, which is "limited" not "error")
			// supersedes any earlier terminal error.
			have = false
		}
	}
	return cur, have
}

// claudeErrorFromJSONL reads the tail of a session JSONL and reports the
// terminal API error its latest turn stopped on, if any.
func claudeErrorFromJSONL(path string) (claudeTurnError, bool) {
	f, err := os.Open(path)
	if err != nil {
		return claudeTurnError{}, false
	}
	defer f.Close()
	scanner := tailScanner(f, 256<<10)
	var lines [][]byte
	for scanner.Scan() {
		lines = append(lines, append([]byte(nil), scanner.Bytes()...))
	}
	return scanClaudeError(lines)
}

// claudeErrorFromSession is the meta-addressed wrapper used by the sync loop.
func claudeErrorFromSession(meta claudeSessionMeta) (claudeTurnError, bool) {
	path := claudeSessionJSONLPath(meta)
	if path == "" {
		return claudeTurnError{}, false
	}
	return claudeErrorFromJSONL(path)
}

// ── Auto-retry scheduling ───────────────────────────────────────────────────

// Window options carrying per-window retry state (survive daemon restarts):
const (
	optErrorAt     = "@agent_error_at"          // unix sec of the active retryable error
	optErrorType   = "@agent_error_type"        // error class (server_error/…)
	optRetryCount  = "@agent_error_retry_count" // attempts made for the current error episode
	optRetryNextAt = "@agent_error_retry_next_at"
)

type errRetryAction int

const (
	retryNoop     errRetryAction = iota // do nothing this pass
	retryClear                          // settled clean state → reset counters
	retrySchedule                       // fresh error → set the first retry time
	retryFire                           // backoff elapsed → inject the retry now
)

// retryInput is the pure snapshot planErrorRetry decides on.
type retryInput struct {
	Enabled  bool      // auto-retry setting on
	HasError bool      // a retryable error is active for this window
	Busy     bool      // the agent turn is in progress (never inject mid-turn)
	ErrorAt  time.Time // when the active error was recorded
	Count    int       // retries already attempted this episode
	NextAt   time.Time // scheduled time for the next retry (zero = unscheduled)
	Max      int       // attempt cap
	Now      time.Time
}

type retryPlan struct {
	Action   errRetryAction
	NextAt   time.Time // for retrySchedule / retryFire
	NewCount int       // for retryFire
}

// planErrorRetry is the pure retry decision. jitter perturbs a backoff duration
// (identity in tests). Semantics:
//   - disabled → noop.
//   - no active error: settled (not busy) → clear counters; busy → noop (keep
//     counters across the in-progress retry).
//   - active error, cap reached → noop (stop, leave [E]).
//   - active error, unscheduled → schedule first retry at ErrorAt + backoff.
//   - active error, before NextAt → wait. Busy → noop (defensive).
//   - active error, due → fire: bump count; schedule the next unless the cap is
//     now reached.
func planErrorRetry(in retryInput, jitter func(time.Duration) time.Duration) retryPlan {
	if !in.Enabled {
		return retryPlan{Action: retryNoop}
	}
	if !in.HasError {
		if in.Busy {
			return retryPlan{Action: retryNoop}
		}
		return retryPlan{Action: retryClear}
	}
	if in.Count >= in.Max {
		return retryPlan{Action: retryNoop}
	}
	if in.NextAt.IsZero() {
		return retryPlan{Action: retrySchedule, NextAt: in.ErrorAt.Add(jitter(retryBackoff(in.Count)))}
	}
	if in.Now.Before(in.NextAt) {
		return retryPlan{Action: retryNoop}
	}
	if in.Busy {
		return retryPlan{Action: retryNoop}
	}
	next := in.Count + 1
	plan := retryPlan{Action: retryFire, NewCount: next}
	if next < in.Max {
		plan.NextAt = in.Now.Add(jitter(retryBackoff(next)))
	}
	return plan
}

// applyRetryJitter perturbs d by ±autoRetryJitterPct. rnd is a value in [0,1).
func applyRetryJitter(d time.Duration, rnd float64) time.Duration {
	if d <= 0 {
		return d
	}
	span := float64(d) * float64(autoRetryJitterPct) / 100.0
	delta := (rnd*2 - 1) * span
	out := time.Duration(math.Round(float64(d) + delta))
	if out < 0 {
		out = 0
	}
	return out
}

// ── Window-option IO for retry state ────────────────────────────────────────

func windowTimeOption(windowID, opt string) time.Time {
	v := strings.TrimSpace(tmuxWindowOption(windowID, opt))
	if v == "" {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

func windowIntOption(windowID, opt string) int {
	v := strings.TrimSpace(tmuxWindowOption(windowID, opt))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

func setWindowTimeOption(windowID, opt string, t time.Time) {
	setWindowOption(windowID, opt, strconv.FormatInt(t.Unix(), 10))
}

func setWindowIntOption(windowID, opt string, n int) {
	setWindowOption(windowID, opt, strconv.Itoa(n))
}

// clearRetryState drops the per-episode counters (kept the error stamps out —
// those are owned by agentWindowName each pass).
func clearRetryState(windowID string) {
	unsetWindowOption(windowID, optRetryCount)
	unsetWindowOption(windowID, optRetryNextAt)
}

// reconcileClaudeErrorRetry runs one auto-retry pass for a Claude window, using
// the @agent_error_* stamp agentWindowName wrote this pass. meta.Status gives
// the live busy/idle signal used to avoid injecting mid-turn.
func reconcileClaudeErrorRetry(windowID, aiPane string, meta claudeSessionMeta) {
	cfg := loadAppConfig()
	in := retryInput{
		Enabled:  autoRetrySetting(cfg),
		HasError: !windowTimeOption(windowID, optErrorAt).IsZero(),
		Busy:     isBusyStatus(meta.Status),
		ErrorAt:  windowTimeOption(windowID, optErrorAt),
		Count:    windowIntOption(windowID, optRetryCount),
		NextAt:   windowTimeOption(windowID, optRetryNextAt),
		Max:      autoRetryMaxSetting(cfg),
		Now:      time.Now(),
	}
	plan := planErrorRetry(in, func(d time.Duration) time.Duration {
		return applyRetryJitter(d, jitterFraction())
	})
	switch plan.Action {
	case retryClear:
		clearRetryState(windowID)
	case retrySchedule:
		setWindowTimeOption(windowID, optRetryNextAt, plan.NextAt)
	case retryFire:
		sendClaudeRetry(aiPane)
		setWindowIntOption(windowID, optRetryCount, plan.NewCount)
		if plan.NextAt.IsZero() {
			unsetWindowOption(windowID, optRetryNextAt)
		} else {
			setWindowTimeOption(windowID, optRetryNextAt, plan.NextAt)
		}
	}
}

// sendClaudeRetry injects the continuation message + Enter into the agent pane.
// Uses the bracketed-paste path (see pasteAndSubmit) so the submit isn't dropped
// as a newline on slower machines.
func sendClaudeRetry(aiPane string) {
	_ = pasteAndSubmit(aiPane, claudeRetryMessage, true)
}

func isBusyStatus(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s == "busy" || s == "shell"
}
