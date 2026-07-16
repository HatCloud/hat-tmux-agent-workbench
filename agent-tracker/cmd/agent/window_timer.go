package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/david/agent-tracker/internal/paths"
)

var reUTCOffset = regexp.MustCompile(`^(?i:(?:UTC|GMT))?([+-])(\d{1,2})(?::?(\d{2}))?$`)

func windowTimerNow() time.Time {
	return time.Now().In(windowTimerLocation())
}

func formatWindowTimerTime(t time.Time, layout string) string {
	return formatWindowTimerTimeIn(t, layout, windowTimerLocation())
}

func formatWindowTimerTimeIn(t time.Time, layout string, loc *time.Location) string {
	return t.In(loc).Format(layout)
}

func windowTimerLocation() *time.Location {
	loc, err := parseTimerTimezone(timerTimezoneSetting(loadAppConfig()))
	if err != nil {
		return time.FixedZone("UTC+8", 8*60*60)
	}
	return loc
}

func parseTimerTimezone(value string) (*time.Location, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("timezone cannot be empty")
	}
	if strings.EqualFold(value, "auto") {
		return time.Local, nil
	}
	if m := reUTCOffset.FindStringSubmatch(value); m != nil {
		hour, _ := strconv.Atoi(m[2])
		minute := 0
		if m[3] != "" {
			minute, _ = strconv.Atoi(m[3])
		}
		if hour > 14 || minute > 59 || (hour == 14 && minute != 0) {
			return nil, fmt.Errorf("UTC offset must be between -14:00 and +14:00")
		}
		offset := hour*60*60 + minute*60
		if m[1] == "-" {
			offset = -offset
		}
		name := fmt.Sprintf("UTC%s%02d:%02d", m[1], hour, minute)
		return time.FixedZone(name, offset), nil
	}
	loc, err := time.LoadLocation(value)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: use auto, an IANA name (Asia/Shanghai), or UTC offset (UTC+8)", value)
	}
	return loc, nil
}

func setTimerTimezone(value string) error {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "auto") {
		value = "auto"
	}
	loc, err := parseTimerTimezone(value)
	if err != nil {
		return err
	}
	if err := updateAppConfig(func(cfg *appConfig) {
		if value == "UTC+8" {
			cfg.TimerTimezone = ""
		} else {
			cfg.TimerTimezone = value
		}
	}); err != nil {
		return err
	}

	release := acquireTimerLock()
	defer release()
	timers := loadWindowTimers()
	dirty := false
	now := time.Now().In(loc)
	for _, timer := range timers {
		if timer.Enabled && timer.TriggerMode == windowTimerTriggerTime {
			timer.NextFireAt = computeNextFireAtFrom(timer, now, loc)
			dirty = true
		}
	}
	if dirty {
		return saveWindowTimers(timers)
	}
	return nil
}

// ── types ─────────────────────────────────────────────────────────────────────

type windowTimerTriggerMode string

const (
	windowTimerTriggerDelay windowTimerTriggerMode = "delay" // relative duration from now
	windowTimerTriggerTime  windowTimerTriggerMode = "time"  // specific HH:MM of day
	windowTimerTriggerQuota windowTimerTriggerMode = "quota" // when the AI client's usage quota resets
)

type windowTimerLoopMode string

const (
	windowTimerLoopNone     windowTimerLoopMode = "none"
	windowTimerLoopInterval windowTimerLoopMode = "interval" // repeat every N seconds (delay trigger)
	windowTimerLoopDaily    windowTimerLoopMode = "daily"    // repeat at same HH:MM each day (time trigger)
	windowTimerLoopQuota    windowTimerLoopMode = "quota"    // re-arm at every quota reset (quota trigger)
)

type windowTimer struct {
	ID              string                 `json:"id"`
	WindowID        string                 `json:"window_id"`
	Content         string                 `json:"content"`
	SendEnter       bool                   `json:"send_enter"`
	TriggerMode     windowTimerTriggerMode `json:"trigger_mode"`
	TriggerDelaySec int64                  `json:"trigger_delay_sec,omitempty"` // seconds, delay mode
	TriggerTime     string                 `json:"trigger_time,omitempty"`      // "HH:MM", time mode
	LoopMode        windowTimerLoopMode    `json:"loop_mode"`
	LoopIntervalSec int64                  `json:"loop_interval_sec,omitempty"` // seconds, interval loop
	MaxExecutions   int                    `json:"max_executions"`              // 0 = unlimited
	ExecutionCount  int                    `json:"execution_count"`
	Enabled         bool                   `json:"enabled"`
	NextFireAt      time.Time              `json:"next_fire_at"`
	CreatedAt       time.Time              `json:"created_at"`
	DeleteOnDone    bool                   `json:"delete_on_done,omitempty"` // remove the record after the final execution
	QuotaFallback   bool                   `json:"quota_fallback,omitempty"` // NextFireAt came from the statusline estimate; upgrade to the exact 429 stamp when one appears
}

