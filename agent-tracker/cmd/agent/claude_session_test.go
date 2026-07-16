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
	cases := map[string]string{"busy": "[B] ", "shell": "[I] ", "idle": "[I] ", "error": "[E] ", "BUSY": "[B] ", "": "", "weird": ""}
	for in, want := range cases {
		if got := statusTag(in); got != want {
			t.Fatalf("statusTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSyncNamesSingleFlight(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "sync.lock")
	releaseFirst, ok, err := acquireSyncNamesLock(lockPath, false)
	if err != nil || !ok {
		t.Fatalf("first lock claim = ok:%v err:%v, want acquired", ok, err)
	}
	defer releaseFirst()

	if releaseSecond, ok, err := acquireSyncNamesLock(lockPath, false); err != nil || ok {
		if releaseSecond != nil {
			releaseSecond()
		}
		t.Fatalf("overlapping lock claim = ok:%v err:%v, want coalesced", ok, err)
	}

	releaseFirst()
	releaseThird, ok, err := acquireSyncNamesLock(lockPath, false)
	if err != nil || !ok {
		t.Fatalf("claim after release = ok:%v err:%v, want acquired", ok, err)
	}
	releaseThird()
}

// TestSyncNamesLockBlockingWaits verifies the event-driven (--wait) path blocks
// for an in-flight holder and then acquires, rather than dropping like the
// non-blocking path.
func TestSyncNamesLockBlockingWaits(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "sync.lock")
	releaseFirst, ok, err := acquireSyncNamesLock(lockPath, false)
	if err != nil || !ok {
		t.Fatalf("first lock claim = ok:%v err:%v, want acquired", ok, err)
	}

	acquired := make(chan func())
	go func() {
		release, ok, err := acquireSyncNamesLock(lockPath, true) // blocks until released
		if err == nil && ok {
			acquired <- release
			return
		}
		acquired <- nil
	}()

	select {
	case <-acquired:
		t.Fatal("blocking acquire returned while the lock was held")
	case <-time.After(150 * time.Millisecond):
	}

	releaseFirst()
	select {
	case release := <-acquired:
		if release == nil {
			t.Fatal("blocking acquire failed after release")
		}
		release()
	case <-time.After(2 * time.Second):
		t.Fatal("blocking acquire never completed after release")
	}
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
	if got := providerForPID(cmd2.Process.Pid, map[string]string{url: "minimax"}); got != "anthropic" {
		t.Fatalf("providerForPID (no base url) = %q, want anthropic", got)
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
		{"shell", "in_progress", []reconcileAction{{command: "finish_task"}}}, // shell=turn 已结束，按 idle 走完成流程
		{"shell", "", nil},
		// idle：in_progress 才 finish_task（走 daemon 宽限）；否则 no-op。
		{"idle", "in_progress", []reconcileAction{{command: "finish_task"}}},
		{"idle", "", nil},
		// busy：未在跑则 start_task；在跑则 mark_asking{false}。
		{"busy", "", []reconcileAction{{command: "start_task"}}},
		{"busy", "in_progress", []reconcileAction{{command: "mark_asking", asking: false}}},
		// asking/waiting/paused：in_progress 仅 mark_asking{true}；非 in_progress 先 start_task 再 mark_asking{true}（保序）。
		{"asking", "in_progress", []reconcileAction{{command: "mark_asking", asking: true, attention: "asking"}}},
		{"asking", "", []reconcileAction{{command: "start_task"}, {command: "mark_asking", asking: true, attention: "asking"}}},
		{"waiting", "", []reconcileAction{{command: "start_task"}, {command: "mark_asking", asking: true, attention: "asking"}}},
		{"paused", "", []reconcileAction{{command: "start_task"}, {command: "mark_asking", asking: true, attention: "asking"}}},
		{"limited", "in_progress", []reconcileAction{{command: "mark_asking", asking: true, attention: "limited"}}},
		{"error", "in_progress", []reconcileAction{{command: "mark_asking", asking: true, attention: "error"}}},
		{"error", "", []reconcileAction{{command: "start_task"}, {command: "mark_asking", asking: true, attention: "error"}}},
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

func TestCodexStatusSnapshotTracksRecoverySignals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	now := time.Date(2026, 7, 11, 8, 0, 10, 0, time.UTC)
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"2026-07-11T08:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
			`{"timestamp":"2026-07-11T08:00:02Z","type":"event_msg","payload":{"type":"token_count"}}`+"\n"+
			`{"timestamp":"2026-07-11T08:00:04Z","type":"response_item","payload":{"type":"reasoning"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	snapshot := codexStatusSnapshotFromRolloutAt(path, now)
	if snapshot.Status != "busy" {
		t.Fatalf("snapshot.Status = %q, want busy", snapshot.Status)
	}
	wantStart := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	if !snapshot.LastTaskStartedAt.Equal(wantStart) {
		t.Fatalf("LastTaskStartedAt = %v, want %v", snapshot.LastTaskStartedAt, wantStart)
	}
	wantProgress := time.Date(2026, 7, 11, 8, 0, 4, 0, time.UTC)
	if !snapshot.LastProgressAt.Equal(wantProgress) {
		t.Fatalf("LastProgressAt = %v, want %v", snapshot.LastProgressAt, wantProgress)
	}
}

func TestResolveCodexStatusWithTurnError(t *testing.T) {
	start := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		snapshot codexStatusSnapshot
		errorAt  time.Time
		want     string
	}{
		{
			name:     "unresolved error overrides busy",
			snapshot: codexStatusSnapshot{Status: "busy", LastTaskStartedAt: start, LastProgressAt: start.Add(time.Second)},
			errorAt:  start.Add(2 * time.Second),
			want:     "error",
		},
		{
			name:     "task complete bookkeeping does not hide error",
			snapshot: codexStatusSnapshot{Status: "idle", LastTaskStartedAt: start, LastProgressAt: start.Add(time.Second)},
			errorAt:  start.Add(2 * time.Second),
			want:     "error",
		},
		{
			name:     "later model progress clears error",
			snapshot: codexStatusSnapshot{Status: "busy", LastTaskStartedAt: start, LastProgressAt: start.Add(3 * time.Second)},
			errorAt:  start.Add(2 * time.Second),
			want:     "busy",
		},
		{
			name:     "missing error preserves rollout status",
			snapshot: codexStatusSnapshot{Status: "asking", LastTaskStartedAt: start, LastProgressAt: start.Add(time.Second)},
			want:     "asking",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveCodexStatus(tc.snapshot, tc.errorAt); got != tc.want {
				t.Fatalf("resolveCodexStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLatestCodexTurnErrorFromDBFiltersNoise(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 unavailable")
	}
	db := filepath.Join(t.TempDir(), "logs.sqlite")
	schema := `
		create table logs (
			id integer primary key autoincrement,
			ts integer not null,
			ts_nanos integer not null,
			level text not null,
			target text not null,
			feedback_log_body text,
			thread_id text
		);
		insert into logs(ts,ts_nanos,level,target,feedback_log_body,thread_id) values
			(100,1,'INFO','codex_core::session::turn','Turn error: stale','thread-a'),
			(201,2,'DEBUG','codex_core::session::turn','Turn error: debug noise','thread-a'),
			(202,3,'INFO','codex_core::stream_events_utils','quoted Turn error: noise','thread-a'),
			(203,4,'INFO','codex_core::session::turn','Turn error: HTTP 529','thread-b'),
			(204,5,'INFO','codex_core::session::turn','prefix: Turn error: HTTP 529','thread-a');
	`
	if out, err := exec.Command("sqlite3", db, schema).CombinedOutput(); err != nil {
		t.Fatalf("create fixture db: %v: %s", err, out)
	}

	got, ok := latestCodexTurnErrorFromDB(db, "thread-a", time.Unix(200, 0))
	if !ok {
		t.Fatal("expected final turn error")
	}
	want := time.Unix(204, 5)
	if !got.Equal(want) {
		t.Fatalf("error timestamp = %v, want %v", got, want)
	}
	if _, ok := latestCodexTurnErrorFromDB(filepath.Join(t.TempDir(), "missing.sqlite"), "thread-a", time.Time{}); ok {
		t.Fatal("missing database must degrade without an error signal")
	}
}

func TestCodexStatusCacheLoadsThreadOnce(t *testing.T) {
	cache := codexStatusCache{}
	meta := codexThreadMeta{ID: "thread-a", RolloutPath: "/tmp/rollout-a.jsonl"}
	calls := 0
	load := func(codexThreadMeta) string {
		calls++
		return "error"
	}
	if got := cache.status(meta, load); got != "error" {
		t.Fatalf("first cached status = %q", got)
	}
	if got := cache.status(meta, load); got != "error" {
		t.Fatalf("second cached status = %q", got)
	}
	if calls != 1 {
		t.Fatalf("status loader calls = %d, want 1", calls)
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

// truncateWindowTitle bounds the title segment of a window name. Codex uses the
// whole prompt as its session title and `prefix ]` accepts pasted text, so an
// unbounded title reaches tmux's per-tick format expansion and leaks there
// (~6KB name measured at ~6MB/min of tmux heap growth). Counting is by rune, not
// byte, so CJK titles are not cut mid-character.
func TestTruncateWindowTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"under limit passes through", "agent-hl-sessions", 100, "agent-hl-sessions"},
		{"empty stays empty", "", 100, ""},
		{"exactly at limit is untouched", strings.Repeat("a", 100), 100, strings.Repeat("a", 100)},
		{"one over limit truncates with ellipsis", strings.Repeat("a", 101), 100, strings.Repeat("a", 99) + "…"},
		{"cjk counted by rune not byte", strings.Repeat("需", 120), 100, strings.Repeat("需", 99) + "…"},
		{"result never exceeds max runes", strings.Repeat("x", 5000), 100, strings.Repeat("x", 99) + "…"},
		{"non-positive max disables truncation", strings.Repeat("a", 200), 0, strings.Repeat("a", 200)},
	}
	for _, c := range cases {
		got := truncateWindowTitle(c.in, c.max)
		if got != c.want {
			t.Errorf("%s: truncateWindowTitle(len=%d, max=%d) = %q (%d runes), want %q (%d runes)",
				c.name, len([]rune(c.in)), c.max, got, len([]rune(got)), c.want, len([]rune(c.want)))
		}
		if c.max > 0 && len([]rune(got)) > c.max {
			t.Errorf("%s: result %d runes exceeds max %d", c.name, len([]rune(got)), c.max)
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
