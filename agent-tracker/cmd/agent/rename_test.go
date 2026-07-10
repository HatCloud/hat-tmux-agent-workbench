package main

import "testing"

func TestTmuxWindowOptionArgsAreQuiet(t *testing.T) {
	got := tmuxWindowOptionArgs("@42", "@agent_missing")
	want := []string{"show-options", "-q", "-w", "-t", "@42", "-v", "@agent_missing"}
	if len(got) != len(want) {
		t.Fatalf("tmuxWindowOptionArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tmuxWindowOptionArgs = %v, want %v", got, want)
		}
	}
}

func TestComposeWindowName(t *testing.T) {
	cases := []struct {
		name     string
		client   string
		provider string
		project  string
		session  string
		want     string
	}{
		{"default index", "claude", "", "hat-config", "1", "hat-config"},
		{"index with trailing dash", "codex", "", "myproj", "2-", "myproj"},
		{"claude with provider", "claude", "minimax", "hat-config", "1", "hat-config"},
		{"named session", "claude", "minimax", "hat-config", "1-refactor", "refactor"},
		{"named session no provider", "claude", "", "hat-config", "1-refactor", "refactor"},
		{"label without index prefix", "claude", "", "hat-config", "demo", "demo"},
		{"no project", "claude", "", "", "1", ""},
		{"whitespace provider no effect", "claude", "  ", "hat-config", "1", "hat-config"},
		{"empty client yields empty", "", "minimax", "hat-config", "1", ""},
		{"date prefix stripped + truncated", "claude", "minimax", "2026-06-18-plugin-popup-reminders", "1", "plugin-popup-re…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := composeWindowName(tc.client, tc.provider, tc.project, tc.session)
			if got != tc.want {
				t.Fatalf("composeWindowName(%q,%q,%q,%q) = %q, want %q",
					tc.client, tc.provider, tc.project, tc.session, got, tc.want)
			}
		})
	}
}

func TestAgentNameBase(t *testing.T) {
	cases := []struct {
		name                      string
		client, provider, project string
		want                      string
	}{
		{"client only", "claude", "", "", ""},
		{"project + client", "claude", "", "hat-config", "hat-config"},
		{"with provider ignored", "claude", "minimax", "hat-config", "hat-config"},
		{"codex", "codex", "", "hat-config", "hat-config"},
		{"whitespace provider ignored", "claude", "  ", "hat-config", "hat-config"},
		{"empty client yields empty", "", "minimax", "hat-config", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentNameBase(tc.client, tc.provider, tc.project); got != tc.want {
				t.Fatalf("agentNameBase(%q,%q,%q) = %q, want %q", tc.client, tc.provider, tc.project, got, tc.want)
			}
		})
	}
}

func TestAbbrevProject(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hat-config", "hat-config"},
		{"2026-06-18-plugin-popup-reminders", "plugin-popup-re…"},
		{"my-very-long-project-name", "my-very-long-pr…"},
		{"2026-06-18-short", "short"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := abbrevProject(tc.in); got != tc.want {
			t.Fatalf("abbrevProject(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSplitSessionLabel(t *testing.T) {
	cases := []struct {
		in        string
		wantIndex string
		wantLabel string
	}{
		{"1-refactor", "1", "refactor"},
		{"12-foo bar", "12", "foo bar"},
		{"1", "1", ""},
		{"1-", "1", ""},
		{"1abc", "", "1abc"},
		{"demo", "", "demo"},
		{"", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			idx, label := splitSessionLabel(tc.in)
			if idx != tc.wantIndex || label != tc.wantLabel {
				t.Fatalf("splitSessionLabel(%q) = (%q,%q), want (%q,%q)",
					tc.in, idx, label, tc.wantIndex, tc.wantLabel)
			}
		})
	}
}
