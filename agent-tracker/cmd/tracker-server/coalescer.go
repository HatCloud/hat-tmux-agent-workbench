package main

import (
	"sync"
	"time"
)

// coalescer runs fn on a leading edge, then coalesces a burst of triggers into a
// single trailing run. The first trigger after an idle period runs fn right away
// (near-zero latency); any triggers that arrive while fn is in its cooldown
// window are absorbed into one follow-up run at the window's end. This bounds fn
// to at most once per window while still reacting immediately to the first event
// — the debounce/merge behaviour the status refresh needs so a burst of state
// changes in the same tick doesn't fan out into one refresh per event.
type coalescer struct {
	fn     func()
	window time.Duration

	mu     sync.Mutex
	active bool // a run loop is in flight (fn running or in cooldown)
	dirty  bool // a trigger arrived during the current cooldown

	// sleep is time.Sleep in production; tests inject a controllable stub.
	sleep func(time.Duration)
}

func newCoalescer(window time.Duration, fn func()) *coalescer {
	return &coalescer{fn: fn, window: window, sleep: time.Sleep}
}

// trigger requests a run. It never blocks on fn: the first trigger starts a
// background loop, and concurrent triggers only mark the run dirty.
func (c *coalescer) trigger() {
	c.mu.Lock()
	if c.active {
		c.dirty = true
		c.mu.Unlock()
		return
	}
	c.active = true
	c.mu.Unlock()
	go c.loop()
}

func (c *coalescer) loop() {
	for {
		c.fn()
		c.sleep(c.window) // cooldown: absorb concurrent triggers into c.dirty
		c.mu.Lock()
		if !c.dirty {
			// No trigger during cooldown — settle. A trigger racing this exit
			// blocks on mu, then sees active=false and starts a fresh loop, so no
			// wakeup is lost.
			c.active = false
			c.mu.Unlock()
			return
		}
		c.dirty = false // run once more for the absorbed burst
		c.mu.Unlock()
	}
}
