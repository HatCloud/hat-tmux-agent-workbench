package main

import (
	"testing"
	"time"
)

func TestPollIntervalDuration(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", 3 * time.Second},     // default
		{"1s", 1 * time.Second},   // preset
		{"3s", 3 * time.Second},   // preset
		{"10s", 10 * time.Second}, // preset
		{"5", 5 * time.Second},    // bare number = seconds
		{"500ms", 500 * time.Millisecond},
		{"garbage", 3 * time.Second},    // unparsable → fallback
		{"0", 500 * time.Millisecond},   // clamp min
		{"1ms", 500 * time.Millisecond}, // clamp min
		{"120s", 60 * time.Second},      // clamp max
		{"999", 60 * time.Second},       // clamp max (bare number)
	}
	for _, c := range cases {
		got := pollIntervalDuration(appConfig{PollInterval: c.raw})
		if got != c.want {
			t.Errorf("pollIntervalDuration(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestPollIntervalSetting(t *testing.T) {
	if got := pollIntervalSetting(appConfig{}); got != "3s" {
		t.Errorf("default pollIntervalSetting = %q, want 3s", got)
	}
	if got := pollIntervalSetting(appConfig{PollInterval: "10s"}); got != "10s" {
		t.Errorf("pollIntervalSetting = %q, want 10s", got)
	}
}

func TestStripDatePrefixSetting(t *testing.T) {
	if !stripDatePrefixSetting(appConfig{}) {
		t.Error("stripDatePrefixSetting default should be true")
	}
	f := false
	if stripDatePrefixSetting(appConfig{StripDatePrefix: &f}) {
		t.Error("stripDatePrefixSetting with false ptr should be false")
	}
}

func TestNewAgentPromptSetting(t *testing.T) {
	if !newAgentPromptSetting(appConfig{}) {
		t.Error("newAgentPromptSetting default should be true")
	}
	f := false
	if newAgentPromptSetting(appConfig{NewAgentPrompt: &f}) {
		t.Error("newAgentPromptSetting with false ptr should be false")
	}
}

func TestURLPickerDirsSetting(t *testing.T) {
	if urlPickerDirsSetting(appConfig{}) {
		t.Error("urlPickerDirsSetting default should be false")
	}
	tr := true
	if !urlPickerDirsSetting(appConfig{URLPickerDirs: &tr}) {
		t.Error("urlPickerDirsSetting with true ptr should be true")
	}
}

func TestWindowNavSizeSetting(t *testing.T) {
	cases := map[string]string{
		"":         "wide", // default
		"wide":     "wide",
		"standard": "standard",
		"full":     "full",
		"bogus":    "wide", // unknown → default
	}
	for in, want := range cases {
		if got := windowNavSizeSetting(appConfig{WindowNavSize: in}); got != want {
			t.Errorf("windowNavSizeSetting(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaybeStripDatePrefix(t *testing.T) {
	if got := maybeStripDatePrefix("2026-07-09-open-source-refactor", true); got != "open-source-refactor" {
		t.Errorf("strip enabled = %q, want open-source-refactor", got)
	}
	if got := maybeStripDatePrefix("2026-07-09-open-source-refactor", false); got != "2026-07-09-open-source-refactor" {
		t.Errorf("strip disabled should keep the date, got %q", got)
	}
	if got := maybeStripDatePrefix("no-date-here", true); got != "no-date-here" {
		t.Errorf("no leading date should be unchanged, got %q", got)
	}
}
