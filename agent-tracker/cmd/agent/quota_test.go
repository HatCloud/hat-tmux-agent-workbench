package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseClaudeResetText(t *testing.T) {
	// Real 429 sample: event 2026-07-08T03:49:12Z is 2026-07-07 20:49 in LA, so
	// the next 12:40am there is 2026-07-08 00:40 PDT = 07:40 UTC.
	event := time.Date(2026, 7, 8, 3, 49, 12, 0, time.UTC)
	got, ok := parseClaudeResetText(
		"You've hit your session limit · resets 12:40am (America/Los_Angeles)", event)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if want := time.Date(2026, 7, 8, 7, 40, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got.UTC(), want)
	}

	// "reset at Npm" variant; same-day occurrence when still ahead of the event.
	event = time.Date(2026, 7, 8, 3, 0, 0, 0, time.UTC)
	got, ok = parseClaudeResetText("Your limit will reset at 4pm (UTC)", event)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if want := time.Date(2026, 7, 8, 16, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got.UTC(), want)
	}

	// 12pm is noon, not midnight.
	got, ok = parseClaudeResetText("resets 12pm (UTC)", event)
	if !ok || got.UTC().Hour() != 12 {
		t.Fatalf("12pm should be noon, got %v ok=%v", got.UTC(), ok)
	}

	// 24h style without am/pm.
	got, ok = parseClaudeResetText("resets 18:30 (UTC)", event)
	if !ok || got.UTC().Hour() != 18 || got.UTC().Minute() != 30 {
		t.Fatalf("24h parse failed, got %v ok=%v", got.UTC(), ok)
	}

	// No reset phrase, or a dated weekly text we can't resolve → not ok.
	if _, ok := parseClaudeResetText("plain error text", event); ok {
		t.Fatal("expected no parse for plain text")
	}
	if _, ok := parseClaudeResetText("resets Jul 15 at 10am (UTC)", event); ok {
		t.Fatal("expected no parse for dated weekly text")
	}
}

func TestPickCodexReset(t *testing.T) {
	now := time.Unix(1_783_600_000, 0)
	future := func(d time.Duration) int64 { return now.Add(d).Unix() }

	// Secondary (weekly) exhausted at 99% → wait for it even though primary
	// resets sooner.
	rl := codexRateLimits{
		Primary:   &codexRateWindow{UsedPercent: 11, WindowMinutes: 300, ResetsAt: future(2 * time.Hour)},
		Secondary: &codexRateWindow{UsedPercent: 99, WindowMinutes: 10080, ResetsAt: future(20 * time.Hour)},
	}
	got, ok := pickCodexReset(rl, now)
	if !ok || got.Unix() != future(20*time.Hour) {
		t.Fatalf("exhausted secondary should win: got %v ok=%v", got, ok)
	}

	// Nothing exhausted → shortest window's boundary (primary).
	rl.Secondary.UsedPercent = 40
	got, ok = pickCodexReset(rl, now)
	if !ok || got.Unix() != future(2*time.Hour) {
		t.Fatalf("primary boundary expected: got %v ok=%v", got, ok)
	}

	// Stale snapshot: resets already in the past never qualify.
	rl = codexRateLimits{
		Primary: &codexRateWindow{UsedPercent: 99, WindowMinutes: 300, ResetsAt: now.Add(-time.Hour).Unix()},
	}
	if _, ok := pickCodexReset(rl, now); ok {
		t.Fatal("stale reset should not qualify")
	}

	if _, ok := pickCodexReset(codexRateLimits{}, now); ok {
		t.Fatal("empty rate limits should not qualify")
	}
}

func writeTempJSONL(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClaudeLimitResetFromJSONL(t *testing.T) {
	line429 := `{"type":"assistant","timestamp":"2030-01-01T00:00:00Z","error":"rate_limit","apiErrorStatus":429,` +
		`"message":{"content":[{"type":"text","text":"You've hit your session limit · resets 3am (UTC)"}]}}`
	user := `{"type":"user","timestamp":"2030-01-01T00:10:00Z","message":{"content":"try again"}}`
	meta := `{"type":"ai-title","aiTitle":"whatever"}`
	now := time.Date(2030, 1, 1, 1, 0, 0, 0, time.UTC)

	// 429 as the latest turn event → limited until 03:00 UTC.
	path := writeTempJSONL(t, []string{user, line429, meta})
	got, ok := claudeLimitResetFromJSONL(path, now)
	if !ok {
		t.Fatal("expected limited")
	}
	if want := time.Date(2030, 1, 1, 3, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got.UTC(), want)
	}

	// A newer user message supersedes the 429.
	path = writeTempJSONL(t, []string{line429, user})
	if _, ok := claudeLimitResetFromJSONL(path, now); ok {
		t.Fatal("newer user message should clear limited")
	}

	// Reset already in the past → not limited.
	path = writeTempJSONL(t, []string{line429})
	late := time.Date(2030, 1, 1, 4, 0, 0, 0, time.UTC)
	if _, ok := claudeLimitResetFromJSONL(path, late); ok {
		t.Fatal("past reset should not be limited")
	}
}

