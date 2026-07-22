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

func TestLayoutDefaultSetting(t *testing.T) {
	if got := layoutDefaultSetting(appConfig{}); got != "landscape" {
		t.Fatalf("default layout = %q, want landscape", got)
	}
	if got := layoutDefaultSetting(appConfig{LayoutDefault: "portrait"}); got != "portrait" {
		t.Fatalf("portrait layout = %q, want portrait", got)
	}
	if got := layoutDefaultSetting(appConfig{LayoutDefault: "auto"}); got != "auto" {
		t.Fatalf("legacy auto layout = %q, want auto", got)
	}
	auto := true
	if got := layoutDefaultSetting(appConfig{LayoutAutoResize: &auto}); got != "auto" {
		t.Fatalf("auto-resize layout = %q, want auto", got)
	}
	auto = false
	if got := layoutDefaultSetting(appConfig{LayoutDefault: "auto", LayoutAutoResize: &auto}); got != "landscape" {
		t.Fatalf("explicitly disabled auto-resize layout = %q, want landscape", got)
	}
}

func TestWindowResizeSettingsDefaultsAndValidation(t *testing.T) {
	cfg := appConfig{}
	if got := layoutOrientationSetting(cfg); got != "landscape" {
		t.Errorf("default orientation = %q, want landscape", got)
	}
	if layoutAutoResizeSetting(cfg) {
		t.Error("auto resize should default off")
	}
	if got := layoutMainPercentSetting(cfg); got != 55 {
		t.Errorf("default main percent = %d, want 55", got)
	}
	if layoutThirdPaneSetting(cfg) {
		t.Error("third pane should default off")
	}
	if got := layoutSideTopPercentSetting(cfg); got != 75 {
		t.Errorf("default side top percent = %d, want 75", got)
	}

	if got := layoutMainPercentSetting(appConfig{LayoutMainPercent: 60}); got != 60 {
		t.Errorf("configured main percent = %d, want 60", got)
	}
	if got := layoutMainPercentSetting(appConfig{LayoutMainPercent: 10}); got != 55 {
		t.Errorf("invalid main percent = %d, want fallback 55", got)
	}
	if got := layoutSideTopPercentSetting(appConfig{LayoutSideTopPercent: 70}); got != 70 {
		t.Errorf("configured side top percent = %d, want 70", got)
	}
	if got := layoutSideTopPercentSetting(appConfig{LayoutSideTopPercent: 99}); got != 75 {
		t.Errorf("invalid side top percent = %d, want fallback 75", got)
	}
	third := true
	if !layoutThirdPaneSetting(appConfig{LayoutThirdPane: &third}) {
		t.Error("configured third pane should be on")
	}
}

func TestPersistLayoutOrientationKeepsLegacyAutoResizeEnabled(t *testing.T) {
	cfg := appConfig{LayoutDefault: "auto"}
	persistLayoutOrientation(&cfg, "portrait")
	if cfg.LayoutDefault != "portrait" {
		t.Fatalf("layout default = %q, want portrait", cfg.LayoutDefault)
	}
	if cfg.LayoutAutoResize == nil || !*cfg.LayoutAutoResize {
		t.Fatal("choosing an orientation must preserve legacy auto-resize")
	}
}

func TestShouldReflowOrientationDoesNotCorrectManualRatio(t *testing.T) {
	if shouldReflowOrientation(false, "landscape", "portrait") {
		t.Error("disabled auto-resize must never reflow")
	}
	if shouldReflowOrientation(true, "landscape", "landscape") {
		t.Error("same orientation must not reflow just to correct pane proportions")
	}
	if !shouldReflowOrientation(true, "landscape", "portrait") {
		t.Error("enabled auto-resize should reflow when orientation changes")
	}
}

func TestSettingsIncludesWindowResizeCategory(t *testing.T) {
	panel := newSettingsPanelModel()
	for _, entry := range panel.entries {
		if entry.Mode == paletteModeWindowResize {
			return
		}
	}
	t.Fatal("Settings should include Window & Resize category")
}

func TestPaletteOpenModeWindowResize(t *testing.T) {
	for _, name := range []string{"window-resize", "windowresize", "resize"} {
		if got := paletteOpenModeToState(name); got != paletteModeWindowResize {
			t.Errorf("paletteOpenModeToState(%q) = %v, want Window & Resize", name, got)
		}
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
