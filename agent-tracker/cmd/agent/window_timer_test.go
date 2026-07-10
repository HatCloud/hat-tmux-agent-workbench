package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseDurationSeconds(t *testing.T) {
	ok := []struct {
		in   string
		want int64
	}{
		{"5m", 300},
		{"1h", 3600},
		{"30s", 30},
		{"1d", 86400},
		{"3h20m", 3*3600 + 20*60},
		{"1h30m", 5400},
		{"1h30m15s", 3600 + 30*60 + 15},
		{"2D", 2 * 86400}, // case-insensitive
	}
	for _, c := range ok {
		got, valid := parseDurationSeconds(c.in)
		if !valid || got != c.want {
			t.Errorf("parseDurationSeconds(%q)=%d,%v want %d,true", c.in, got, valid, c.want)
		}
	}
	bad := []string{"", "abc", "5x", "1h2", "h20m", "-5m", "0m", ":", "13:10"}
	for _, c := range bad {
		if _, valid := parseDurationSeconds(c); valid {
			t.Errorf("parseDurationSeconds(%q) expected invalid", c)
		}
	}
}

func TestParseTriggerCompound(t *testing.T) {
	mode, delay, _, err := parseTrigger("3h20m")
	if err != nil || mode != windowTimerTriggerDelay || delay != 3*3600+20*60 {
		t.Fatalf("parseTrigger(3h20m)=%v,%d,%v want delay,%d,nil", mode, delay, err, 3*3600+20*60)
	}
}

func TestComputeNextFireAtUsesUTC8(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	timer := &windowTimer{TriggerMode: windowTimerTriggerTime, TriggerTime: "09:00"}
	// 2026-07-09 17:30 in UTC-7 is 2026-07-10 08:30 in UTC+8.
	now := time.Date(2026, 7, 9, 17, 30, 0, 0, time.FixedZone("UTC-7", -7*60*60))
	want := time.Date(2026, 7, 10, 9, 0, 0, 0, loc)
	if got := computeNextFireAtFrom(timer, now, loc); !got.Equal(want) || got.Location() != loc {
		t.Fatalf("computeNextFireAtFrom()=%v (%v), want %v (%v)", got, got.Location(), want, want.Location())
	}
}

func TestComputeNextDailyFireUsesUTC8(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	timer := &windowTimer{
		TriggerTime: "09:00",
		LoopMode:    windowTimerLoopDaily,
	}
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, loc)
	want := time.Date(2026, 7, 11, 9, 0, 0, 0, loc)
	if got := computeNextLoopFireAtFrom(timer, time.Time{}, now, loc); !got.Equal(want) {
		t.Fatalf("computeNextLoopFireAtFrom()=%v, want %v", got, want)
	}
}

func TestParseTimerTimezone(t *testing.T) {
	for _, tc := range []struct {
		value      string
		wantOffset int
	}{
		{"UTC+8", 8 * 60 * 60},
		{"+08:00", 8 * 60 * 60},
		{"UTC-7:30", -(7*60*60 + 30*60)},
		{"Asia/Shanghai", 8 * 60 * 60},
	} {
		loc, err := parseTimerTimezone(tc.value)
		if err != nil {
			t.Fatalf("parseTimerTimezone(%q): %v", tc.value, err)
		}
		_, offset := time.Date(2026, 7, 10, 12, 0, 0, 0, loc).Zone()
		if offset != tc.wantOffset {
			t.Fatalf("parseTimerTimezone(%q) offset=%d, want %d", tc.value, offset, tc.wantOffset)
		}
	}
	for _, value := range []string{"", "UTC+15", "UTC+08:90", "Mars/Olympus"} {
		if _, err := parseTimerTimezone(value); err == nil {
			t.Fatalf("parseTimerTimezone(%q) expected error", value)
		}
	}
}

func TestSetTimerTimezoneReschedulesWallClockTimers(t *testing.T) {
	withTimerHistoryRoot(t)
	originalDelayFire := time.Now().Add(45 * time.Minute).UTC()
	timers := []*windowTimer{
		{
			ID:          "wall-clock",
			TriggerMode: windowTimerTriggerTime,
			TriggerTime: "09:00",
			Enabled:     true,
			NextFireAt:  time.Now().Add(24 * time.Hour),
		},
		{
			ID:          "duration",
			TriggerMode: windowTimerTriggerDelay,
			Enabled:     true,
			NextFireAt:  originalDelayFire,
		},
	}
	if err := saveWindowTimers(timers); err != nil {
		t.Fatal(err)
	}
	if err := setTimerTimezone("UTC-7"); err != nil {
		t.Fatal(err)
	}

	if got := timerTimezoneSetting(loadAppConfig()); got != "UTC-7" {
		t.Fatalf("timer timezone=%q, want UTC-7", got)
	}
	got := loadWindowTimers()
	if len(got) != 2 {
		t.Fatalf("loaded %d timers, want 2", len(got))
	}
	wallClock := got[0].NextFireAt.In(time.FixedZone("UTC-7", -7*60*60))
	if wallClock.Hour() != 9 || wallClock.Minute() != 0 || !wallClock.After(time.Now()) {
		t.Fatalf("wall-clock timer was not rescheduled to the next 09:00 UTC-7: %v", wallClock)
	}
	if !got[1].NextFireAt.Equal(originalDelayFire) {
		t.Fatalf("duration timer changed from %v to %v", originalDelayFire, got[1].NextFireAt)
	}
}