type windowTimerStore struct {
	// ServerPID records which tmux server's window-id space the timers belong
	// to. Window ids (@N) are only unique within one server lifetime: after a
	// server restart (crash / workspace restore) new windows recycle old ids,
	// so timers from a previous server must be invalidated wholesale or a new
	// window inherits — and gets injected with — another window's timers.
	ServerPID int            `json:"server_pid,omitempty"`
	Timers    []*windowTimer `json:"timers"`
}

// ── persistence ───────────────────────────────────────────────────────────────

func loadWindowTimerStore() *windowTimerStore {
	var store windowTimerStore
	data, err := os.ReadFile(paths.TimersFile())
	if err != nil {
		return &store
	}
	_ = json.Unmarshal(data, &store)
	return &store
}

func loadWindowTimers() []*windowTimer {
	return loadWindowTimerStore().Timers
}

func saveWindowTimerStore(store *windowTimerStore) error {
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	path := paths.TimersFile()
	if err := os.MkdirAll(strings.TrimSuffix(path, "/"+lastPathElem(path)), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// saveWindowTimers persists timers, preserving the store's ownership stamp.
func saveWindowTimers(timers []*windowTimer) error {
	store := loadWindowTimerStore()
	store.Timers = timers
	return saveWindowTimerStore(store)
}

// tmuxServerPID identifies the current tmux server (0 when unreachable).
func tmuxServerPID() int {
	out, err := runTmuxOutput("display-message", "-p", "#{pid}")
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0
	}
	return pid
}

// reconcileTimerOwnership drops timers that can no longer belong to any live
// window and reports whether the store changed. Two layers:
//   - serverPID differs from the stamped one → the id space was reset (tmux
//     server restarted); every stored timer references a dead window whose id a
//     NEW window may recycle, so all of them are invalidated.
//   - same server → timers whose window is gone are dropped (a closed window's
//     timer can never fire again; the History library keeps the template).
//
// serverPID==0 (tmux unreachable for the pid probe) skips the wipe/stamp; an
// empty live set is untrustworthy (any session has ≥1 window) and skips the
// orphan drop.
func reconcileTimerOwnership(store *windowTimerStore, serverPID int, live map[string]bool) bool {
	changed := false
	if serverPID != 0 && store.ServerPID != serverPID {
		if store.ServerPID != 0 && len(store.Timers) > 0 {
			store.Timers = nil
		}
		store.ServerPID = serverPID
		changed = true
	}
	if len(live) == 0 {
		return changed
	}
	kept := store.Timers[:0]
	for _, t := range store.Timers {
		if live[t.WindowID] {
			kept = append(kept, t)
		} else {
			changed = true
		}
	}
	store.Timers = kept
	return changed
}

func lastPathElem(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}

// ── history ─────────────────────────────────────────────────────────────────

// windowTimerHistoryEntry records a timer content (+ its trigger config) that was
// used in a window. Persisted independently of active timers so it survives timer
// deletion. Dedup key is the full (content, trigger, loop, max, sendEnter) combo.
type windowTimerHistoryEntry struct {
	WindowID   string    `json:"window_id"`
	Content    string    `json:"content"`
	Trigger    string    `json:"trigger"`
	Loop       string    `json:"loop"`
	Max        string    `json:"max"`
	SendEnter  bool      `json:"send_enter"`
	LastUsedAt time.Time `json:"last_used_at"`
	UseCount   int       `json:"use_count"`
}

type windowTimerHistoryStore struct {
	Entries []*windowTimerHistoryEntry `json:"entries"`
}

func loadTimerHistory() []*windowTimerHistoryEntry {
	data, err := os.ReadFile(paths.TimerHistoryFile())
	if err != nil {
		return nil
	}
	var store windowTimerHistoryStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil
	}
	return store.Entries
}

