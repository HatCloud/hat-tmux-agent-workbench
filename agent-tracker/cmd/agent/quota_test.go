package main

import (
	"os"
	"path/filepath"
	"testing"
)



func writeTempJSONL(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}



func TestParseTriggerQuota(t *testing.T) {
	for _, s := range []string{"reset", "quota", "RESET"} {
		mode, _, _, err := parseTrigger(s)
		if err != nil || mode != windowTimerTriggerQuota {
			t.Fatalf("parseTrigger(%q) = %v, %v", s, mode, err)
		}
	}
	if _, _, err := parseLoopInterval("5m", windowTimerTriggerQuota); err == nil {
		t.Fatal("quota trigger must reject duration loops")
	}
	if lm, _, err := parseLoopInterval("", windowTimerTriggerQuota); err != nil || lm != windowTimerLoopNone {
		t.Fatalf("empty loop should be none for quota: %v %v", lm, err)
	}
	if lm, _, err := parseLoopInterval("reset", windowTimerTriggerQuota); err != nil || lm != windowTimerLoopQuota {
		t.Fatalf("reset loop should parse for quota trigger: %v %v", lm, err)
	}
	if _, _, err := parseLoopInterval("reset", windowTimerTriggerDelay); err == nil {
		t.Fatal("reset loop must be rejected for non-quota triggers")
	}
}


func TestScreenShowsUsageLimit(t *testing.T) {
	for _, s := range []string{
		"│ You've hit your session limit · resets 12:40am │",
		"You have hit your usage limit",
		"Usage limit reached · upgrade or wait",
		"You've reached your weekly limit",
		"You're approaching your usage limit",
		"Your limit will reset at 3pm",
		"Opus limit resets at 9:00",
		"resets at 12",
		"Please try again later",
	} {
		if !screenShowsUsageLimit(s) {
			t.Fatalf("should match: %q", s)
		}
	}
	if screenShowsUsageLimit("just a normal claude screen ready for input") {
		t.Fatal("should not match plain screen")
	}
}

// TestShouldDismissUsageDialog covers the only safety gate for timer firing:
// Escape only when no live agent is busy AND the quota box is on screen.
func TestShouldDismissUsageDialog(t *testing.T) {
	cases := []struct {
		name                       string
		anyBusy, screenMatch, want bool
	}{
		{"any busy blocks", true, true, false},
		{"idle no dialog → no escape", false, false, false},
		{"idle + dialog → escape", false, true, true},
	}
	for _, c := range cases {
		if got := shouldDismissUsageDialog(c.anyBusy, c.screenMatch); got != c.want {
			t.Errorf("%s: shouldDismissUsageDialog(%v,%v) = %v, want %v",
				c.name, c.anyBusy, c.screenMatch, got, c.want)
		}
	}
}
