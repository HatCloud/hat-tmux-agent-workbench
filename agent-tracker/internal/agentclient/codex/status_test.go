package codex

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func statusFromRolloutAt(path string, now time.Time) string {
	return statusSnapshotFromRolloutAt(path, now).Status
}

func TestStatusFromRollout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	now := time.Date(2026, 6, 22, 12, 0, 5, 0, time.UTC)
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"timestamp":"2026-06-22T12:00:04Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:04Z","type":"event_msg","payload":{"type":"agent_message"}}` + "\n")
	if got := statusFromRolloutAt(path, now); got != "busy" {
		t.Fatalf("incomplete = %q, want busy", got)
	}
	write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:01Z","type":"event_msg","payload":{"type":"task_complete"}}` + "\n")
	if got := statusFromRolloutAt(path, now); got != "idle" {
		t.Fatalf("complete = %q, want idle", got)
	}
	write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:00Z","type":"response_item","payload":{"type":"custom_tool_call_output","output":"` +
		strings.Repeat("x", 4<<20) + `"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:01Z","type":"event_msg","payload":{"type":"task_complete"}}` + "\n")
	if got := statusFromRolloutAt(path, now); got != "idle" {
		t.Fatalf("oversized tool output = %q, want idle", got)
	}
	for _, terminalEvent := range []string{"turn_aborted", "thread_rolled_back"} {
		write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
			`{"timestamp":"2026-06-22T12:00:01Z","type":"event_msg","payload":{"type":"` + terminalEvent + `"}}` + "\n")
		if got := statusFromRolloutAt(path, now); got != "idle" {
			t.Fatalf("%s = %q, want idle", terminalEvent, got)
		}
	}
	write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input"}}` + "\n")
	if got := statusFromRolloutAt(path, now); got != "asking" {
		t.Fatalf("request_user_input = %q, want asking", got)
	}
	write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call-a"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-a"}}` + "\n")
	if got := statusFromRolloutAt(path, now); got != "busy" {
		t.Fatalf("answered request_user_input = %q, want busy", got)
	}
	write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call-a"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:02Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call-b"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-b"}}` + "\n")
	if got := statusFromRolloutAt(path, now); got != "asking" {
		t.Fatalf("one of two requests answered = %q, want asking", got)
	}
	write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-other"}}` + "\n")
	if got := statusFromRolloutAt(path, now); got != "asking" {
		t.Fatalf("idless request with unrelated output = %q, want asking", got)
	}
	write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call-a"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-a"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:03Z","type":"event_msg","payload":{"type":"task_complete"}}` + "\n")
	if got := statusFromRolloutAt(path, now); got != "idle" {
		t.Fatalf("completed after request_user_input = %q, want idle", got)
	}
	write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call-a"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:02Z","type":"event_msg","payload":{"type":"turn_aborted"}}` + "\n")
	if got := statusFromRolloutAt(path, now); got != "idle" {
		t.Fatalf("aborted after request_user_input = %q, want idle", got)
	}
	write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:01Z","type":"event_msg","payload":{"type":"agent_message","phase":"commentary"}}` + "\n")
	if got := statusFromRolloutAt(path, now); got != "busy" {
		t.Fatalf("commentary while reasoning = %q, want busy", got)
	}
	write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:01Z","type":"event_msg","payload":{"type":"agent_message","phase":"commentary"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:02Z","type":"response_item","payload":{"type":"function_call","name":"exec_command"}}` + "\n")
	if got := statusFromRolloutAt(path, now); got != "busy" {
		t.Fatalf("tool after assistant = %q, want busy", got)
	}
	write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"exec_command"}}` + "\n")
	if got := statusFromRolloutAt(path, now.Add(2*time.Second)); got != "asking" {
		t.Fatalf("waiting tool approval = %q, want asking", got)
	}
	write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:00Z","type":"turn_context","payload":{"approvals_reviewer":"auto_review"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"exec_command"}}` + "\n")
	if got := statusFromRolloutAt(path, now.Add(2*time.Second)); got != "busy" {
		t.Fatalf("auto-reviewed tool approval = %q, want busy", got)
	}
	write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:00Z","type":"turn_context","payload":{"approvals_reviewer":"auto_review"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input"}}` + "\n")
	if got := statusFromRolloutAt(path, now.Add(2*time.Second)); got != "asking" {
		t.Fatalf("auto-review request_user_input = %q, want asking", got)
	}
	write(`{"timestamp":"2026-06-22T12:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:01Z","type":"response_item","payload":{"type":"function_call","name":"exec_command"}}` + "\n" +
		`{"timestamp":"2026-06-22T12:00:07Z","type":"response_item","payload":{"type":"function_call_output"}}` + "\n")
	if got := statusFromRolloutAt(path, now.Add(2*time.Second)); got != "busy" {
		t.Fatalf("tool output = %q, want busy", got)
	}
}

func TestStatusSnapshotTracksRecoverySignals(t *testing.T) {
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

	snapshot := statusSnapshotFromRolloutAt(path, now)
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

func TestResolveStatusWithTurnError(t *testing.T) {
	start := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		snapshot statusSnapshot
		errorAt  time.Time
		want     string
	}{
		{
			name:     "unresolved error overrides busy",
			snapshot: statusSnapshot{Status: "busy", LastTaskStartedAt: start, LastProgressAt: start.Add(time.Second)},
			errorAt:  start.Add(2 * time.Second),
			want:     "error",
		},
		{
			name:     "task complete bookkeeping does not hide error",
			snapshot: statusSnapshot{Status: "idle", LastTaskStartedAt: start, LastProgressAt: start.Add(time.Second)},
			errorAt:  start.Add(2 * time.Second),
			want:     "error",
		},
		{
			name:     "later model progress clears error",
			snapshot: statusSnapshot{Status: "busy", LastTaskStartedAt: start, LastProgressAt: start.Add(3 * time.Second)},
			errorAt:  start.Add(2 * time.Second),
			want:     "busy",
		},
		{
			name:     "missing error preserves rollout status",
			snapshot: statusSnapshot{Status: "asking", LastTaskStartedAt: start, LastProgressAt: start.Add(time.Second)},
			want:     "asking",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveStatus(tc.snapshot, tc.errorAt); got != tc.want {
				t.Fatalf("resolveStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLatestTurnErrorFromDBFiltersNoise(t *testing.T) {
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

	got, ok := latestTurnErrorFromDB(db, "thread-a", time.Unix(200, 0))
	if !ok {
		t.Fatal("expected final turn error")
	}
	want := time.Unix(204, 5)
	if !got.Equal(want) {
		t.Fatalf("error timestamp = %v, want %v", got, want)
	}
	if _, ok := latestTurnErrorFromDB(filepath.Join(t.TempDir(), "missing.sqlite"), "thread-a", time.Time{}); ok {
		t.Fatal("missing database must degrade without an error signal")
	}
}
