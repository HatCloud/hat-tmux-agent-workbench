package main

import (
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
