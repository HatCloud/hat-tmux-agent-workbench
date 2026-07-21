package main

// Auto-retry engine for turns that stopped on a recoverable API error.
// Detection lives in the agentclient adapters (Claude: session-JSONL terminal
// record; Codex: SQLite "Turn error:") and surfaces as LiveSession.Error; the
// window-naming pass stamps @agent_error_at/type for retryable errors. This
// engine turns those stamps into a bounded retry: after an exponential backoff
// it injects the adapter's RetryPolicy continuation message into the agent
// pane. Backoff/attempt state lives in window options so it survives across
// sync passes and daemon restarts and needs no in-memory bookkeeping.

import (
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/david/agent-tracker/internal/agentclient"
)

// jitterFraction returns a pseudo-random value in [0,1) for backoff jitter.
func jitterFraction() float64 { return rand.Float64() }

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

// reconcileErrorRetry runs one auto-retry pass for a window, using the
// @agent_error_* stamp agentWindowName wrote this pass. The adapter's
// RetryPolicy gates participation (missing / disabled → engine off for that
// client) and supplies the continuation message; live.Status gives the
// busy/idle signal used to avoid injecting mid-turn.
func reconcileErrorRetry(windowID, aiPane string, live *agentclient.LiveSession) {
	if live == nil {
		return
	}
	policy := agentclient.RetryPolicy{}
	if rp, ok := agentclient.DefaultRegistry().AdapterByID(live.Client).(agentclient.RetryPolicier); ok {
		policy = rp.RetryPolicy()
	}
	cfg := loadAppConfig()
	in := retryInput{
		Enabled:  policy.Enabled && autoRetrySetting(cfg),
		HasError: !windowTimeOption(windowID, optErrorAt).IsZero(),
		Busy:     isBusyStatus(live.Status),
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
		// Bracketed-paste path (see pasteAndSubmit) so the submit isn't dropped
		// as a newline on slower machines.
		_ = pasteAndSubmit(aiPane, policy.Message, true)
		setWindowIntOption(windowID, optRetryCount, plan.NewCount)
		if plan.NextAt.IsZero() {
			unsetWindowOption(windowID, optRetryNextAt)
		} else {
			setWindowTimeOption(windowID, optRetryNextAt, plan.NextAt)
		}
	}
}

// isBusyStatus reports a turn in progress. "shell" is NOT busy: the turn has
// ended (input is accepted), so injecting a retry there is equivalent to the
// user typing — and treating it as busy would block retries forever under a
// long-lived background job.
func isBusyStatus(status string) bool {
	return strings.ToLower(strings.TrimSpace(status)) == "busy"
}
