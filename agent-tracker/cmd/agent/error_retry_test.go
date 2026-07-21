package main

import (
	"testing"
	"time"
)




func TestPlanErrorRetry(t *testing.T) {
	id := func(d time.Duration) time.Duration { return d } // no jitter
	base := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	t.Run("disabled → noop", func(t *testing.T) {
		p := planErrorRetry(retryInput{Enabled: false, HasError: true, Max: 3, Now: base}, id)
		if p.Action != retryNoop {
			t.Fatalf("got %v", p.Action)
		}
	})
	t.Run("no error + settled → clear", func(t *testing.T) {
		p := planErrorRetry(retryInput{Enabled: true, HasError: false, Busy: false, Max: 3, Now: base}, id)
		if p.Action != retryClear {
			t.Fatalf("got %v", p.Action)
		}
	})
	t.Run("no error + busy → noop (keep counters mid-retry)", func(t *testing.T) {
		p := planErrorRetry(retryInput{Enabled: true, HasError: false, Busy: true, Max: 3, Now: base}, id)
		if p.Action != retryNoop {
			t.Fatalf("got %v", p.Action)
		}
	})
	t.Run("fresh error → schedule first retry at errorAt+backoff(0)", func(t *testing.T) {
		p := planErrorRetry(retryInput{Enabled: true, HasError: true, ErrorAt: base, Count: 0, Max: 3, Now: base}, id)
		if p.Action != retrySchedule || !p.NextAt.Equal(base.Add(30*time.Second)) {
			t.Fatalf("got %v nextAt=%v", p.Action, p.NextAt)
		}
	})
	t.Run("scheduled but not due → noop", func(t *testing.T) {
		p := planErrorRetry(retryInput{Enabled: true, HasError: true, ErrorAt: base, Count: 0, NextAt: base.Add(30 * time.Second), Max: 3, Now: base.Add(10 * time.Second)}, id)
		if p.Action != retryNoop {
			t.Fatalf("got %v", p.Action)
		}
	})
	t.Run("due → fire, bump count, schedule next", func(t *testing.T) {
		now := base.Add(40 * time.Second)
		p := planErrorRetry(retryInput{Enabled: true, HasError: true, ErrorAt: base, Count: 0, NextAt: base.Add(30 * time.Second), Max: 3, Now: now}, id)
		if p.Action != retryFire || p.NewCount != 1 || !p.NextAt.Equal(now.Add(60*time.Second)) {
			t.Fatalf("got %v count=%d nextAt=%v", p.Action, p.NewCount, p.NextAt)
		}
	})
	t.Run("last attempt fires with no further schedule", func(t *testing.T) {
		now := base.Add(1000 * time.Second)
		p := planErrorRetry(retryInput{Enabled: true, HasError: true, ErrorAt: base, Count: 2, NextAt: base, Max: 3, Now: now}, id)
		if p.Action != retryFire || p.NewCount != 3 || !p.NextAt.IsZero() {
			t.Fatalf("got %v count=%d nextAt=%v", p.Action, p.NewCount, p.NextAt)
		}
	})
	t.Run("cap reached → noop (stop, leave [E])", func(t *testing.T) {
		p := planErrorRetry(retryInput{Enabled: true, HasError: true, ErrorAt: base, Count: 3, NextAt: base, Max: 3, Now: base.Add(9999 * time.Second)}, id)
		if p.Action != retryNoop {
			t.Fatalf("got %v", p.Action)
		}
	})
	t.Run("due but busy → noop (never inject mid-turn)", func(t *testing.T) {
		p := planErrorRetry(retryInput{Enabled: true, HasError: true, Busy: true, ErrorAt: base, Count: 0, NextAt: base, Max: 3, Now: base.Add(60 * time.Second)}, id)
		if p.Action != retryNoop {
			t.Fatalf("got %v", p.Action)
		}
	})
}

func TestRetryBackoff(t *testing.T) {
	cases := map[int]time.Duration{0: 30 * time.Second, 1: 60 * time.Second, 4: 300 * time.Second, 9: 300 * time.Second}
	for count, want := range cases {
		if got := retryBackoff(count); got != want {
			t.Fatalf("retryBackoff(%d) = %v, want %v", count, got, want)
		}
	}
}

func TestApplyRetryJitter(t *testing.T) {
	d := 100 * time.Second
	if got := applyRetryJitter(d, 0.5); got != d { // 0.5 → zero delta
		t.Fatalf("mid jitter = %v, want %v", got, d)
	}
	lo := applyRetryJitter(d, 0.0) // -15%
	hi := applyRetryJitter(d, 1.0) // +15% (exclusive bound, but test the formula)
	if lo != 85*time.Second || hi != 115*time.Second {
		t.Fatalf("jitter bounds = %v / %v, want 85s / 115s", lo, hi)
	}
}
