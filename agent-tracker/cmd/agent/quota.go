package main

// Usage-quota probing: resolve when the AI client's usage window resets, from
// on-disk artifacts only (no network).
//
//   - Codex: every request appends a token_count event to the session rollout
//     JSONL carrying payload.rate_limits.{primary,secondary}.resets_at — an
//     absolute epoch second for the 5h / weekly windows.
//   - Claude Code: no proactive on-disk source. Hitting the limit inserts a
//     synthetic assistant message (error="rate_limit", apiErrorStatus=429) into
//     the project session JSONL whose text ends in a human-readable
//     "… resets 12:40am (America/Los_Angeles)"; we parse that relative to the
//     event timestamp. Weekly-limit texts with an explicit date don't match the
//     pattern and are treated as "no reset time known".

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/david/agent-tracker/internal/paths"
)

// quotaResetFireBuffer pads the probed reset instant: Claude texts are
// minute-precision and both clocks may skew slightly.
const quotaResetFireBuffer = 90 * time.Second

// quotaExhaustedPercent marks a rate window as blocking when used_percent
// reaches it; every blocking window must reset before work can resume.
const quotaExhaustedPercent = 95.0

// ── Claude: parse the 429 message text ──────────────────────────────────────

var reClaudeReset = regexp.MustCompile(
	`(?i)\bresets?\b(?:\s+at)?\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)?(?:\s*\(([^)]+)\))?`)

// parseClaudeResetText resolves the absolute reset instant from a 429 message
// text and the moment it was written: the next occurrence of the given
// wall-clock time (in the given IANA zone, defaulting to local) strictly after
// the event.
func parseClaudeResetText(text string, event time.Time) (time.Time, bool) {
	m := reClaudeReset.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	hour, _ := strconv.Atoi(m[1])
	minute := 0
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
	}
	switch strings.ToLower(m[3]) {
	case "am":
		if hour == 12 {
			hour = 0
		}
	case "pm":
		if hour != 12 {
			hour += 12
		}
	}
	if hour > 23 || minute > 59 {
		return time.Time{}, false
	}
	loc := time.Local
	if m[4] != "" {
		if l, err := time.LoadLocation(strings.TrimSpace(m[4])); err == nil {
			loc = l
		}
	}
	ev := event.In(loc)
	reset := time.Date(ev.Year(), ev.Month(), ev.Day(), hour, minute, 0, 0, loc)
	if !reset.After(ev) {
		reset = reset.Add(24 * time.Hour)
	}
	return reset, true
}

// tailScanner positions a Scanner ~tailBytes before EOF with the first (likely
// partial) line consumed, mirroring liveModelFromSession's bounded tail read.
func tailScanner(f *os.File, tailBytes int64) *bufio.Scanner {
	start := int64(0)
	if info, err := f.Stat(); err == nil && info.Size() > tailBytes {
		start = info.Size() - tailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		start = 0
		_, _ = f.Seek(0, io.SeekStart)
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 4<<20)
	if start > 0 {
		scanner.Scan()
	}
	return scanner
}

// claudeLimitResetFromJSONL reports whether the session's latest turn ended on
// an unresolved usage-limit 429, and when that limit lifts. Any newer user or
// assistant message supersedes an earlier 429 (a later turn got through), and a
// reset instant already in the past means the window has reopened.
func claudeLimitResetFromJSONL(path string, now time.Time) (time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	scanner := tailScanner(f, 256<<10)
	var (
		reset time.Time
		have  bool
	)
	for scanner.Scan() {
		var entry struct {
			Type           string `json:"type"`
			Timestamp      string `json:"timestamp"`
			Error          string `json:"error"`
			APIErrorStatus int    `json:"apiErrorStatus"`
			Message        struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		if entry.Type != "assistant" && entry.Type != "user" {
			continue
		}
		if entry.Error == "rate_limit" && entry.APIErrorStatus == 429 {
			have = false
			ev, err := time.Parse(time.RFC3339, entry.Timestamp)
			if err != nil {
				continue
			}
			if at, ok := parseClaudeResetText(textFromJSONContent(entry.Message.Content), ev); ok {
				reset, have = at, true
			}
		} else {
			have = false
		}
	}
	if !have || !reset.After(now) {
		return time.Time{}, false
	}
	return reset, true
}

// claudeLimitResetFromSession is the meta-addressed wrapper used by the sync loop.
func claudeLimitResetFromSession(meta claudeSessionMeta, now time.Time) (time.Time, bool) {
	path := claudeSessionJSONLPath(meta)
	if path == "" {
		return time.Time{}, false
	}
	return claudeLimitResetFromJSONL(path, now)
}

// windowQuotaLimitedUntil reads the @agent_limit_reset_at stamp that
// agentWindowName wrote earlier in the same sync pass, so downstream consumers
// (task reconcile) reuse the probe instead of re-reading the JSONL tail.
func windowQuotaLimitedUntil(windowID string) (time.Time, bool) {
	v := strings.TrimSpace(tmuxWindowOption(windowID, "@agent_limit_reset_at"))
	if v == "" {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	at := time.Unix(sec, 0)
	if !at.After(time.Now()) {
		return time.Time{}, false
	}
	return at, true
}

// anyWindowQuotaLimitedUntil returns the furthest future @agent_limit_reset_at
// across all windows — the account-wide "limited until" signal. A subscription
// quota is not per-window, so any window hitting the limit wakes every dormant
// reset timer.
func anyWindowQuotaLimitedUntil() time.Time {
	out, err := runTmuxOutput("list-windows", "-a", "-F", "#{@agent_limit_reset_at}")
	if err != nil {
		return time.Time{}
	}
	now := time.Now()
	var best time.Time
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		sec, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
		if err != nil {
			continue
		}
		if at := time.Unix(sec, 0); at.After(now) && at.After(best) {
			best = at
		}
	}
	return best
}

// ── Codex: rate_limits snapshot from the rollout JSONL ──────────────────────

type codexRateWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int64   `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

type codexRateLimits struct {
	Primary   *codexRateWindow `json:"primary"`
	Secondary *codexRateWindow `json:"secondary"`
}

// codexRateLimitsFromRollout returns the latest token_count rate_limits
// snapshot in a rollout JSONL (bounded tail read).
func codexRateLimitsFromRollout(path string) (codexRateLimits, bool) {
	if strings.TrimSpace(path) == "" {
		return codexRateLimits{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return codexRateLimits{}, false
	}
	defer f.Close()
	scanner := tailScanner(f, 256<<10)
	var (
		rl    codexRateLimits
		found bool
	)
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				Type       string           `json:"type"`
				RateLimits *codexRateLimits `json:"rate_limits"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		if entry.Type == "event_msg" && entry.Payload.Type == "token_count" &&
			entry.Payload.RateLimits != nil {
			rl, found = *entry.Payload.RateLimits, true
		}
	}
	return rl, found
}