func saveTimerHistory(entries []*windowTimerHistoryEntry) error {
	store := windowTimerHistoryStore{Entries: entries}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	path := paths.TimerHistoryFile()
	if err := os.MkdirAll(strings.TrimSuffix(path, "/"+lastPathElem(path)), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func sameHistoryKey(e *windowTimerHistoryEntry, windowID, content, trigger, loop, max string, sendEnter bool) bool {
	return e.WindowID == windowID && e.Content == content && e.Trigger == trigger &&
		e.Loop == loop && e.Max == max && e.SendEnter == sendEnter
}

// upsertHistory returns entries with the (full-combo) entry inserted or its
// LastUsedAt/UseCount refreshed. Pure function for testability.
func upsertHistory(entries []*windowTimerHistoryEntry, windowID, content, trigger, loop, max string, sendEnter bool, now time.Time) []*windowTimerHistoryEntry {
	for _, e := range entries {
		if sameHistoryKey(e, windowID, content, trigger, loop, max, sendEnter) {
			e.LastUsedAt = now
			e.UseCount++
			return entries
		}
	}
	return append(entries, &windowTimerHistoryEntry{
		WindowID:   windowID,
		Content:    content,
		Trigger:    trigger,
		Loop:       loop,
		Max:        max,
		SendEnter:  sendEnter,
		LastUsedAt: now,
		UseCount:   1,
	})
}

// recordTimerHistory upserts a used timer content into per-window history.
func recordTimerHistory(windowID, content, trigger, loop, max string, sendEnter bool) {
	if strings.TrimSpace(content) == "" {
		return
	}
	release := acquireTimerHistoryLock()
	defer release()
	entries := upsertHistory(loadTimerHistory(), windowID, content, trigger, loop, max, sendEnter, windowTimerNow())
	_ = saveTimerHistory(entries)
}

// acquireTimerHistoryLock guards the history file's load-modify-write. A lock
// of its own (not acquireTimerLock) because addWindowTimer records history
// while already holding the timer lock — the mkdir lock is not reentrant.
func acquireTimerHistoryLock() func() {
	dir := filepath.Join(filepath.Dir(paths.TimerHistoryFile()), ".timer-history.lock.d")
	release, err := acquireMkdirLock(dir, configLockStale)
	if err != nil {
		return func() {}
	}
	return release
}

// timerHistoryAll returns every window's history merged into one list, deduped
// by the (content, trigger, loop, max, sendEnter) combo (most recent survives),
// most-recently-used first. The history pane is a global template library:
// window ids churn across tmux server restarts, so per-window scoping would
// silently orphan old entries.
func timerHistoryAll() []*windowTimerHistoryEntry {
	byKey := map[string]*windowTimerHistoryEntry{}
	for _, e := range loadTimerHistory() {
		k := strings.Join([]string{e.Content, e.Trigger, e.Loop, e.Max, strconv.FormatBool(e.SendEnter)}, "\x00")
		if cur, ok := byKey[k]; !ok || e.LastUsedAt.After(cur.LastUsedAt) {
			byKey[k] = e
		}
	}
	out := make([]*windowTimerHistoryEntry, 0, len(byKey))
	for _, e := range byKey {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastUsedAt.After(out[j].LastUsedAt)
	})
	return out
}

// deleteTimerHistoryCombo removes a combo from every window's history — the
// panel shows the deduped global list, so a delete must not let another
// window's copy of the same combo resurface.
func deleteTimerHistoryCombo(content, trigger, loop, max string, sendEnter bool) error {
	release := acquireTimerHistoryLock()
	defer release()
	entries := loadTimerHistory()
	kept := entries[:0]
	for _, e := range entries {
		if e.Content == content && e.Trigger == trigger && e.Loop == loop &&
			e.Max == max && e.SendEnter == sendEnter {
			continue
		}
		kept = append(kept, e)
	}
	return saveTimerHistory(kept)
}

func newTimerID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

// ── parsing ───────────────────────────────────────────────────────────────────

// reDurationWhole validates a full compound duration (one or more unit tokens),
// reDurationToken extracts each (number, unit) pair, e.g. "3h20m" → 3h + 20m.
var reDurationWhole = regexp.MustCompile(`^(\d+(s|m|h|d))+$`)
var reDurationToken = regexp.MustCompile(`(\d+)(s|m|h|d)`)
var reTimeHHMM = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)

