package agentclient

import (
	"testing"
	"time"
)

func TestPickReset(t *testing.T) {
	now := time.Unix(1_783_600_000, 0)
	future := func(d time.Duration) int64 { return now.Add(d).Unix() }

	// Secondary (weekly) exhausted at 99% → wait for it even though primary
	// resets sooner.
	windows := []RateWindow{
		{UsedPercent: 11, WindowMinutes: 300, ResetsAt: future(2 * time.Hour)},
		{UsedPercent: 99, WindowMinutes: 10080, ResetsAt: future(20 * time.Hour)},
	}
	got, ok := PickReset(windows, now)
	if !ok || got.Unix() != future(20*time.Hour) {
		t.Fatalf("exhausted secondary should win: got %v ok=%v", got, ok)
	}

	// Nothing exhausted → shortest window's boundary (primary).
	windows[1].UsedPercent = 40
	got, ok = PickReset(windows, now)
	if !ok || got.Unix() != future(2*time.Hour) {
		t.Fatalf("primary boundary expected: got %v ok=%v", got, ok)
	}

	// Stale snapshot: resets already in the past never qualify.
	if _, ok := PickReset([]RateWindow{
		{UsedPercent: 99, WindowMinutes: 300, ResetsAt: now.Add(-time.Hour).Unix()},
	}, now); ok {
		t.Fatal("stale reset should not qualify")
	}

	if _, ok := PickReset(nil, now); ok {
		t.Fatal("empty rate limits should not qualify")
	}
}

func TestIndexMemoLoadsOnce(t *testing.T) {
	idx := &Index{SideCar: map[string]any{}}
	calls := 0
	load := func() any { calls++; return "v" }
	if got := idx.Memo("k", load); got != "v" {
		t.Fatalf("first Memo = %v", got)
	}
	if got := idx.Memo("k", load); got != "v" {
		t.Fatalf("second Memo = %v", got)
	}
	if calls != 1 {
		t.Fatalf("loader calls = %d, want 1", calls)
	}
}