// latestCodexRolloutPath finds the most recently written rollout under
// ~/.codex/sessions — quota is account-wide, so any fresh snapshot works.
func latestCodexRolloutPath() string {
	root := filepath.Join(homeDir(), ".codex", "sessions")
	var (
		newest    string
		newestMod time.Time
	)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(newestMod) {
			newestMod, newest = info.ModTime(), path
		}
		return nil
	})
	return newest
}

// pickCodexReset chooses which resets_at to wait for: every exhausted window
// (>= quotaExhaustedPercent) must reopen before work resumes, so take the
// latest among them; when none is exhausted fall back to the shortest window's
// boundary (the next 5h reset). Instants already in the past mean that window
// has reset and never qualify.
func pickCodexReset(rl codexRateLimits, now time.Time) (time.Time, bool) {
	var windows []codexRateWindow
	for _, w := range []*codexRateWindow{rl.Primary, rl.Secondary} {
		if w != nil && w.ResetsAt > 0 && time.Unix(w.ResetsAt, 0).After(now) {
			windows = append(windows, *w)
		}
	}
	if len(windows) == 0 {
		return time.Time{}, false
	}
	var exhausted time.Time
	for _, w := range windows {
		if w.UsedPercent >= quotaExhaustedPercent {
			if at := time.Unix(w.ResetsAt, 0); at.After(exhausted) {
				exhausted = at
			}
		}
	}
	if !exhausted.IsZero() {
		return exhausted, true
	}
	best := windows[0]
	for _, w := range windows[1:] {
		if w.WindowMinutes < best.WindowMinutes {
			best = w
		}
	}
	return time.Unix(best.ResetsAt, 0), true
}

// ── Claude statusline rate-limits cache (fallback estimate) ─────────────────

// claudeRateLimitsCachePath is written by cc-statusline-official on every
// status-line render: the rate_limits object Claude Code injects into the
// statusline payload (first-party five_hour/seven_day used_percentage +
// resets_at epochs). Cached, so it lags a little behind the true boundary —
// good enough as the fallback fire time until a 429 stamp gives the exact one.
func claudeRateLimitsCachePath() string {
	return filepath.Join(paths.StateDir(), "claude-rate-limits.json")
}

// claudeFallbackResetAt reads the statusline cache and picks the reset instant
// with the same exhausted-window rules as codex (5h ↔ primary, 7d ↔ secondary).
func claudeFallbackResetAt(now time.Time) (time.Time, bool) {
	data, err := os.ReadFile(claudeRateLimitsCachePath())
	if err != nil {
		return time.Time{}, false
	}
	var rl struct {
		FiveHour struct {
			UsedPercentage float64 `json:"used_percentage"`
			ResetsAt       int64   `json:"resets_at"`
		} `json:"five_hour"`
		SevenDay struct {
			UsedPercentage float64 `json:"used_percentage"`
			ResetsAt       int64   `json:"resets_at"`
		} `json:"seven_day"`
	}
	if json.Unmarshal(data, &rl) != nil {
		return time.Time{}, false
	}
	var conv codexRateLimits
	if rl.FiveHour.ResetsAt > 0 {
		conv.Primary = &codexRateWindow{
			UsedPercent: rl.FiveHour.UsedPercentage, WindowMinutes: 300, ResetsAt: rl.FiveHour.ResetsAt}
	}
	if rl.SevenDay.ResetsAt > 0 {
		conv.Secondary = &codexRateWindow{
			UsedPercent: rl.SevenDay.UsedPercentage, WindowMinutes: 10080, ResetsAt: rl.SevenDay.ResetsAt}
	}
	return pickCodexReset(conv, now)
}

