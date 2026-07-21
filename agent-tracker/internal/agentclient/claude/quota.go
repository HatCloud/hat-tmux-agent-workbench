package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/david/agent-tracker/internal/agentclient"
	"github.com/david/agent-tracker/internal/paths"
)

// Usage-quota probing from on-disk artifacts only (no network). Claude has no
// proactive source: hitting the limit inserts a synthetic assistant message
// (error="rate_limit", apiErrorStatus=429) into the project session JSONL whose
// text ends in a human-readable "… resets 12:40am (America/Los_Angeles)"; we
// parse that relative to the event timestamp. Weekly-limit texts with an
// explicit date don't match the pattern and are treated as "no reset known".

var reResetText = regexp.MustCompile(
	`(?i)\bresets?\b(?:\s+at)?\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)?(?:\s*\(([^)]+)\))?`)

// parseResetText resolves the absolute reset instant from a 429 message text
// and the moment it was written: the next occurrence of the given wall-clock
// time (in the given IANA zone, defaulting to local) strictly after the event.
func parseResetText(text string, event time.Time) (time.Time, bool) {
	m := reResetText.FindStringSubmatch(text)
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

// limitResetFromJSONL reports whether the session's latest turn ended on an
// unresolved usage-limit 429, and when that limit lifts. Any newer user or
// assistant message supersedes an earlier 429 (a later turn got through), and a
// reset instant already in the past means the window has reopened.
func limitResetFromJSONL(path string, now time.Time) (time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	scanner := tailScanner(f, jsonlTailBytes)
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
			if at, ok := parseResetText(textFromJSONContent(entry.Message.Content), ev); ok {
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

// rateLimitsCachePath is written by cc-statusline-official on every status-line
// render: the rate_limits object Claude Code injects into the statusline
// payload (first-party five_hour/seven_day used_percentage + resets_at epochs).
// Cached, so it lags a little behind the true boundary — good enough as the
// fallback fire time until a 429 stamp gives the exact one.
func rateLimitsCachePath() string {
	return filepath.Join(paths.StateDir(), "claude-rate-limits.json")
}

// fallbackResetAt reads the statusline cache and picks the reset instant with
// the shared exhausted-window rules (5h ↔ primary, 7d ↔ secondary).
func fallbackResetAt(now time.Time) (time.Time, bool) {
	data, err := os.ReadFile(rateLimitsCachePath())
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
	var windows []agentclient.RateWindow
	if rl.FiveHour.ResetsAt > 0 {
		windows = append(windows, agentclient.RateWindow{
			UsedPercent: rl.FiveHour.UsedPercentage, WindowMinutes: 300, ResetsAt: rl.FiveHour.ResetsAt})
	}
	if rl.SevenDay.ResetsAt > 0 {
		windows = append(windows, agentclient.RateWindow{
			UsedPercent: rl.SevenDay.UsedPercentage, WindowMinutes: 10080, ResetsAt: rl.SevenDay.ResetsAt})
	}
	return agentclient.PickReset(windows, now)
}
