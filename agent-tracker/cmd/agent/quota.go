package main

// Quota orchestration: the timer "reset" trigger and the usage-limit dialog
// dismissal. The actual probing (Claude 429 text / statusline cache, Codex
// rollout rate_limits) lives in the agentclient adapters behind QuotaProvider;
// this file only routes windows to adapters and reads back the
// @agent_limit_reset_at stamps agentWindowName wrote.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/david/agent-tracker/internal/agentclient"
)

// quotaResetFireBuffer pads the probed reset instant: Claude texts are
// minute-precision and both clocks may skew slightly.
const quotaResetFireBuffer = 90 * time.Second

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

// ── entry point for the "reset" timer trigger ───────────────────────────────

// quotaResetFireAt resolves when the window's AI client usage window resets,
// plus a safety buffer, via the client adapter's QuotaProvider. fallback=true
// means the instant came from an estimate (Claude statusline cache) rather
// than an exact source; callers keep such timers eligible for an exact-stamp
// upgrade.
func quotaResetFireAt(windowID string) (at time.Time, fallback bool, err error) {
	acIdx := agentclient.BuildIndex()
	aiPane := agentAIPane(windowID, acIdx)
	if aiPane == "" {
		return time.Time{}, false, fmt.Errorf("no AI pane in window %s", windowID)
	}
	reg := agentclient.DefaultRegistry()
	live, ok := reg.DetectForPane(acIdx, panePID(aiPane), tmuxWindowOption(windowID, "@agent_client"))
	if !ok {
		return time.Time{}, false, fmt.Errorf("no live agent session in window %s", windowID)
	}
	qp, ok := reg.AdapterByID(live.Client).(agentclient.QuotaProvider)
	if !ok {
		return time.Time{}, false, fmt.Errorf("client %s exposes no usage-quota source", live.Client)
	}
	reset, exact, ok := qp.QuotaReset(live, time.Now())
	if !ok {
		return time.Time{}, false, fmt.Errorf("no pending usage-window reset known for %s", live.Client)
	}
	return reset.Add(quotaResetFireBuffer), !exact, nil
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
// Escape is sent ONLY when the usage-limit box is actually on screen AND no live
// agent (Claude/Codex/Grok/…) is mid-turn. A blind Escape mid-turn would abort
// an in-flight response; an Escape with no dialog present could wipe a draft.
func shouldDismissUsageDialog(anyAgentBusy, screenMatch bool) bool {
	if anyAgentBusy {
		return false
	}
	return screenMatch
}

// dismissUsageLimitDialog clears a Claude usage-limit dialog that would otherwise
// swallow the keys a timer injects. Busy guard covers any client via Registry.
func dismissUsageLimitDialog(paneID string, acIdx *agentclient.Index) {
	if acIdx == nil {
		acIdx = agentclient.BuildIndex()
	}
	anyBusy := false
	if live, ok := agentclient.DefaultRegistry().DetectForPane(acIdx, panePID(paneID), ""); ok {
		anyBusy = strings.EqualFold(live.Status, agentclient.StatusBusy)
	}
	screenMatch := false
	if out, err := runTmuxOutput("capture-pane", "-p", "-t", paneID); err == nil {
		screenMatch = screenShowsUsageLimit(out)
	}
	if !shouldDismissUsageDialog(anyBusy, screenMatch) {
		return
	}
	_ = runTmux("send-keys", "-t", paneID, "Escape")
	time.Sleep(500 * time.Millisecond)
}
