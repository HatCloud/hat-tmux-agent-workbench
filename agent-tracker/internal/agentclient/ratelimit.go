package agentclient

import "time"

// ExhaustedPercent marks a rate window as blocking when used_percent reaches
// it; every blocking window must reset before work can resume.
const ExhaustedPercent = 95.0

// RateWindow is one usage-quota window (5h / weekly) as reported by a client.
type RateWindow struct {
	UsedPercent   float64
	WindowMinutes int64
	ResetsAt      int64 // epoch seconds
}

// ExhaustedResetAt reports the reset instant the client must wait for because a
// window is actually exhausted (>= ExhaustedPercent), taking the latest among
// any exhausted windows. A zero/false result means the client is not currently
// blocked — this drives the "limited" ([L]) status.
func ExhaustedResetAt(windows []RateWindow, now time.Time) (time.Time, bool) {
	var exhausted time.Time
	for _, w := range windows {
		if w.UsedPercent < ExhaustedPercent || w.ResetsAt <= 0 {
			continue
		}
		if at := time.Unix(w.ResetsAt, 0); at.After(now) && at.After(exhausted) {
			exhausted = at
		}
	}
	if exhausted.IsZero() {
		return time.Time{}, false
	}
	return exhausted, true
}

// PickReset chooses which reset instant to wait for: every exhausted window
// must reopen before work resumes, so take the latest among them; when none is
// exhausted fall back to the shortest window's boundary (the next 5h reset).
// Instants already in the past mean that window has reset and never qualify.
func PickReset(windows []RateWindow, now time.Time) (time.Time, bool) {
	var live []RateWindow
	for _, w := range windows {
		if w.ResetsAt > 0 && time.Unix(w.ResetsAt, 0).After(now) {
			live = append(live, w)
		}
	}
	if len(live) == 0 {
		return time.Time{}, false
	}
	if exhausted, ok := ExhaustedResetAt(windows, now); ok {
		return exhausted, true
	}
	best := live[0]
	for _, w := range live[1:] {
		if w.WindowMinutes < best.WindowMinutes {
			best = w
		}
	}
	return time.Unix(best.ResetsAt, 0), true
}
