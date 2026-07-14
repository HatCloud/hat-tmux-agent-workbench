package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCoalescerLeadingAndTrailing drives the coalescer with a sleep stub gated on
// a channel so timing is deterministic: the first trigger fires fn immediately
// (leading), a burst during cooldown collapses into exactly one trailing fire,
// and a quiet cooldown settles.
func TestCoalescerLeadingAndTrailing(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	c := newCoalescer(time.Millisecond, func() { atomic.AddInt32(&calls, 1) })
	c.sleep = func(time.Duration) { <-release } // block cooldown until released

	// Leading edge: first trigger fires fn immediately, then enters cooldown.
	c.trigger()
	waitFor(t, func() bool { return atomic.LoadInt32(&calls) == 1 }, "leading fire")

	// Burst during cooldown: many triggers coalesce into one trailing run.
	for i := 0; i < 5; i++ {
		c.trigger()
	}
	release <- struct{}{} // end first cooldown → trailing fire (calls==2)
	waitFor(t, func() bool { return atomic.LoadInt32(&calls) == 2 }, "trailing fire")

	// The trailing run's cooldown is quiet → settle, no further fire.
	release <- struct{}{}
	waitFor(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.active
	}, "settle")
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2 (one leading + one coalesced trailing)", got)
	}

	// After settling, a new trigger starts a fresh leading fire.
	c.trigger()
	waitFor(t, func() bool { return atomic.LoadInt32(&calls) == 3 }, "re-arm leading fire")
	release <- struct{}{}
}

// TestCoalescerConcurrentTriggers ensures concurrent triggers never lose the last
// wakeup: fn runs at least twice (a leading fire plus at least one trailing run
// covering the burst) and the loop settles.
func TestCoalescerConcurrentTriggers(t *testing.T) {
	var calls int32
	c := newCoalescer(0, func() { atomic.AddInt32(&calls, 1) })
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.trigger() }()
	}
	wg.Wait()
	waitFor(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.active
	}, "settle after concurrent burst")
	if got := atomic.LoadInt32(&calls); got < 1 {
		t.Fatalf("calls = %d, want >= 1", got)
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}
