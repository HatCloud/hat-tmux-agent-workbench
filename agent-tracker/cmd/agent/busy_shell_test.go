package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/david/agent-tracker/internal/agentclient"
	"github.com/david/agent-tracker/internal/agentclient/claude"
)

// TestBusyShellPatternsJSONThreeState pins that the three-state semantics
// survive a real JSON round-trip — the disable path ([] → non-nil empty
// pointer) is the crux and must not silently degrade to "absent → default".
func TestBusyShellPatternsJSONThreeState(t *testing.T) {
	// absent key → nil pointer → default
	var cfg appConfig
	if err := json.Unmarshal([]byte(`{}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.BusyShellPatterns != nil {
		t.Fatalf("absent key must leave nil pointer, got %v", *cfg.BusyShellPatterns)
	}
	if !reflect.DeepEqual(busyShellPatternsSetting(cfg), claude.DefaultBusyShellPatterns) {
		t.Fatal("absent → want default")
	}

	// explicit [] → non-nil empty pointer → disabled
	cfg = appConfig{}
	if err := json.Unmarshal([]byte(`{"busy_shell_patterns":[]}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.BusyShellPatterns == nil {
		t.Fatal("explicit [] must yield a non-nil pointer (disable path)")
	}
	if got := busyShellPatternsSetting(cfg); len(got) != 0 {
		t.Fatalf("explicit [] → want disabled (empty), got %v", got)
	}

	// non-empty → replace
	cfg = appConfig{}
	if err := json.Unmarshal([]byte(`{"busy_shell_patterns":["only-this"]}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := busyShellPatternsSetting(cfg); !reflect.DeepEqual(got, []string{"only-this"}) {
		t.Fatalf("non-empty → want [only-this], got %v", got)
	}
}

// TestBusyShellPatternsSetting pins the three-state config semantics (AC-2).
func TestBusyShellPatternsSetting(t *testing.T) {
	// ① nil → built-in default
	got := busyShellPatternsSetting(appConfig{})
	if !reflect.DeepEqual(got, claude.DefaultBusyShellPatterns) {
		t.Fatalf("nil field → want default %v, got %v", claude.DefaultBusyShellPatterns, got)
	}
	// returned copy must be independent of the shared default
	if len(got) > 0 {
		got[0] = "MUTATED"
		if claude.DefaultBusyShellPatterns[0] == "MUTATED" {
			t.Fatal("busyShellPatternsSetting must return a copy, not the shared default slice")
		}
	}

	// ② non-empty → replace (not merge): result is exactly the configured list
	custom := []string{"foo"}
	got = busyShellPatternsSetting(appConfig{BusyShellPatterns: &custom})
	if !reflect.DeepEqual(got, []string{"foo"}) {
		t.Fatalf("non-empty field → want exactly [foo] (replace), got %v", got)
	}
	for _, p := range got {
		if p == "hl-run" {
			t.Fatal("non-empty config must REPLACE defaults, not merge them in")
		}
	}

	// ③ explicit empty [] → disabled (empty result)
	empty := []string{}
	got = busyShellPatternsSetting(appConfig{BusyShellPatterns: &empty})
	if len(got) != 0 {
		t.Fatalf("explicit empty [] → want disabled (empty), got %v", got)
	}
}

// TestInjectBusyShellPatterns pins the SideCar injection glue (AC-3①): the
// resolved patterns land under the exact key the adapter reads.
func TestInjectBusyShellPatterns(t *testing.T) {
	// default injection
	idx := &agentclient.Index{}
	injectBusyShellPatterns(idx, appConfig{})
	v, ok := idx.SideCar[claude.BusyShellSideCarKey]
	if !ok {
		t.Fatalf("SideCar key %q not set", claude.BusyShellSideCarKey)
	}
	if !reflect.DeepEqual(v, append([]string(nil), claude.DefaultBusyShellPatterns...)) {
		t.Fatalf("default injection = %v, want default patterns", v)
	}

	// replace injection
	custom := []string{"bar"}
	idx = &agentclient.Index{SideCar: map[string]any{}}
	injectBusyShellPatterns(idx, appConfig{BusyShellPatterns: &custom})
	if !reflect.DeepEqual(idx.SideCar[claude.BusyShellSideCarKey], []string{"bar"}) {
		t.Fatalf("replace injection = %v, want [bar]", idx.SideCar[claude.BusyShellSideCarKey])
	}

	// disabled injection (empty slice present, not nil)
	empty := []string{}
	idx = &agentclient.Index{SideCar: map[string]any{}}
	injectBusyShellPatterns(idx, appConfig{BusyShellPatterns: &empty})
	got, _ := idx.SideCar[claude.BusyShellSideCarKey].([]string)
	if got == nil || len(got) != 0 {
		t.Fatalf("disabled injection = %v, want non-nil empty slice", got)
	}
}