// parseDurationSeconds parses a compound duration like "3h20m", "1h30m15s",
// "5m", "1d" into total seconds. Returns ok=false on invalid/empty/non-positive.
func parseDurationSeconds(s string) (int64, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || !reDurationWhole.MatchString(s) {
		return 0, false
	}
	var total int64
	for _, m := range reDurationToken.FindAllStringSubmatch(s, -1) {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		switch m[2] {
		case "s":
			total += n
		case "m":
			total += n * 60
		case "h":
			total += n * 3600
		case "d":
			total += n * 86400
		}
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}

// parseTrigger parses a trigger string into mode + values.
// Accepts "HH:MM" for time mode, compound durations like "5m", "1h", "3h20m",
// "30s" for delay mode (a bare integer is treated as minutes), or
// "reset"/"quota" to fire when the window's AI client usage quota resets.
func parseTrigger(s string) (mode windowTimerTriggerMode, delaySec int64, timeStr string, err error) {
	s = strings.TrimSpace(s)
	if l := strings.ToLower(s); l == "reset" || l == "quota" {
		return windowTimerTriggerQuota, 0, "", nil
	}
	// Bare integer → minutes (e.g. "5" → 5m)
	if n, err2 := strconv.ParseInt(s, 10, 64); err2 == nil && n > 0 {
		return windowTimerTriggerDelay, n * 60, "", nil
	}
	if m := reTimeHHMM.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		if h > 23 || min > 59 {
			return "", 0, "", fmt.Errorf("invalid time %q (hour 0-23, minute 0-59)", s)
		}
		return windowTimerTriggerTime, 0, fmt.Sprintf("%02d:%02d", h, min), nil
	}
	if sec, ok := parseDurationSeconds(s); ok {
		return windowTimerTriggerDelay, sec, "", nil
	}
	return "", 0, "", fmt.Errorf("invalid trigger %q: use HH:MM, duration (5m, 1h, 3h20m, 30s) or reset", s)
}

// parseLoopInterval parses a loop interval string.
// For interval loops: same duration syntax as trigger.
// For daily loops: accepts "daily" or empty.
func parseLoopInterval(s string, mode windowTimerTriggerMode) (loopMode windowTimerLoopMode, loopSec int64, err error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "none" || s == "no" {
		return windowTimerLoopNone, 0, nil
	}
	if mode == windowTimerTriggerQuota {
		if s == "reset" || s == "quota" || s == "r" {
			return windowTimerLoopQuota, 0, nil
		}
		return "", 0, fmt.Errorf("for reset triggers, loop must be 'reset' (each quota reset) or empty")
	}
	if mode == windowTimerTriggerTime {
		if s == "daily" || s == "d" {
			return windowTimerLoopDaily, 0, nil
		}
		return "", 0, fmt.Errorf("for time triggers, loop must be 'daily' or empty")
	}
	// delay mode: interval duration (compound supported, e.g. 3h20m)
	if sec, ok := parseDurationSeconds(s); ok {
		return windowTimerLoopInterval, sec, nil
	}
	return "", 0, fmt.Errorf("invalid loop interval %q: use duration (5m, 1h) or 'none'", s)
}

// scheduleQuotaFireAt stamps a quota-trigger timer with the probed reset
// instant: exact (429 stamp / codex rollout) or the statusline fallback
// estimate (QuotaFallback=true, upgraded to exact by the re-arm pass when a
// limit stamp appears). Dormant (zero) when no source knows.
func scheduleQuotaFireAt(t *windowTimer) {
	at, fallback, err := quotaResetFireAt(t.WindowID)
	if err != nil {
		t.NextFireAt = time.Time{}
		t.QuotaFallback = false
		return
	}
	t.NextFireAt = at
	t.QuotaFallback = fallback
}

// computeNextFireAt calculates the next time the timer should fire, starting from now.
// Quota triggers are scheduled via scheduleQuotaFireAt (needs the fallback flag).
func computeNextFireAt(t *windowTimer) time.Time {
	loc := windowTimerLocation()
	return computeNextFireAtFrom(t, time.Now().In(loc), loc)
}

func computeNextFireAtFrom(t *windowTimer, now time.Time, loc *time.Location) time.Time {
	now = now.In(loc)
	switch t.TriggerMode {
	case windowTimerTriggerQuota:
		return time.Time{}
	case windowTimerTriggerDelay:
		return now.Add(time.Duration(t.TriggerDelaySec) * time.Second)
	case windowTimerTriggerTime:
		parts := strings.SplitN(t.TriggerTime, ":", 2)
		if len(parts) != 2 {
			return now.Add(time.Hour)
		}
		h, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		fire := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, loc)
		if !fire.After(now) {
			fire = fire.AddDate(0, 0, 1)
		}
		return fire
	}
	return now.Add(time.Hour)
}

// computeNextLoopFireAt calculates the next fire time after execution, for looping timers.
// Quota loops are rescheduled via scheduleQuotaFireAt at the call site.
func computeNextLoopFireAt(t *windowTimer, prevFire time.Time) time.Time {
	loc := windowTimerLocation()
	return computeNextLoopFireAtFrom(t, prevFire, time.Now().In(loc), loc)
}

