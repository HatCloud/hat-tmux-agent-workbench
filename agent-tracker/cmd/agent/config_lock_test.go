package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// readRawConfig reads the persisted config as a raw key map, failing the test if
// the file is missing or not valid JSON.
func readRawConfig(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(configPath())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("config is not valid JSON: %v\n%s", err, data)
	}
	return m
}

// TestUpdateAppConfigConcurrentDistinctFields asserts two goroutines writing
// different fields both persist without corrupting the JSON (the config lock
// serializes them so neither clobbers the other's field).
func TestUpdateAppConfigConcurrentDistinctFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := updateAppConfig(func(cfg *appConfig) { cfg.IconSet = "emoji" }); err != nil {
			t.Errorf("write icon_set: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := updateAppConfig(func(cfg *appConfig) { cfg.StatusPosition = "top" }); err != nil {
			t.Errorf("write status_position: %v", err)
		}
	}()
	wg.Wait()

	raw := readRawConfig(t)
	if got := string(raw["icon_set"]); got != `"emoji"` {
		t.Errorf("icon_set not preserved: got %q", got)
	}
	if got := string(raw["status_position"]); got != `"top"` {
		t.Errorf("status_position not preserved: got %q", got)
	}
}

// TestAcquireConfigLockPreemptsStale asserts a lock dir with an old mtime is
// preemptible, so a crashed holder never deadlocks writers.
func TestAcquireConfigLockPreemptsStale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lock := configLockDir()
	if err := os.MkdirAll(lock, 0o755); err != nil {
		t.Fatalf("seed lock: %v", err)
	}
	old := time.Now().Add(-configLockStale - time.Second)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- updateAppConfig(func(cfg *appConfig) { cfg.IconSet = "ascii" })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("update after stale lock: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("updateAppConfig blocked on stale lock (not preempted)")
	}
	if got := string(readRawConfig(t)["icon_set"]); got != `"ascii"` {
		t.Errorf("icon_set after preempt: got %q", got)
	}
}

// TestUpdateAppConfigPreservesUnknownKeys asserts keys not owned by appConfig
// survive a write (forward-compat with config written by other tools / newer
// versions).
func TestUpdateAppConfigPreservesUnknownKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := `{"icon_set":"nerd","future_field":{"nested":true},"another":42}` + "\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := updateAppConfig(func(cfg *appConfig) { cfg.IconSet = "emoji" }); err != nil {
		t.Fatalf("update: %v", err)
	}
	raw := readRawConfig(t)
	if got := string(raw["icon_set"]); got != `"emoji"` {
		t.Errorf("icon_set: got %q", got)
	}
	if _, ok := raw["future_field"]; !ok {
		t.Error("future_field (unknown) was dropped")
	}
	if got := string(raw["another"]); got != "42" {
		t.Errorf("another (unknown): got %q", got)
	}
}
