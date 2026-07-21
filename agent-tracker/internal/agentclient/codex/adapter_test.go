package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCommandLooksLikeCodex(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"/opt/homebrew/bin/codex", true},
		{"node /Users/me/.nvm/bin/codex", true},
		{"/path/to/codex app-server", false},
		{"node /path/to/codex app-server", false},
		{"/opt/homebrew/bin/codex exec --json do stuff", false}, // headless worker，不驱动窗口
		{"/opt/homebrew/bin/codex resume abc", true},
		{"claude", false},
	}
	for _, c := range cases {
		if got := CommandLooksLikeCodex(c.command); got != c.want {
			t.Fatalf("CommandLooksLikeCodex(%q) = %v, want %v", c.command, got, c.want)
		}
	}
}

func TestParseProcFilesFromLsof(t *testing.T) {
	out := "p101\n" +
		"fcwd\n" +
		"n/Users/me/project\n" +
		"f12\n" +
		"n/Users/me/.codex/state_5.sqlite\n" +
		"f13\n" +
		"n/Users/me/.codex/sessions/2026/07/02/rollout-root.jsonl\n" +
		"f14\n" +
		"n/Users/me/.codex/sessions/2026/07/02/rollout-subagent.jsonl\n" +
		"p202\n" +
		"f9\n" +
		"n/Users/me/project/rollout-unrelated.jsonl\n" +
		"n/Users/me/.codex/sessions/2026/07/02/not-a-rollout.jsonl\n"
	got := parseProcFilesFromLsof(out)
	if len(got.Rollouts[101]) != 2 {
		t.Fatalf("pid 101 rollouts = %v, want 2 Codex rollout paths", got.Rollouts[101])
	}
	if got.CWDs[101] != "/Users/me/project" {
		t.Fatalf("pid 101 cwd = %q", got.CWDs[101])
	}
	if len(got.Rollouts[202]) != 0 {
		t.Fatalf("pid 202 rollouts = %v, want none", got.Rollouts[202])
	}

	// Legacy -Fn shape (no f records) must still yield rollouts.
	legacy := "p101\n" +
		"n/Users/me/.codex/sessions/2026/07/02/rollout-root.jsonl\n"
	if got := parseProcFilesFromLsof(legacy); len(got.Rollouts[101]) != 1 {
		t.Fatalf("legacy shape rollouts = %v", got.Rollouts[101])
	}
}

func TestPromptExtraction(t *testing.T) {
	payload := json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"},{"type":"input_text","text":"world"}]}`)
	if got := userPromptFromPayload(payload); got != "hello\nworld" {
		t.Fatalf("userPromptFromPayload = %q", got)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	body := `{"type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"  first prompt  "}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := promptFromRollout(path); got != "first prompt" {
		t.Fatalf("promptFromRollout = %q", got)
	}
}

func TestRateLimitsFromRollout(t *testing.T) {
	tc := func(primaryPct float64, resetsAt int64) string {
		return fmt.Sprintf(`{"timestamp":"2030-01-01T00:00:00Z","type":"event_msg","payload":{"type":"token_count",`+
			`"rate_limits":{"primary":{"used_percent":%g,"window_minutes":300,"resets_at":%d}}}}`,
			primaryPct, resetsAt)
	}
	other := `{"timestamp":"2030-01-01T00:00:01Z","type":"event_msg","payload":{"type":"agent_message"}}`
	nullRL := `{"timestamp":"2030-01-01T00:00:02Z","type":"event_msg","payload":{"type":"token_count"}}`

	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	body := tc(10, 100) + "\n" + tc(80, 200) + "\n" + other + "\n" + nullRL + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rl, ok := rateLimitsFromRollout(path)
	if !ok || rl.Primary == nil || rl.Primary.ResetsAt != 200 || rl.Primary.UsedPercent != 80 {
		t.Fatalf("unexpected snapshot: %+v ok=%v", rl, ok)
	}

	if _, ok := rateLimitsFromRollout(filepath.Join(t.TempDir(), "missing.jsonl")); ok {
		t.Fatal("missing file should not parse")
	}
}

// TestExhaustedResetAtViaWindows pins the [L] rule: only an actually exhausted
// window (>=95%) blocks, taking the latest reset among exhausted ones.
func TestExhaustedResetAtViaWindows(t *testing.T) {
	now := time.Unix(1_783_600_000, 0)
	future := func(d time.Duration) int64 { return now.Add(d).Unix() }
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	body := fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{`+
		`"primary":{"used_percent":11,"window_minutes":300,"resets_at":%d},`+
		`"secondary":{"used_percent":99,"window_minutes":10080,"resets_at":%d}}}}`,
		future(2*time.Hour), future(20*time.Hour)) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Home: t.TempDir()}
	got, ok := a.exhaustedResetAt(path, now)
	if !ok || got.Unix() != future(20*time.Hour) {
		t.Fatalf("exhausted secondary should drive [L]: got %v ok=%v", got, ok)
	}
}
