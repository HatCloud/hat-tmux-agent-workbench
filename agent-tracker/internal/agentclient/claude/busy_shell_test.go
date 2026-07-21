package claude

import (
	"testing"

	"github.com/david/agent-tracker/internal/agentclient"
)

// TestResolveBusyShell pins the pure identification/override logic (AC-1):
// shell/idle + a subtree command matching a pattern → busy; everything else is
// returned unchanged; empty/whitespace patterns never match.
func TestResolveBusyShell(t *testing.T) {
	pats := []string{"hl-run", "hl-dispatch"}
	cmds := []string{"claude", "bash /Users/x/.hat-env/bin/agent-hl/hl-run --engine claude"}

	cases := []struct {
		name     string
		status   string
		cmds     []string
		patterns []string
		want     string
	}{
		// ① shell/idle + match → busy
		{"shell match → busy", agentclient.StatusShell, cmds, pats, agentclient.StatusBusy},
		{"idle match → busy", agentclient.StatusIdle, cmds, pats, agentclient.StatusBusy},
		// case-insensitive substring
		{"case-insensitive match", agentclient.StatusShell, []string{"HL-RUN worker"}, pats, agentclient.StatusBusy},
		// ② shell/idle no match → unchanged
		{"shell no match → shell", agentclient.StatusShell, []string{"claude", "npm run dev"}, pats, agentclient.StatusShell},
		{"idle no match → idle", agentclient.StatusIdle, []string{"claude"}, pats, agentclient.StatusIdle},
		// ③ non-eligible statuses → unchanged even if match present
		{"busy stays busy", agentclient.StatusBusy, cmds, pats, agentclient.StatusBusy},
		{"asking stays asking", agentclient.StatusAsking, cmds, pats, agentclient.StatusAsking},
		{"limited stays limited", agentclient.StatusLimited, cmds, pats, agentclient.StatusLimited},
		{"error stays error", agentclient.StatusError, cmds, pats, agentclient.StatusError},
		{"waiting stays waiting", agentclient.StatusWaiting, cmds, pats, agentclient.StatusWaiting},
		{"paused stays paused", agentclient.StatusPaused, cmds, pats, agentclient.StatusPaused},
		// ④ empty patterns / empty subtree → unchanged
		{"empty patterns → shell", agentclient.StatusShell, cmds, nil, agentclient.StatusShell},
		{"empty subtree → shell", agentclient.StatusShell, nil, pats, agentclient.StatusShell},
		// ⑤ empty/whitespace pattern element ignored (must not match everything)
		{"empty-string pattern ignored", agentclient.StatusShell, []string{"claude"}, []string{""}, agentclient.StatusShell},
		{"whitespace pattern ignored", agentclient.StatusShell, []string{"claude"}, []string{"   "}, agentclient.StatusShell},
		{"empty pattern among reals still matches real", agentclient.StatusShell, cmds, []string{"", "hl-run"}, agentclient.StatusBusy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveBusyShell(tc.status, tc.cmds, tc.patterns); got != tc.want {
				t.Fatalf("resolveBusyShell(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// TestDefaultBusyShellPatterns pins the built-in defaults (agent-hl launchers).
func TestDefaultBusyShellPatterns(t *testing.T) {
	got := map[string]bool{}
	for _, p := range DefaultBusyShellPatterns {
		got[p] = true
	}
	for _, want := range []string{"hl-run", "hl-dispatch"} {
		if !got[want] {
			t.Fatalf("DefaultBusyShellPatterns missing %q: %v", want, DefaultBusyShellPatterns)
		}
	}
}
