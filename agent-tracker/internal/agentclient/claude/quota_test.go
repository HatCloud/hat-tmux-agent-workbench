package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseResetText(t *testing.T) {
	// Real 429 sample: event 2026-07-08T03:49:12Z is 2026-07-07 20:49 in LA, so
	// the next 12:40am there is 2026-07-08 00:40 PDT = 07:40 UTC.
	event := time.Date(2026, 7, 8, 3, 49, 12, 0, time.UTC)
	got, ok := parseResetText(
		"You've hit your session limit · resets 12:40am (America/Los_Angeles)", event)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if want := time.Date(2026, 7, 8, 7, 40, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got.UTC(), want)
	}

	// "reset at Npm" variant; same-day occurrence when still ahead of the event.
	event = time.Date(2026, 7, 8, 3, 0, 0, 0, time.UTC)
	got, ok = parseResetText("Your limit will reset at 4pm (UTC)", event)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if want := time.Date(2026, 7, 8, 16, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got.UTC(), want)
	}

	// 12pm is noon, not midnight.
	got, ok = parseResetText("resets 12pm (UTC)", event)
	if !ok || got.UTC().Hour() != 12 {
		t.Fatalf("12pm should be noon, got %v ok=%v", got.UTC(), ok)
	}

	// 24h style without am/pm.
	got, ok = parseResetText("resets 18:30 (UTC)", event)
	if !ok || got.UTC().Hour() != 18 || got.UTC().Minute() != 30 {
		t.Fatalf("24h parse failed, got %v ok=%v", got.UTC(), ok)
	}

	// No reset phrase, or a dated weekly text we can't resolve → not ok.
	if _, ok := parseResetText("plain error text", event); ok {
		t.Fatal("expected no parse for plain text")
	}
	if _, ok := parseResetText("resets Jul 15 at 10am (UTC)", event); ok {
		t.Fatal("expected no parse for dated weekly text")
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

func TestLimitResetFromJSONL(t *testing.T) {
	line429 := `{"type":"assistant","timestamp":"2030-01-01T00:00:00Z","error":"rate_limit","apiErrorStatus":429,` +
		`"message":{"content":[{"type":"text","text":"You've hit your session limit · resets 3am (UTC)"}]}}`
	user := `{"type":"user","timestamp":"2030-01-01T00:10:00Z","message":{"content":"try again"}}`
	meta := `{"type":"ai-title","aiTitle":"whatever"}`
	now := time.Date(2030, 1, 1, 1, 0, 0, 0, time.UTC)

	// 429 as the latest turn event → limited until 03:00 UTC.
	path := writeTempJSONL(t, []string{user, line429, meta})
	got, ok := limitResetFromJSONL(path, now)
	if !ok {
		t.Fatal("expected limited")
	}
	if want := time.Date(2030, 1, 1, 3, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got.UTC(), want)
	}

	// A newer user message supersedes the 429.
	path = writeTempJSONL(t, []string{line429, user})
	if _, ok := limitResetFromJSONL(path, now); ok {
		t.Fatal("newer user message should clear limited")
	}

	// Reset already in the past → not limited.
	path = writeTempJSONL(t, []string{line429})
	late := time.Date(2030, 1, 1, 4, 0, 0, 0, time.UTC)
	if _, ok := limitResetFromJSONL(path, late); ok {
		t.Fatal("past reset should not be limited")
	}
}

func TestFallbackResetAt(t *testing.T) {
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
	if _, ok := fallbackResetAt(now); ok {
		t.Fatal("missing cache should not produce a fallback")
	}

	// Normal usage: five_hour boundary wins (shortest window, nothing exhausted).
	writeCache(fmt.Sprintf(
		`{"five_hour":{"used_percentage":42.5,"resets_at":%d},"seven_day":{"used_percentage":80.1,"resets_at":%d}}`,
		now.Unix()+3600, now.Unix()+86400))
	got, ok := fallbackResetAt(now)
	if !ok || got.Unix() != now.Unix()+3600 {
		t.Fatalf("expected five_hour boundary, got %v ok=%v", got, ok)
	}

	// Weekly exhausted → wait for it.
	writeCache(fmt.Sprintf(
		`{"five_hour":{"used_percentage":42.5,"resets_at":%d},"seven_day":{"used_percentage":99.0,"resets_at":%d}}`,
		now.Unix()+3600, now.Unix()+86400))
	got, ok = fallbackResetAt(now)
	if !ok || got.Unix() != now.Unix()+86400 {
		t.Fatalf("expected exhausted seven_day reset, got %v ok=%v", got, ok)
	}

	// Stale cache (resets in the past) → no fallback.
	writeCache(fmt.Sprintf(`{"five_hour":{"used_percentage":10,"resets_at":%d}}`, now.Unix()-60))
	if _, ok := fallbackResetAt(now); ok {
		t.Fatal("stale cache should not produce a fallback")
	}
}