func computeNextLoopFireAtFrom(t *windowTimer, prevFire, now time.Time, loc *time.Location) time.Time {
	switch t.LoopMode {
	case windowTimerLoopInterval:
		next := prevFire.Add(time.Duration(t.LoopIntervalSec) * time.Second)
		if !next.After(now) {
			// Sleep/downtime swallowed whole periods: re-anchor to now instead of
			// replaying every missed period as an immediate one-per-tick burst.
			next = now.Add(time.Duration(t.LoopIntervalSec) * time.Second)
		}
		return next
	case windowTimerLoopDaily:
		parts := strings.SplitN(t.TriggerTime, ":", 2)
		if len(parts) != 2 {
			return prevFire.Add(24 * time.Hour)
		}
		h, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		now = now.In(loc)
		fire := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, loc)
		if !fire.After(now) {
			fire = fire.AddDate(0, 0, 1)
		}
		return fire
	}
	return time.Time{}
}

// timerDisplaySummary returns the "[N]HH:MM" string for the window nav column.
// N = count of enabled timers, HH:MM = next upcoming fire time.
// Returns "" if no enabled timers.
func timerDisplaySummary(timers []*windowTimer) string {
	return timerDisplaySummaryIn(timers, windowTimerLocation())
}

func timerDisplaySummaryIn(timers []*windowTimer, loc *time.Location) string {
	var enabled []*windowTimer
	for _, t := range timers {
		if t.Enabled && !t.NextFireAt.IsZero() {
			enabled = append(enabled, t)
		}
	}
	if len(enabled) == 0 {
		return ""
	}
	sort.Slice(enabled, func(i, j int) bool {
		return enabled[i].NextFireAt.Before(enabled[j].NextFireAt)
	})
	next := enabled[0].NextFireAt
	return fmt.Sprintf("[%d]%s", len(enabled), formatWindowTimerTimeIn(next, "15:04", loc))
}

// timersTotalCount returns total timer count for a window (enabled+disabled).
func timersTotalCount(timers []*windowTimer) int {
	return len(timers)
}

// formatDelayDuration formats seconds into a human-readable string like "5m", "1h30m".
func formatDelayDuration(sec int64) string {
	if sec <= 0 {
		return "0s"
	}
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	if sec < 3600 {
		m := sec / 60
		s := sec % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

// timerTriggerInput returns the trigger in the same string form parseTrigger
// accepts, for round-tripping an existing timer back through addWindowTimer
// (copy-to-current-window).
func timerTriggerInput(t *windowTimer) string {
	switch t.TriggerMode {
	case windowTimerTriggerTime:
		return t.TriggerTime
	case windowTimerTriggerQuota:
		return "reset"
	}
	return formatDelayDuration(t.TriggerDelaySec)
}

// timerLoopInput mirrors timerTriggerInput for the loop field.
func timerLoopInput(t *windowTimer) string {
	switch t.LoopMode {
	case windowTimerLoopInterval:
		return formatDelayDuration(t.LoopIntervalSec)
	case windowTimerLoopDaily:
		return "daily"
	case windowTimerLoopQuota:
		return "reset"
	}
	return ""
}

// timerTriggerDisplay returns a short human-readable string for the trigger.
func timerTriggerDisplay(t *windowTimer) string {
	switch t.TriggerMode {
	case windowTimerTriggerTime:
		return t.TriggerTime
	case windowTimerTriggerDelay:
		return formatDelayDuration(t.TriggerDelaySec)
	case windowTimerTriggerQuota:
		return "reset"
	}
	return "?"
}

// timerLoopDisplay returns a short string for the loop mode.
func timerLoopDisplay(t *windowTimer) string {
	switch t.LoopMode {
	case windowTimerLoopNone:
		return "once"
	case windowTimerLoopDaily:
		return "daily"
	case windowTimerLoopInterval:
		return "every " + formatDelayDuration(t.LoopIntervalSec)
	case windowTimerLoopQuota:
		return "each reset"
	}
	return "?"
}

// timerExecDisplay returns "X/N" or "X/∞".
func timerExecDisplay(t *windowTimer) string {
	if t.MaxExecutions == 0 {
		return fmt.Sprintf("%d/∞", t.ExecutionCount)
	}
	return fmt.Sprintf("%d/%d", t.ExecutionCount, t.MaxExecutions)
}

// ── firing ────────────────────────────────────────────────────────────────────

// findAIPaneForWindow returns the pane_id of the pane with @agent_pane_role=ai in the window.
// Falls back to the first pane if none found.
func findAIPaneForWindow(windowID string) string {
	out, err := runTmuxOutput("list-panes", "-t", windowID, "-F", "#{pane_id}|#{@agent_pane_role}")
	if err != nil {
		return ""
	}
	firstPane := ""
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) < 1 || parts[0] == "" {
			continue
		}
		if firstPane == "" {
			firstPane = parts[0]
		}
		if len(parts) == 2 && parts[1] == "ai" {
			return parts[0]
		}
	}
	return firstPane
}

