package main

import (
	"testing"

	"github.com/david/agent-tracker/internal/ipc"
)

func TestRemoteTaskStatus(t *testing.T) {
	cases := []struct {
		name string
		task ipc.Task
		want string
	}{
		{"name prefix busy", ipc.Task{Window: "[B] proj/foo", Status: "in_progress"}, "busy"},
		{"name prefix asking wins over structured", ipc.Task{Window: "[?] proj/foo", Status: "in_progress"}, "asking"},
		{"name prefix error", ipc.Task{Window: "[E] proj/foo"}, "error"},
		{"name prefix idle", ipc.Task{Window: "[I] proj/foo"}, "idle"},
		// Remote hides prefixes → fall back to structured fields.
		{"structured busy", ipc.Task{Window: "proj/foo", Status: "in_progress"}, "busy"},
		{"structured asking", ipc.Task{Window: "proj/foo", Status: "in_progress", Asking: true, Attention: "asking"}, "asking"},
		{"structured error", ipc.Task{Window: "proj/foo", Attention: "error"}, "error"},
		{"structured idle", ipc.Task{Window: "proj/foo", Status: "completed"}, ""},
	}
	for _, c := range cases {
		if got := remoteTaskStatus(c.task); got != c.want {
			t.Errorf("%s: remoteTaskStatus = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAggregateRemoteState(t *testing.T) {
	t.Run("any busy → busy status", func(t *testing.T) {
		_, st := aggregateRemoteState([]ipc.Task{
			{Window: "[I] a", Acknowledged: true},
			{Window: "[B] b", Status: "in_progress", Acknowledged: true},
		})
		if st != "busy" {
			t.Fatalf("status = %q, want busy", st)
		}
	})
	t.Run("asking outranks busy", func(t *testing.T) {
		_, st := aggregateRemoteState([]ipc.Task{
			{Window: "[B] a", Status: "in_progress", Acknowledged: true},
			{Window: "[?] b", Status: "in_progress", Acknowledged: true},
		})
		if st != "asking" {
			t.Fatalf("status = %q, want asking", st)
		}
	})
	t.Run("error outranks everything", func(t *testing.T) {
		_, st := aggregateRemoteState([]ipc.Task{
			{Window: "[?] a", Acknowledged: true},
			{Window: "[E] b", Acknowledged: true},
			{Window: "[B] c", Status: "in_progress", Acknowledged: true},
		})
		if st != "error" {
			t.Fatalf("status = %q, want error", st)
		}
	})
	t.Run("all idle → empty status, no bell", func(t *testing.T) {
		bell, st := aggregateRemoteState([]ipc.Task{
			{Window: "[I] a", Status: "in_progress", Acknowledged: true},
		})
		if st != "" || bell {
			t.Fatalf("got status=%q bell=%v, want empty/false", st, bell)
		}
	})
	t.Run("busy status but also unacked completed → bell rises", func(t *testing.T) {
		bell, st := aggregateRemoteState([]ipc.Task{
			{Window: "[B] a", Status: "in_progress", Acknowledged: true},
			{Window: "done b", Status: statusCompleted, Acknowledged: false},
		})
		if st != "busy" || !bell {
			t.Fatalf("got status=%q bell=%v, want busy/true", st, bell)
		}
	})
	t.Run("[?] name raises bell even if acknowledged", func(t *testing.T) {
		bell, _ := aggregateRemoteState([]ipc.Task{
			{Window: "[?] a", Acknowledged: true},
		})
		if !bell {
			t.Fatal("want bell for [?] window regardless of ack")
		}
	})
}
