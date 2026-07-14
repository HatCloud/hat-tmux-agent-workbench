package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionStatusOf(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if st, ok := sessionStatusOf(write("busy.json", `{"pid":1,"status":"busy"}`)); !ok || st != "busy" {
		t.Fatalf("busy: got %q ok=%v", st, ok)
	}
	if st, ok := sessionStatusOf(write("idle.json", `{"status":"idle","statusUpdatedAt":123}`)); !ok || st != "idle" {
		t.Fatalf("idle: got %q ok=%v", st, ok)
	}
	// Partial/mid-write content must fail readably (ok=false), so the watcher skips
	// it rather than treating it as a real (empty) status transition.
	if _, ok := sessionStatusOf(write("partial.json", `{"pid":1,"stat`)); ok {
		t.Fatalf("partial write should be unreadable")
	}
	// Missing file → not readable.
	if _, ok := sessionStatusOf(filepath.Join(dir, "nope.json")); ok {
		t.Fatalf("missing file should be unreadable")
	}
	// Valid JSON without a status field → readable, empty status.
	if st, ok := sessionStatusOf(write("nostatus.json", `{"pid":9}`)); !ok || st != "" {
		t.Fatalf("no-status: got %q ok=%v, want empty+ok", st, ok)
	}
}