// fireTimer sends the timer's content to the window's AI pane. A usage-limit
// dialog left on screen (e.g. the quota-reset trigger firing after the limit
// lifted) would swallow the keys, so it is dismissed first.
func fireTimer(t *windowTimer, ci *claudeIndex) error {
	paneID := findAIPaneForWindow(t.WindowID)
	if paneID == "" {
		return fmt.Errorf("no pane found for window %s", t.WindowID)
	}
	// Clear a usage-limit dialog first, but ONLY when it's actually detected on
	// screen and no agent is mid-turn (see shouldDismissUsageDialog). When it does
	// Escape it waits ~500ms so the box tears down before the content lands; the
	// normal no-dialog case sends nothing here and just injects content+Enter.
	dismissUsageLimitDialog(paneID, ci)
	return pasteAndSubmit(paneID, t.Content, t.SendEnter)
}

// pasteAndSubmit delivers content to a pane as a bracketed paste, then optionally
// submits with Enter.
//
// A raw `send-keys -l` burst carries no explicit paste-end marker, so an agent
// TUI (Claude Code) that detects pastes by timing can still be "settling" the
// burst when a follow-up Enter arrives and absorb it as a newline instead of
// submitting — a race that surfaces on slower machines (content ends up sitting
// in the input box with a trailing newline, never sent). Bracketed paste
// (set-buffer + `paste-buffer -p`) wraps the content in ESC[200~…ESC[201~, so the
// terminal sees an explicit paste-end before the Enter in the same ordered byte
// stream; the Enter is then unambiguously a submit regardless of timing.
//
// `paste-buffer -p` only adds the brackets when the app requested bracketed paste
// mode, so it degrades gracefully for panes that didn't. A pane-scoped named
// buffer avoids clobbering the user's paste buffer and races between panes.
func pasteAndSubmit(paneID, content string, sendEnter bool) error {
	if strings.TrimSpace(paneID) == "" {
		return fmt.Errorf("pasteAndSubmit: empty pane")
	}
	if content != "" {
		buf := "agent-paste-" + sanitizeBufferName(paneID)
		if err := runTmux("set-buffer", "-b", buf, "--", content); err != nil {
			return err
		}
		if err := runTmux("paste-buffer", "-p", "-d", "-b", buf, "-t", paneID); err != nil {
			return err
		}
	}
	if sendEnter {
		// The Enter already follows the paste-end marker in-order, so submission no
		// longer depends on timing; this brief pause is just insurance for the input
		// to render before the submit.
		time.Sleep(150 * time.Millisecond)
		return runTmux("send-keys", "-t", paneID, "Enter")
	}
	return nil
}

// sanitizeBufferName maps a pane id (e.g. "%5") to a tmux-buffer-safe token.
func sanitizeBufferName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, s)
}

// acquireTimerLock serializes every load-modify-write of the timer store and
// history files across processes — the timer panel (its own tea process) and
// the sync loop mutate the same files, and an unlocked read-modify-write races
// away one side's save (e.g. a fire-count update overwriting a just-added
// timer). Best-effort: on lock failure proceed unlocked rather than dropping
// the operation.
func acquireTimerLock() func() {
	dir := filepath.Join(filepath.Dir(paths.TimersFile()), ".timers.lock.d")
	release, err := acquireMkdirLock(dir, configLockStale)
	if err != nil {
		return func() {}
	}
	return release
}

// reconcileTimerOwnershipLive runs reconcileTimerOwnership against the live
// tmux state (current server pid + live window set).
func reconcileTimerOwnershipLive(store *windowTimerStore) bool {
	live := map[string]bool{}
	if out, err := runTmuxOutput("list-windows", "-a", "-F", "#{window_id}"); err == nil {
		for _, id := range strings.Fields(out) {
			live[id] = true
		}
	}
	return reconcileTimerOwnership(store, tmuxServerPID(), live)
}

