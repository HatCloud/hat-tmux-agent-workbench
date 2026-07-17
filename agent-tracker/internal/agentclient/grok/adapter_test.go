package grok

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/david/agent-tracker/internal/agentclient"
)

func TestCommandLooksLikeGrokInteractive(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"grok", true},
		{"/usr/local/bin/grok", true},
		{"grok-macos-aarc", true},
		{"/opt/bin/grok-macos-aarc --cwd /tmp", true},
		{"grok -p hello", false},
		{"grok --single hi", false},
		{"grok agent do stuff", false},
		{"codex", false},
	}
	for _, c := range cases {
		if got := CommandLooksLikeGrokInteractive(c.cmd); got != c.want {
			t.Errorf("cmd %q: got %v want %v", c.cmd, got, c.want)
		}
	}
}

func TestStatusFromEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	// busy
	os.WriteFile(path, []byte(`{"type":"turn_started"}
{"type":"phase_changed","phase":"tool_execution"}
`), 0644)
	if got := statusFromEvents(path); got != agentclient.StatusBusy {
		t.Fatalf("busy: got %s", got)
	}
	// idle after turn_ended
	os.WriteFile(path, []byte(`{"type":"turn_started"}
{"type":"phase_changed","phase":"streaming_text"}
{"type":"turn_ended"}
{"type":"phase_changed","phase":"streaming_text"}
`), 0644)
	// residual phase after turn_ended → design: idle when sawTurnEnded
	if got := statusFromEvents(path); got != agentclient.StatusIdle {
		t.Fatalf("idle: got %s", got)
	}
	// true asking: open permission gate, not yet resolved
	os.WriteFile(path, []byte(`{"type":"phase_changed","phase":"permission_prompt"}
{"type":"permission_requested","tool_name":"run_terminal_command"}
`), 0644)
	if got := statusFromEvents(path); got != agentclient.StatusAsking {
		t.Fatalf("asking: got %s", got)
	}
	// auto-allow tool cycle (wait_ms:0) must NOT stick on asking — this is the
	// queue-while-busy false-[?] bug: every tool flashes permission_prompt.
	os.WriteFile(path, []byte(`{"type":"turn_started"}
{"type":"phase_changed","phase":"tool_execution"}
{"type":"tool_started","tool_name":"read_file"}
{"type":"phase_changed","phase":"permission_prompt"}
{"type":"permission_requested","tool_name":"read_file"}
{"type":"permission_resolved","tool_name":"read_file","decision":"allow","wait_ms":0}
{"type":"phase_changed","phase":"tool_execution"}
{"type":"tool_completed","tool_name":"read_file","duration_ms":0,"outcome":"success"}
{"type":"phase_changed","phase":"streaming_text"}
`), 0644)
	if got := statusFromEvents(path); got != agentclient.StatusBusy {
		t.Fatalf("post auto-allow tool cycle should be busy, got %s", got)
	}
	// missing file
	if got := statusFromEvents(filepath.Join(dir, "nope.jsonl")); got != agentclient.StatusUnknown {
		t.Fatalf("missing: got %s", got)
	}
}

func TestDetect_WithFixture(t *testing.T) {
	home := t.TempDir()
	// active session
	os.WriteFile(filepath.Join(home, "active_sessions.json"), []byte(`[
	  {"session_id":"sid-1","pid":4242,"cwd":"/proj"}
	]`), 0644)
	sess := filepath.Join(home, "sessions", "enc", "sid-1")
	os.MkdirAll(sess, 0755)
	os.WriteFile(filepath.Join(sess, "summary.json"), []byte(`{
	  "generated_title":"Hello Grok","current_model_id":"grok-4.5"
	}`), 0644)
	os.WriteFile(filepath.Join(sess, "events.jsonl"), []byte(`{"type":"turn_ended"}
{"type":"phase_changed","phase":"streaming_text"}
`), 0644)

	a := &Adapter{Home: home}
	idx := &agentclient.Index{
		Children: map[int][]int{100: {4242}},
		Commands: map[int]string{4242: "/opt/bin/grok"},
	}
	s, ok := a.Detect(idx, 100)
	if !ok {
		t.Fatal("expected detect")
	}
	if s.Client != "grok" || s.Title != "Hello Grok" || s.Model != "grok-4.5" {
		t.Fatalf("session: %+v", s)
	}
	if s.Status != agentclient.StatusIdle {
		t.Fatalf("status %s", s.Status)
	}
}

func TestDetect_HeadlessRejected(t *testing.T) {
	a := &Adapter{Home: t.TempDir()}
	idx := &agentclient.Index{
		Commands: map[int]string{1: "grok -p hi"},
	}
	if _, ok := a.Detect(idx, 1); ok {
		t.Fatal("headless must not detect")
	}
}

func TestRetryOff(t *testing.T) {
	a := &Adapter{}
	if a.RetryPolicy().Enabled {
		t.Fatal("retry must be off")
	}
}
