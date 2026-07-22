package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/david/agent-tracker/internal/agentclient"
)

func writeNamingDB(t *testing.T, home, name string) {
	t.Helper()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	query := `create table threads (` +
		`id text primary key, title text not null, name text, cwd text not null, model text, ` +
		`rollout_path text not null, source text not null, updated_at_ms integer);` +
		`insert into threads values ('thread-1','default prompt title',` + shellSQLString(name) +
		`, '/proj', 'gpt-5.6-luna', '/tmp/rollout.jsonl', 'cli', 1);`
	cmd := exec.Command("sqlite3", filepath.Join(dir, "state_5.sqlite"), query)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlite fixture: %v: %s", err, out)
	}
}

func writeSessionIndex(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session_index.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSessionNamingUsesLatestSessionIndexRecord(t *testing.T) {
	home := t.TempDir()
	writeNamingDB(t, home, "Hat配置同步规范")
	writeSessionIndex(t, home,
		`{"id":"thread-1","thread_name":"Hat配置同步规范","updated_at":"2026-07-22T04:00:00Z"}`+"\n"+
			`{"id":"thread-1","thread_name":"测试","updated_at":"2026-07-22T04:31:23Z"}`+"\n")
	a := &Adapter{Home: home}
	state, err := a.SessionName(agentclient.LiveSession{SessionKey: "thread-1"})
	if err != nil || state.Value != "测试" || state.Source != agentclient.SessionNameUser {
		t.Fatalf("SessionName = %+v err=%v, want latest session index name", state, err)
	}
	meta, ok := a.queryThreads(nil, `id = 'thread-1'`)
	if !ok || meta.Name != "测试" {
		t.Fatalf("queryThreads = %+v ok=%v, want latest session index name", meta, ok)
	}
}

func TestSessionNamingInitialSessionIndexTitleIsNotAUserRename(t *testing.T) {
	home := t.TempDir()
	writeNamingDB(t, home, "")
	writeSessionIndex(t, home,
		`{"id":"thread-1","thread_name":"Hat配置同步规范","updated_at":"2026-07-22T04:00:00Z"}`+"\n")
	a := &Adapter{Home: home}
	state, err := a.SessionName(agentclient.LiveSession{SessionKey: "thread-1"})
	if err != nil || state.Value != "" || state.Source != agentclient.SessionNameNone || !state.Writable {
		t.Fatalf("SessionName = %+v err=%v, want initial index title treated as default", state, err)
	}
	meta, ok := a.queryThreads(nil, `id = 'thread-1'`)
	if !ok || meta.Title != "default prompt title" || meta.Name != "" || !meta.NameSupported {
		t.Fatalf("queryThreads = %+v ok=%v, want default title without a user name", meta, ok)
	}
}

func TestSessionNamingSessionIndexCanClearStaleDBName(t *testing.T) {
	home := t.TempDir()
	writeNamingDB(t, home, "stale DB name")
	writeSessionIndex(t, home,
		`{"id":"thread-1","thread_name":"old name","updated_at":"2026-07-22T04:00:00Z"}`+"\n"+
			`{"id":"thread-1","thread_name":"","updated_at":"2026-07-22T04:31:23Z"}`+"\n")
	state, err := (&Adapter{Home: home}).SessionName(agentclient.LiveSession{SessionKey: "thread-1"})
	if err != nil || state.Source != agentclient.SessionNameNone || !state.Writable {
		t.Fatalf("cleared state = %+v err=%v", state, err)
	}
}

func TestSessionNamingMalformedSessionIndexIsUnknown(t *testing.T) {
	home := t.TempDir()
	writeNamingDB(t, home, "must not leak through")
	writeSessionIndex(t, home, "{not-json}\n")
	state, err := (&Adapter{Home: home}).SessionName(agentclient.LiveSession{SessionKey: "thread-1"})
	if err != nil || state.Source != agentclient.SessionNameUnknown || state.Writable {
		t.Fatalf("malformed state = %+v err=%v", state, err)
	}
}

func TestSessionNamingSeparatesNameFromTitle(t *testing.T) {
	home := t.TempDir()
	writeNamingDB(t, home, "")
	called := false
	a := &Adapter{Home: home, setName: func(_ context.Context, id, name string) error {
		called = id == "thread-1" && name == "Meaningful Name"
		return nil
	}}
	s := agentclient.LiveSession{SessionKey: "thread-1"}
	state, err := a.SessionName(s)
	if err != nil || state.Source != agentclient.SessionNameNone || !state.Writable {
		t.Fatalf("unnamed state = %+v err=%v", state, err)
	}
	if err := a.SetSessionName(context.Background(), s, "Meaningful Name"); err != nil || !called {
		t.Fatalf("SetSessionName err=%v called=%v", err, called)
	}

	home = t.TempDir()
	writeNamingDB(t, home, "User Name")
	a = &Adapter{Home: home, setName: func(context.Context, string, string) error {
		t.Fatal("writer must not run for a named thread")
		return nil
	}}
	state, err = a.SessionName(s)
	if err != nil || state.Value != "User Name" || state.Source != agentclient.SessionNameUser {
		t.Fatalf("named state = %+v err=%v", state, err)
	}
	if err := a.SetSessionName(context.Background(), s, "overwrite"); err != agentclient.ErrSessionAlreadyNamed {
		t.Fatalf("overwrite err=%v, want already named", err)
	}
}

func TestSessionNamingLegacySchemaIsExplicitlyUnsupported(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	query := `create table threads (` +
		`id text primary key, title text not null, cwd text not null, model text, ` +
		`rollout_path text not null, source text not null, updated_at_ms integer);` +
		`insert into threads values ('thread-1','legacy title','/proj','model','/tmp/rollout.jsonl','cli',1);`
	cmd := exec.Command("sqlite3", filepath.Join(dir, "state_5.sqlite"), query)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlite fixture: %v: %s", err, out)
	}
	a := &Adapter{Home: home}
	state, err := a.SessionName(agentclient.LiveSession{SessionKey: "thread-1"})
	if err != nil || state.Source != agentclient.SessionNameUnknown || state.Writable {
		t.Fatalf("legacy state = %+v err=%v", state, err)
	}
	if err := a.SetSessionName(context.Background(), agentclient.LiveSession{SessionKey: "thread-1"}, "name"); err != agentclient.ErrSessionNameUnsupported {
		t.Fatalf("SetSessionName err=%v, want unsupported", err)
	}
}

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
