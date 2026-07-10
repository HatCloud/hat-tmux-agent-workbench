package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatusTag(t *testing.T) {
	cases := map[string]string{"busy": "[B] ", "idle": "[I] ", "BUSY": "[B] ", "": "", "weird": ""}
	for in, want := range cases {
		if got := statusTag(in); got != want {
			t.Fatalf("statusTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSyncNamesSingleFlight(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "sync.lock")
	releaseFirst, ok, err := acquireSyncNamesLock(lockPath)
	if err != nil || !ok {
		t.Fatalf("first lock claim = ok:%v err:%v, want acquired", ok, err)
	}
	defer releaseFirst()

	if releaseSecond, ok, err := acquireSyncNamesLock(lockPath); err != nil || ok {
		if releaseSecond != nil {
			releaseSecond()
		}
		t.Fatalf("overlapping lock claim = ok:%v err:%v, want coalesced", ok, err)
	}

	releaseFirst()
	releaseThird, ok, err := acquireSyncNamesLock(lockPath)
	if err != nil || !ok {
		t.Fatalf("claim after release = ok:%v err:%v, want acquired", ok, err)
	}
	releaseThird()
}

func TestSyncNamesPeriodicDue(t *testing.T) {
	stampPath := filepath.Join(t.TempDir(), "sync.last")
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	if !syncNamesPeriodicDue(stampPath, now, 5*time.Second) {
		t.Fatal("missing stamp should be due")
	}
	if err := markSyncNamesStarted(stampPath, now); err != nil {
		t.Fatal(err)
	}
	if syncNamesPeriodicDue(stampPath, now.Add(4*time.Second), 5*time.Second) {
		t.Fatal("periodic sync should be coalesced before 5 seconds")
	}
	if !syncNamesPeriodicDue(stampPath, now.Add(5*time.Second), 5*time.Second) {
		t.Fatal("periodic sync should be due at 5 seconds")
	}
}

// providerForPID reads a process's launch-time env via `ps eww` and maps
// ANTHROPIC_BASE_URL back to a provider name. Start a real process whose initial
// env carries a known base URL and assert the mapping resolves it.
func TestProviderForPID(t *testing.T) {
	const url = "https://api.minimaxi.com/anthropic"
	// Inherit the full environment (as a real claude launched from a shell does);
	// macOS `ps eww` only exposes env for processes with a full environment block.
	cmd := exec.Command("/bin/sh", "-c", "exec sleep 5")
	cmd.Env = append(os.Environ(), "ANTHROPIC_BASE_URL="+url)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start helper process: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	time.Sleep(150 * time.Millisecond)

	got := providerForPID(cmd.Process.Pid, map[string]string{url: "minimax"})
	if got != "minimax" {
		t.Skipf("ps eww does not expose helper env on this host; providerForPID = %q", got)
	}

	// A process with no ANTHROPIC_BASE_URL resolves to the official default.
	cmd2 := exec.Command("/bin/sh", "-c", "unset ANTHROPIC_BASE_URL; exec sleep 5")
	cmd2.Env = os.Environ()
	if err := cmd2.Start(); err != nil {
		t.Skipf("cannot start helper process: %v", err)
	}
	defer func() { _ = cmd2.Process.Kill() }()
	time.Sleep(150 * time.Millisecond)
	if got := providerForPID(cmd2.Process.Pid, map[string]string{url: "minimax"}); got != "official" {
		t.Fatalf("providerForPID (no base url) = %q, want official", got)
	}
}

func TestReconcileActions(t *testing.T) {
	eq := func(a, b []reconcileAction) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	cases := []struct {
		status, daemon string
		want           []reconcileAction
	}{
		// shell：活动态。in_progress 时发 mark_asking{false} 清 pending；否则 no-op（不凭空 start_task）。
		{"shell", "in_progress", []reconcileAction{{command: "mark_asking", asking: false}}},
		{"shell", "", nil},
		// idle：in_progress 才 finish_task（走 daemon 宽限）；否则 no-op。
		{"idle", "in_progress", []reconcileAction{{command: "finish_task"}}},
		{"idle", "", nil},
		// busy：未在跑则 start_task；在跑则 mark_asking{false}。
		{"busy", "", []reconcileAction{{command: "start_task"}}},
		{"busy", "in_progress", []reconcileAction{{command: "mark_asking", asking: false}}},
		// asking/waiting/paused：in_progress 仅 mark_asking{true}；非 in_progress 先 start_task 再 mark_asking{true}（保序）。
		{"asking", "in_progress", []reconcileAction{{command: "mark_asking", asking: true}}},
		{"asking", "", []reconcileAction{{command: "start_task"}, {command: "mark_asking", asking: true}}},
		{"waiting", "", []reconcileAction{{command: "start_task"}, {command: "mark_asking", asking: true}}},
		{"paused", "", []reconcileAction{{command: "start_task"}, {command: "mark_asking", asking: true}}},
		{"weird", "in_progress", nil},
		{"weird", "", nil},
	}
	for _, c := range cases {
		got := reconcileActions(c.status, c.daemon)
		if !eq(got, c.want) {
			t.Fatalf("reconcileActions(%q,%q) = %+v, want %+v", c.status, c.daemon, got, c.want)
		}
	}
}

func TestCodexStatusFromRollout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	now := time.Date(2026, 6, 22, 12, 0, 5, 0, time.UTC)
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:04Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:04Z","type":"event_msg","payload":{"type":"agent_message"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now); got != "busy" {
		t.Fatalf("codexStatusFromRollout(incomplete) = %q, want busy", got)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:01Z","type":"event_msg","payload":{"type":"task_complete"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now); got != "idle" {
		t.Fatalf("codexStatusFromRollout(complete) = %q, want idle", got)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:00Z","type":"response_item","payload":{"type":"custom_tool_call_output","output":"`+
			strings.Repeat("x", 4<<20)+`"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:01Z","type":"event_msg","payload":{"type":"task_complete"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now); got != "idle" {
		t.Fatalf("codexStatusFromRollout(oversized tool output) = %q, want idle", got)
	}
	for _, terminalEvent := range []string{"turn_aborted", "thread_rolled_back"} {
		if err := os.WriteFile(path, []byte(
			`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
				`{"timestamp":"2026-06-22T12:00:01Z","type":"event_msg","payload":{"type":"`+terminalEvent+`"}}`+"\n",
		), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := codexStatusFromRolloutAt(path, now); got != "idle" {
			t.Fatalf("codexStatusFromRollout(%s) = %q, want idle", terminalEvent, got)
		}
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now); got != "asking" {
		t.Fatalf("codexStatusFromRollout(request_user_input) = %q, want asking", got)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call-a"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-a"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now); got != "busy" {
		t.Fatalf("codexStatusFromRollout(answered request_user_input) = %q, want busy", got)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call-a"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:02Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call-b"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-b"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now); got != "asking" {
		t.Fatalf("codexStatusFromRollout(one of two requests answered) = %q, want asking", got)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-other"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now); got != "asking" {
		t.Fatalf("codexStatusFromRollout(idless request with unrelated output) = %q, want asking", got)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call-a"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-a"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:03Z","type":"event_msg","payload":{"type":"task_complete"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now); got != "idle" {
		t.Fatalf("codexStatusFromRollout(completed after request_user_input) = %q, want idle", got)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call-a"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:02Z","type":"event_msg","payload":{"type":"turn_aborted"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now); got != "idle" {
		t.Fatalf("codexStatusFromRollout(aborted after request_user_input) = %q, want idle", got)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:01Z","type":"event_msg","payload":{"type":"agent_message","phase":"commentary"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now); got != "busy" {
		t.Fatalf("codexStatusFromRollout(commentary while reasoning) = %q, want busy", got)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:01Z","type":"event_msg","payload":{"type":"agent_message","phase":"commentary"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:02Z","type":"response_item","payload":{"type":"function_call","name":"exec_command"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now); got != "busy" {
		t.Fatalf("codexStatusFromRollout(tool after assistant) = %q, want busy", got)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"exec_command"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now.Add(2*time.Second)); got != "asking" {
		t.Fatalf("codexStatusFromRollout(waiting tool approval) = %q, want asking", got)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:00Z","type":"turn_context","payload":{"approvals_reviewer":"auto_review"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"exec_command"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now.Add(2*time.Second)); got != "busy" {
		t.Fatalf("codexStatusFromRollout(auto-reviewed tool approval) = %q, want busy", got)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:00Z","type":"turn_context","payload":{"approvals_reviewer":"auto_review"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now.Add(2*time.Second)); got != "asking" {
		t.Fatalf("codexStatusFromRollout(auto-review request_user_input) = %q, want asking", got)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"exec_command"}}`+"\n"+
			`{"timestamp":"2026-06-22T12:00:07Z","type":"response_item","payload":{"type":"function_call_output"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexStatusFromRolloutAt(path, now.Add(2*time.Second)); got != "busy" {
		t.Fatalf("codexStatusFromRollout(tool output) = %q, want busy", got)
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
		{"claude", false},
	}
	for _, c := range cases {
		if got := commandLooksLikeCodex(c.command); got != c.want {
			t.Fatalf("commandLooksLikeCodex(%q) = %v, want %v", c.command, got, c.want)
		}
	}
}

func TestParseCodexRolloutsFromLsof(t *testing.T) {
	out := "p101\n" +
		"n/Users/me/.codex/state_5.sqlite\n" +
		"n/Users/me/.codex/sessions/2026/07/02/rollout-root.jsonl\n" +
		"n/Users/me/.codex/sessions/2026/07/02/rollout-subagent.jsonl\n" +
		"p202\n" +
		"n/Users/me/project/rollout-unrelated.jsonl\n" +
		"n/Users/me/.codex/sessions/2026/07/02/not-a-rollout.jsonl\n"
	got := parseCodexRolloutsFromLsof(out)
	if len(got[101]) != 2 {
		t.Fatalf("pid 101 rollouts = %v, want 2 Codex rollout paths", got[101])
	}
	if len(got[202]) != 0 {
		t.Fatalf("pid 202 rollouts = %v, want none", got[202])
	}
}

func TestAgentTitleForWindowNormalizesWithoutTruncation(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// No truncation: the full name survives at the data layer.
		{"abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"一二三四五六七八九十十一", "一二三四五六七八九十十一"},
		{"2026-07-09-open-source-refactor", "2026-07-09-open-source-refactor"},
		{"short title", "short title"},
		// Whitespace is still collapsed/trimmed.
		{"  many   spaces\there ", "many spaces here"},
	}
	for _, c := range cases {
		if got := agentTitleForWindow(c.in); got != c.want {
			t.Fatalf("agentTitleForWindow(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPromptExtraction(t *testing.T) {
	codexPayload := json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"},{"type":"input_text","text":"world"}]}`)
	if got := userPromptFromCodexPayload(codexPayload); got != "hello\nworld" {
		t.Fatalf("userPromptFromCodexPayload = %q", got)
	}
	claudeContent := json.RawMessage(`"raw claude prompt"`)
	if got := textFromJSONContent(claudeContent); got != "raw claude prompt" {
		t.Fatalf("textFromJSONContent(string) = %q", got)
	}
}

// parseSSHHost extracts the destination host from an ssh command line (the full
// `ps -o args=` string including the leading program name). It must be flag-aware:
// flags that take an argument consume the following token, so the destination is
// the first non-option token after the program name, with user@ and :port stripped.
func TestParseSSHHost(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ssh mini", "mini"},
		{"ssh user@host -p 2222", "host"},
		{"ssh -i ~/k.pem host", "host"},
		{"ssh -J bastion prod", "prod"},
		{"ssh mini ls -la", "mini"},
		{"ssh -p 22 user@1.2.3.4", "1.2.3.4"},
		{"ssh", ""},
		{"", ""},
		// extras: option terminator + bracketed ipv6 + bare ipv6 left intact +
		// embedded ipv4 port + user@ before bracketed ipv6.
		{"ssh -- host", "host"},
		{"ssh [::1]:22", "::1"},
		{"ssh 2001:db8::1", "2001:db8::1"},
		{"ssh 1.2.3.4:22", "1.2.3.4"},
		{"ssh user@[::1]:22", "::1"},
	}
	for _, c := range cases {
		if got := parseSSHHost(c.in); got != c.want {
			t.Errorf("parseSSHHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// sanitizeWindowMarker strips ASCII control characters and the tmux format
// character '#' so a malformed alias/hostname can't inject into the status line.
// Normal markers (emoji + ascii) must pass through unchanged.
func TestSanitizeWindowMarker(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"🌐 mini", "🌐 mini"},
		{"mini", "mini"},
		{"ho#st", "host"},
		{"#{evil}", "{evil}"},
		{"a\x01b\x1fc", "abc"},
		{"tab\tx", "tabx"},
		{"del\x7f", "del"},
		{"c1\u0085x", "c1x"},
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitizeWindowMarker(c.in); got != c.want {
			t.Errorf("sanitizeWindowMarker(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// sshProcessArgsFromSnapshot finds the first ssh process in the subtree rooted at
// a pane pid. tmux's pane_pid is the shell; ssh typed at a prompt is a child (or
// deeper), so the walk must descend, not just inspect the root.
func TestSSHProcessArgsFromSnapshot(t *testing.T) {
	// pid ppid args — a shell (1000) under tmux (500), with ssh (1001) as its
	// child, plus an unrelated ssh (2001) in another subtree.
	snap := strings.Join([]string{
		"  500     1 tmux",
		" 1000   500 -zsh",
		" 1001  1000 ssh mini",
		" 1002  1001 ssh -W somehost",
		" 2000     1 -zsh",
		" 2001  2000 ssh other@host -p 22",
	}, "\n")
	cases := []struct {
		name string
		root int
		want string
	}{
		{"ssh is shell child", 1000, "ssh mini"},
		{"root itself is ssh", 1001, "ssh mini"},
		{"ssh is grandchild", 500, "ssh mini"},
		{"other subtree", 2000, "ssh other@host -p 22"},
		{"root absent from snapshot", 9999, ""},
		{"no ssh in subtree", 2002, ""},
	}
	for _, c := range cases {
		if got := sshProcessArgsFromSnapshot(snap, c.root); got != c.want {
			t.Errorf("%s: sshProcessArgsFromSnapshot(root=%d) = %q, want %q", c.name, c.root, got, c.want)
		}
	}
}