func TestCodexRateLimitsFromRollout(t *testing.T) {
	tc := func(primaryPct float64, resetsAt int64) string {
		return fmt.Sprintf(`{"timestamp":"2030-01-01T00:00:00Z","type":"event_msg","payload":{"type":"token_count",`+
			`"rate_limits":{"primary":{"used_percent":%g,"window_minutes":300,"resets_at":%d}}}}`,
			primaryPct, resetsAt)
	}
	other := `{"timestamp":"2030-01-01T00:00:01Z","type":"event_msg","payload":{"type":"agent_message"}}`
	nullRL := `{"timestamp":"2030-01-01T00:00:02Z","type":"event_msg","payload":{"type":"token_count"}}`

	// Latest token_count with non-null rate_limits wins; trailing events without
	// rate_limits don't clobber it.
	path := writeTempJSONL(t, []string{tc(10, 100), tc(80, 200), other, nullRL})
	rl, ok := codexRateLimitsFromRollout(path)
	if !ok || rl.Primary == nil || rl.Primary.ResetsAt != 200 || rl.Primary.UsedPercent != 80 {
		t.Fatalf("unexpected snapshot: %+v ok=%v", rl, ok)
	}

	if _, ok := codexRateLimitsFromRollout(filepath.Join(t.TempDir(), "missing.jsonl")); ok {
		t.Fatal("missing file should not parse")
	}
}

func TestParseTriggerQuota(t *testing.T) {
	for _, s := range []string{"reset", "quota", "RESET"} {
		mode, _, _, err := parseTrigger(s)
		if err != nil || mode != windowTimerTriggerQuota {
			t.Fatalf("parseTrigger(%q) = %v, %v", s, mode, err)
		}
	}
	if _, _, err := parseLoopInterval("5m", windowTimerTriggerQuota); err == nil {
		t.Fatal("quota trigger must reject duration loops")
	}
	if lm, _, err := parseLoopInterval("", windowTimerTriggerQuota); err != nil || lm != windowTimerLoopNone {
		t.Fatalf("empty loop should be none for quota: %v %v", lm, err)
	}
	if lm, _, err := parseLoopInterval("reset", windowTimerTriggerQuota); err != nil || lm != windowTimerLoopQuota {
		t.Fatalf("reset loop should parse for quota trigger: %v %v", lm, err)
	}
	if _, _, err := parseLoopInterval("reset", windowTimerTriggerDelay); err == nil {
		t.Fatal("reset loop must be rejected for non-quota triggers")
	}
}

func TestClaudeFallbackResetAt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".hat-config", "state", "agent-tracker")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_900_000_000, 0)
	writeCache := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "claude-rate-limits.json"), []byte(s), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// No cache file → no fallback.
	if _, ok := claudeFallbackResetAt(now); ok {
		t.Fatal("missing cache should not produce a fallback")
	}

	// Normal usage: five_hour boundary wins (shortest window, nothing exhausted).
	writeCache(fmt.Sprintf(
		`{"five_hour":{"used_percentage":42.5,"resets_at":%d},"seven_day":{"used_percentage":80.1,"resets_at":%d}}`,
		now.Unix()+3600, now.Unix()+86400))
	got, ok := claudeFallbackResetAt(now)
	if !ok || got.Unix() != now.Unix()+3600 {
		t.Fatalf("expected five_hour boundary, got %v ok=%v", got, ok)
	}

	// Weekly exhausted → wait for it.
	writeCache(fmt.Sprintf(
		`{"five_hour":{"used_percentage":42.5,"resets_at":%d},"seven_day":{"used_percentage":99.0,"resets_at":%d}}`,
		now.Unix()+3600, now.Unix()+86400))
	got, ok = claudeFallbackResetAt(now)
	if !ok || got.Unix() != now.Unix()+86400 {
		t.Fatalf("expected exhausted seven_day reset, got %v ok=%v", got, ok)
	}

	// Stale cache (resets in the past) → no fallback.
	writeCache(fmt.Sprintf(`{"five_hour":{"used_percentage":10,"resets_at":%d}}`, now.Unix()-60))
	if _, ok := claudeFallbackResetAt(now); ok {
		t.Fatal("stale cache should not produce a fallback")
	}
}

func TestScreenShowsUsageLimit(t *testing.T) {
	for _, s := range []string{
		"│ You've hit your session limit · resets 12:40am │",
		"You have hit your usage limit",
		"Usage limit reached · upgrade or wait",
		"You've reached your weekly limit",
		"You're approaching your usage limit",
		"Your limit will reset at 3pm",
		"Opus limit resets at 9:00",
		"resets at 12",
		"Please try again later",
	} {
		if !screenShowsUsageLimit(s) {
			t.Fatalf("should match: %q", s)
		}
	}
	if screenShowsUsageLimit("just a normal claude screen ready for input") {
		t.Fatal("should not match plain screen")
	}
}

// TestShouldDismissUsageDialog covers the only safety gate for timer firing:
// Escape only when neither client is busy AND the quota box is on screen.
func TestShouldDismissUsageDialog(t *testing.T) {
	cases := []struct {
		name                                     string
		claudeBusy, codexBusy, screenMatch, want bool
	}{
		{"claude busy blocks", true, false, true, false},
		{"codex busy blocks", false, true, true, false},
		{"both busy blocks", true, true, true, false},
		{"idle no dialog → no escape", false, false, false, false},
		{"idle + dialog → escape", false, false, true, true},
	}
	for _, c := range cases {
		if got := shouldDismissUsageDialog(c.claudeBusy, c.codexBusy, c.screenMatch); got != c.want {
			t.Errorf("%s: shouldDismissUsageDialog(%v,%v,%v) = %v, want %v",
				c.name, c.claudeBusy, c.codexBusy, c.screenMatch, got, c.want)
		}
	}
}