// checkAndFireTimers checks all timers and fires any that are due.
// Called from the main sync loop every ~1 second; ci is that pass's shared
// index (nil for one-shot callers).
// Mutates and persists the timer store if any timers fire or are updated.
func checkAndFireTimers(ci *claudeIndex) {
	release := acquireTimerLock()
	defer release()
	store := loadWindowTimerStore()
	// Drop timers stranded by a tmux server restart (recycled window ids) or a
	// closed window BEFORE firing — a recycled id would otherwise inject an
	// unrelated window's pane.
	dirty := false
	if len(store.Timers) > 0 {
		dirty = reconcileTimerOwnershipLive(store)
	}
	timers := store.Timers
	if len(timers) == 0 {
		if dirty {
			_ = saveWindowTimerStore(store)
		}
		return
	}
	now := windowTimerNow()
	doneDelete := map[string]bool{}
	// Account-wide wake signal for dormant/fallback-armed reset timers, resolved
	// lazily at most once per tick: any window's future @agent_limit_reset_at stamp.
	globalReset := time.Time{}
	globalResolved := false
	for _, t := range timers {
		if t.Enabled && t.TriggerMode == windowTimerTriggerQuota &&
			(t.NextFireAt.IsZero() || t.QuotaFallback) {
			// Dormant or fallback-armed reset timer: an exact limit stamp (429
			// hit) overrides. Prefer the timer's own window; quota is
			// account-wide, so any other window's stamp counts too.
			exact := time.Time{}
			if until, ok := windowQuotaLimitedUntil(t.WindowID); ok {
				exact = until
			} else {
				if !globalResolved {
					globalReset = anyWindowQuotaLimitedUntil()
					globalResolved = true
				}
				exact = globalReset
			}
			if !exact.IsZero() {
				t.NextFireAt = exact.Add(quotaResetFireBuffer)
				t.QuotaFallback = false
				dirty = true
			}
			if t.NextFireAt.IsZero() {
				continue // still dormant
			}
			// fall through: a fallback-armed timer may already be due
		}
		if !t.Enabled || t.NextFireAt.IsZero() {
			continue
		}
		if !now.After(t.NextFireAt) {
			continue
		}
		// Fire the timer. A failure (pane vanished mid-tick, tmux timeout) still
		// advances the schedule — retrying stale content into a changed pane is
		// worse than skipping — but is logged so silent no-shows are traceable.
		if err := fireTimer(t, ci); err != nil {
			fmt.Fprintf(os.Stderr, "agent: timer %s (window %s) fire failed: %v\n", t.ID, t.WindowID, err)
		}
		t.ExecutionCount++
		t.QuotaFallback = false
		dirty = true

		// Decide next state
		maxReached := t.MaxExecutions > 0 && t.ExecutionCount >= t.MaxExecutions
		canLoop := t.LoopMode != windowTimerLoopNone && !maxReached
		switch {
		case canLoop && t.LoopMode == windowTimerLoopQuota:
			// Usually dormant right after a reset (no fresh limit yet); the
			// re-arm pass above wakes it on the next limit stamp.
			scheduleQuotaFireAt(t)
		case canLoop:
			t.NextFireAt = computeNextLoopFireAt(t, t.NextFireAt)
		case t.DeleteOnDone:
			doneDelete[t.ID] = true
		default:
			// No more loops or max reached: disable (but keep the record)
			t.Enabled = false
			t.NextFireAt = time.Time{}
		}
	}
	if len(doneDelete) > 0 {
		kept := timers[:0]
		for _, t := range timers {
			if !doneDelete[t.ID] {
				kept = append(kept, t)
			}
		}
		timers = kept
	}
	if dirty {
		store.Timers = timers
		_ = saveWindowTimerStore(store)
	}
}

// timersForWindow returns timers filtered to a specific window ID.
func timersForWindow(windowID string) []*windowTimer {
	all := loadWindowTimers()
	var result []*windowTimer
	for _, t := range all {
		if t.WindowID == windowID {
			result = append(result, t)
		}
	}
	return result
}

// timersByWindowMap returns a map of windowID -> display summary ("[N]HH:MM").
// Used by window nav to populate the timer column.
func timersByWindowMap() map[string]string {
	all := loadWindowTimers()
	byWindow := map[string][]*windowTimer{}
	for _, t := range all {
		byWindow[t.WindowID] = append(byWindow[t.WindowID], t)
	}
	result := map[string]string{}
	for wid, timers := range byWindow {
		if s := timerDisplaySummary(timers); s != "" {
			result[wid] = s
		} else if len(timers) > 0 {
			// Has timers but none enabled/upcoming: show count only
			result[wid] = fmt.Sprintf("[%d]--:--", len(timers))
		}
	}
	return result
}