// ── entry point for the "reset" timer trigger ───────────────────────────────

// quotaResetFireAt resolves when the window's AI client usage window resets,
// plus a safety buffer. fallback=true means the instant came from the cached
// statusline estimate rather than an exact source; callers keep such timers
// eligible for an exact-stamp upgrade.
func quotaResetFireAt(windowID string) (at time.Time, fallback bool, err error) {
	ci := buildClaudeIndex()
	aiPane := agentAIPane(windowID, &ci)
	if aiPane == "" {
		return time.Time{}, false, fmt.Errorf("no AI pane in window %s", windowID)
	}
	now := time.Now()
	if meta, _, ok := ci.sessionForPanePID(panePID(aiPane)); ok {
		if reset, ok := claudeLimitResetFromSession(meta, now); ok {
			return reset.Add(quotaResetFireBuffer), false, nil
		}
		if fb, ok := claudeFallbackResetAt(now); ok {
			return fb.Add(quotaResetFireBuffer), true, nil
		}
		return time.Time{}, false, fmt.Errorf(
			"no pending usage-limit reset in the Claude session and no statusline rate-limits cache")
	}
	if thread, _, ok := codexThreadForPane(aiPane, &ci); ok {
		path := thread.RolloutPath
		if path == "" {
			path = latestCodexRolloutPath()
		}
		if rl, ok := codexRateLimitsFromRollout(path); ok {
			if reset, ok := pickCodexReset(rl, now); ok {
				return reset.Add(quotaResetFireBuffer), false, nil
			}
		}
		return time.Time{}, false, fmt.Errorf("no usable rate_limits snapshot in the codex rollout")
	}
	return time.Time{}, false, fmt.Errorf("no live claude/codex session in window %s", windowID)
}

// ── usage-limit dialog dismissal (timer fire path) ──────────────────────────

// reUsageLimitScreen matches the Claude usage-limit / quota dialog as scraped
// from the pane. Kept a targeted screen match (never a blanket "there's a word
// on screen"), but broadened to the phrasings Claude uses across session /
// weekly / opus limits and the reset-countdown box, since the earlier narrow
// pattern missed the box the user actually hit.
var reUsageLimitScreen = regexp.MustCompile(
	`(?i)` +
		`hit your (session|usage|weekly|5-hour|five-hour|opus) limit` +
		`|usage limit reached` +
		`|reached your (usage|session|weekly) limit` +
		`|approaching your (usage|session|weekly) limit` +
		`|(usage|session|weekly|opus) limit (will reset|resets)` +
		`|your limit (will reset|resets)` +
		`|resets? at \d` +
		`|try again (later|after)`)

func screenShowsUsageLimit(screen string) bool {
	return reUsageLimitScreen.MatchString(screen)
}

// shouldDismissUsageDialog is the pure safety gate for the timer-fire Escape.
// Escape is sent ONLY when the usage-limit box is actually on screen AND neither
// the pane's Claude nor Codex session is actively generating. A blind Escape
// mid-turn would abort an in-flight response; an Escape with no dialog present
// could wipe a user's unsent draft or disrupt an asking/waiting prompt — so both
// conditions must hold. This is the only safety gate for timer firing.
func shouldDismissUsageDialog(claudeBusy, codexBusy, screenMatch bool) bool {
	if claudeBusy || codexBusy {
		return false
	}
	return screenMatch
}

// dismissUsageLimitDialog clears a Claude usage-limit dialog that would otherwise
// swallow the keys a timer injects. It sends Escape only when the dialog is
// detected on screen and neither the Claude nor the Codex session in the pane is
// busy (see shouldDismissUsageDialog) — never a blind Escape into an in-flight
// turn or an idle prompt that may hold a draft.
func dismissUsageLimitDialog(paneID string, ci *claudeIndex) {
	if ci == nil {
		built := buildClaudeIndex()
		ci = &built
	}
	claudeBusy := false
	if meta, _, ok := ci.sessionForPanePID(panePID(paneID)); ok {
		claudeBusy = strings.EqualFold(meta.Status, "busy")
	}
	// Codex windows never populate byPID, so the Claude guard alone would let a
	// blind Escape land mid-generation on Codex — guard it explicitly too.
	codexBusy := false
	if meta, _, ok := codexThreadForPane(paneID, ci); ok {
		codexBusy = strings.EqualFold(meta.Status, "busy")
	}
	screenMatch := false
	if out, err := runTmuxOutput("capture-pane", "-p", "-t", paneID); err == nil {
		screenMatch = screenShowsUsageLimit(out)
	}
	if !shouldDismissUsageDialog(claudeBusy, codexBusy, screenMatch) {
		return
	}
	_ = runTmux("send-keys", "-t", paneID, "Escape")
	time.Sleep(500 * time.Millisecond)
}