func TestTimerTimezoneDefaultIsUTC8(t *testing.T) {
	if got := timerTimezoneSetting(appConfig{}); got != "UTC+8" {
		t.Fatalf("timerTimezoneSetting(default)=%q, want UTC+8", got)
	}
}

func TestTimerDisplaySummaryUsesUTC8(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	timers := []*windowTimer{{
		Enabled:    true,
		NextFireAt: time.Date(2026, 7, 10, 1, 15, 0, 0, time.UTC),
	}}
	if got, want := timerDisplaySummaryIn(timers, loc), "[1]09:15"; got != want {
		t.Fatalf("timerDisplaySummary()=%q, want %q", got, want)
	}
}

func TestTimerHistoryDedup(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Minute)
	var entries []*windowTimerHistoryEntry
	entries = upsertHistory(entries, "@1", "make test", "5m", "", "0", true, now)
	entries = upsertHistory(entries, "@1", "make test", "5m", "", "0", true, later)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after same-combo upsert, got %d", len(entries))
	}
	if entries[0].UseCount != 2 {
		t.Fatalf("expected UseCount=2, got %d", entries[0].UseCount)
	}
	if !entries[0].LastUsedAt.Equal(later) {
		t.Fatalf("expected LastUsedAt refreshed to latest, got %v", entries[0].LastUsedAt)
	}
}

// withTimerHistoryRoot isolates the history file under a temp HOME.
func withTimerHistoryRoot(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-tracker"), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestTimerHistoryAll(t *testing.T) {
	withTimerHistoryRoot(t)
	now := time.Now()
	entries := []*windowTimerHistoryEntry{}
	entries = upsertHistory(entries, "@1", "older", "5m", "", "0", true, now.Add(-2*time.Hour))
	entries = upsertHistory(entries, "@1", "newer", "5m", "", "0", true, now)
	// Same combo from another window: deduped, most recent survives.
	entries = upsertHistory(entries, "@2", "older", "5m", "", "0", true, now.Add(-time.Hour))
	if err := saveTimerHistory(entries); err != nil {
		t.Fatal(err)
	}
	got := timerHistoryAll()
	if len(got) != 2 {
		t.Fatalf("expected 2 deduped entries across windows, got %d", len(got))
	}
	if got[0].Content != "newer" || got[1].Content != "older" {
		t.Fatalf("expected most-recent-first, got %q,%q", got[0].Content, got[1].Content)
	}
	if got[1].WindowID != "@2" {
		t.Fatalf("dedup should keep the most recent copy, got window %q", got[1].WindowID)
	}
}

func TestDeleteTimerHistoryCombo(t *testing.T) {
	withTimerHistoryRoot(t)
	now := time.Now()
	entries := []*windowTimerHistoryEntry{}
	entries = upsertHistory(entries, "@1", "keep", "5m", "", "0", true, now)
	entries = upsertHistory(entries, "@1", "drop", "13:00", "daily", "0", false, now)
	entries = upsertHistory(entries, "@2", "drop", "13:00", "daily", "0", false, now)
	if err := saveTimerHistory(entries); err != nil {
		t.Fatal(err)
	}
	if err := deleteTimerHistoryCombo("drop", "13:00", "daily", "0", false); err != nil {
		t.Fatal(err)
	}
	got := timerHistoryAll()
	if len(got) != 1 || got[0].Content != "keep" {
		t.Fatalf("expected only 'keep' to remain across all windows, got %+v", got)
	}
}

func TestDefaultSnippetNameFromContent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"make test", "make-test"},
		{"Deploy NOW", "deploy-now"},
		{"git commit -m 'wip'\nsecond line", "git-commit--m-wip"},
		{"!!!", "snippet"},
		{"", "snippet"},
	}
	for _, c := range cases {
		if got := defaultSnippetNameFromContent(c.in); got != c.want {
			t.Errorf("defaultSnippetNameFromContent(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestTimerHistoryDistinctTrigger(t *testing.T) {
	now := time.Now()
	var entries []*windowTimerHistoryEntry
	entries = upsertHistory(entries, "@1", "make test", "5m", "", "0", true, now)
	entries = upsertHistory(entries, "@1", "make test", "13:00", "daily", "0", true, now)
	if len(entries) != 2 {
		t.Fatalf("expected 2 distinct entries (different trigger), got %d", len(entries))
	}
}