// addWindowTimer adds a new timer to the store and returns it.
func addWindowTimer(windowID, content, triggerStr, loopStr, maxStr string, sendEnter, autoDelete bool) (*windowTimer, error) {
	mode, delaySec, timeStr, err := parseTrigger(triggerStr)
	if err != nil {
		return nil, err
	}
	maxExec := 0
	if maxStr != "" && maxStr != "0" {
		maxExec, err = strconv.Atoi(maxStr)
		if err != nil || maxExec < 0 {
			return nil, fmt.Errorf("max executions must be a non-negative integer")
		}
	}
	loopMode, loopSec, err := parseLoopInterval(loopStr, mode)
	if err != nil {
		return nil, err
	}
	t := &windowTimer{
		ID:              newTimerID(),
		WindowID:        windowID,
		Content:         content,
		SendEnter:       sendEnter,
		TriggerMode:     mode,
		TriggerDelaySec: delaySec,
		TriggerTime:     timeStr,
		LoopMode:        loopMode,
		LoopIntervalSec: loopSec,
		MaxExecutions:   maxExec,
		ExecutionCount:  0,
		Enabled:         true,
		CreatedAt:       windowTimerNow(),
		DeleteOnDone:    autoDelete,
	}
	// Quota triggers may be created before any limit is hit: exact 429 stamp →
	// statusline fallback estimate → dormant; the per-tick re-arm pass upgrades
	// fallback/dormant timers when a limit stamp appears.
	if mode == windowTimerTriggerQuota {
		scheduleQuotaFireAt(t)
	} else {
		t.NextFireAt = computeNextFireAt(t)
	}

	release := acquireTimerLock()
	defer release()
	store := loadWindowTimerStore()
	// Clean + stamp ownership on the way in, so a timer added right after a
	// tmux server restart lands in a store already claimed by this server.
	_ = reconcileTimerOwnershipLive(store)
	store.Timers = append(store.Timers, t)
	if err := saveWindowTimerStore(store); err != nil {
		return nil, err
	}
	recordTimerHistory(windowID, content, triggerStr, loopStr, maxStr, sendEnter)
	return t, nil
}

// updateWindowTimer updates an existing timer by ID.
func updateWindowTimer(id, windowID, content, triggerStr, loopStr, maxStr string, sendEnter, autoDelete bool) error {
	mode, delaySec, timeStr, err := parseTrigger(triggerStr)
	if err != nil {
		return err
	}
	maxExec := 0
	if maxStr != "" && maxStr != "0" {
		maxExec, err = strconv.Atoi(maxStr)
		if err != nil || maxExec < 0 {
			return fmt.Errorf("max executions must be a non-negative integer")
		}
	}
	loopMode, loopSec, err := parseLoopInterval(loopStr, mode)
	if err != nil {
		return err
	}
	release := acquireTimerLock()
	defer release()
	timers := loadWindowTimers()
	for _, t := range timers {
		if t.ID != id || t.WindowID != windowID {
			continue
		}
		t.Content = content
		t.SendEnter = sendEnter
		t.TriggerMode = mode
		t.TriggerDelaySec = delaySec
		t.TriggerTime = timeStr
		t.LoopMode = loopMode
		t.LoopIntervalSec = loopSec
		t.MaxExecutions = maxExec
		t.DeleteOnDone = autoDelete
		if mode == windowTimerTriggerQuota {
			scheduleQuotaFireAt(t)
		} else {
			t.NextFireAt = computeNextFireAt(t)
			t.QuotaFallback = false
		}
		if !t.Enabled {
			t.Enabled = true
		}
		if err := saveWindowTimers(timers); err != nil {
			return err
		}
		recordTimerHistory(windowID, content, triggerStr, loopStr, maxStr, sendEnter)
		return nil
	}
	return fmt.Errorf("timer %s not found", id)
}

// deleteWindowTimer removes a timer by ID.
func deleteWindowTimer(id, windowID string) error {
	release := acquireTimerLock()
	defer release()
	timers := loadWindowTimers()
	newTimers := timers[:0]
	for _, t := range timers {
		if t.ID == id && t.WindowID == windowID {
			continue
		}
		newTimers = append(newTimers, t)
	}
	return saveWindowTimers(newTimers)
}

// toggleWindowTimer enables/disables a timer by ID.
func toggleWindowTimer(id, windowID string) error {
	release := acquireTimerLock()
	defer release()
	timers := loadWindowTimers()
	for _, t := range timers {
		if t.ID != id || t.WindowID != windowID {
			continue
		}
		t.Enabled = !t.Enabled
		if t.Enabled {
			if t.TriggerMode == windowTimerTriggerQuota {
				// re-enables as exact / fallback / dormant, same as add
				scheduleQuotaFireAt(t)
			} else {
				t.NextFireAt = computeNextFireAt(t)
			}
		} else {
			t.NextFireAt = time.Time{}
			t.QuotaFallback = false
		}
		return saveWindowTimers(timers)
	}
	return fmt.Errorf("timer %s not found", id)
}
