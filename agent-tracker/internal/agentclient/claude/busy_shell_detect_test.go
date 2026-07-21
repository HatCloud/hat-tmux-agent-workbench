package claude

import (
	"testing"

	"github.com/david/agent-tracker/internal/agentclient"
)

// busyShellIndex builds an Index where the session pane (100) has a claude child
// (sessPID) which in turn spawned a background process running `bgCmd`.
func busyShellIndex(sessPID int, bgCmd string, sideCar map[string]any) *agentclient.Index {
	if sideCar == nil {
		sideCar = map[string]any{}
	}
	sideCar["claude.providers"] = map[string]string{}
	return &agentclient.Index{
		Children: map[int][]int{100: {sessPID}, sessPID: {5000}},
		Commands: map[int]string{sessPID: "claude", 5000: bgCmd},
		SideCar:  sideCar,
	}
}

const hlRunCmd = "bash /Users/x/.hat-env/bin/agent-hl/hl-run --engine claude"

// TestDetectBusyShellDefault (AC-3②/AC-4①): no SideCar key → falls back to the
// built-in default patterns; a shell session with hl-run in the subtree → busy.
func TestDetectBusyShellDefaultSideCar(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, 4242,
		`{"pid":4242,"name":"n","status":"shell","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		"")
	a := &Adapter{Home: home}
	idx := busyShellIndex(4242, hlRunCmd, nil) // no busy_shell_patterns key
	s, ok := a.Detect(idx, 100)
	if !ok {
		t.Fatal("expected detect")
	}
	if s.Status != agentclient.StatusBusy {
		t.Fatalf("shell + hl-run subtree (default patterns) → want busy, got %q", s.Status)
	}
}

// TestDetectBusyShellSideCarOverride (AC-3①): injected patterns are honored; an
// empty injected slice disables the feature (shell stays shell).
func TestDetectBusyShellSideCarOverride(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, 4242,
		`{"pid":4242,"name":"n","status":"shell","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		"")
	a := &Adapter{Home: home}

	// Empty slice → disabled → shell stays shell even with hl-run present.
	disabled := busyShellIndex(4242, hlRunCmd, map[string]any{BusyShellSideCarKey: []string{}})
	if s, _ := a.Detect(disabled, 100); s.Status != agentclient.StatusShell {
		t.Fatalf("empty patterns (disabled) → want shell, got %q", s.Status)
	}

	// Custom pattern that matches → busy.
	custom := busyShellIndex(4242, "python long_job.py", map[string]any{BusyShellSideCarKey: []string{"long_job"}})
	if s, _ := a.Detect(custom, 100); s.Status != agentclient.StatusBusy {
		t.Fatalf("custom pattern match → want busy, got %q", s.Status)
	}
}

// TestDetectBusyShellNoMatch (AC-4③): a shell session with no allowlisted
// process stays shell.
func TestDetectBusyShellNoMatch(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, 4242,
		`{"pid":4242,"name":"n","status":"shell","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		"")
	a := &Adapter{Home: home}
	idx := busyShellIndex(4242, "npm run dev", nil)
	if s, _ := a.Detect(idx, 100); s.Status != agentclient.StatusShell {
		t.Fatalf("shell + unrelated bg → want shell, got %q", s.Status)
	}
}

// TestDetectBusyShellLimitedWins (AC-4②): a session limited by a 429 keeps
// [L] even when an allowlisted background task is running (error > limited >
// bg-busy > idle).
func TestDetectBusyShellLimitedWins(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, 4242,
		`{"pid":4242,"name":"n","status":"shell","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		`{"type":"assistant","timestamp":"2030-01-01T00:00:00Z","error":"rate_limit","apiErrorStatus":429,`+
			`"message":{"content":[{"type":"text","text":"You've hit your session limit · resets 3am (UTC)"}]}}`+"\n")
	a := &Adapter{Home: home}
	idx := busyShellIndex(4242, hlRunCmd, nil)
	s, ok := a.Detect(idx, 100)
	if !ok {
		t.Fatal("expected detect")
	}
	if s.Status != agentclient.StatusLimited {
		t.Fatalf("limited must win over busy-shell, got %q", s.Status)
	}
}

// TestDetectBusyShellBusyIdempotent (AC-4④): an already-busy session is not
// touched by the override.
func TestDetectBusyShellBusyIdempotent(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, 4242,
		`{"pid":4242,"name":"n","status":"busy","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		"")
	a := &Adapter{Home: home}
	idx := busyShellIndex(4242, hlRunCmd, nil)
	if s, _ := a.Detect(idx, 100); s.Status != agentclient.StatusBusy {
		t.Fatalf("busy stays busy, got %q", s.Status)
	}
}
