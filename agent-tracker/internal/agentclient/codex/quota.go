package codex

import (
	"bufio"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/david/agent-tracker/internal/agentclient"
)

// Usage-quota probing: every request appends a token_count event to the
// session rollout JSONL carrying payload.rate_limits.{primary,secondary}
// .resets_at — an absolute epoch second for the 5h / weekly windows.

const quotaTailBytes = 256 << 10

type rateWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int64   `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

type rateLimits struct {
	Primary   *rateWindow `json:"primary"`
	Secondary *rateWindow `json:"secondary"`
}

func (rl rateLimits) windows() []agentclient.RateWindow {
	var out []agentclient.RateWindow
	for _, w := range []*rateWindow{rl.Primary, rl.Secondary} {
		if w != nil {
			out = append(out, agentclient.RateWindow{
				UsedPercent: w.UsedPercent, WindowMinutes: w.WindowMinutes, ResetsAt: w.ResetsAt})
		}
	}
	return out
}

// rateLimitsFromRollout returns the latest token_count rate_limits snapshot in
// a rollout JSONL (bounded tail read).
func rateLimitsFromRollout(path string) (rateLimits, bool) {
	if strings.TrimSpace(path) == "" {
		return rateLimits{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return rateLimits{}, false
	}
	defer f.Close()
	start := int64(0)
	if info, err := f.Stat(); err == nil && info.Size() > quotaTailBytes {
		start = info.Size() - quotaTailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		start = 0
		_, _ = f.Seek(0, io.SeekStart)
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 4<<20)
	if start > 0 {
		scanner.Scan()
	}
	var (
		rl    rateLimits
		found bool
	)
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				Type       string      `json:"type"`
				RateLimits *rateLimits `json:"rate_limits"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		// Codex logs a separate token_count snapshot per limit_id (e.g. "codex"
		// vs "premium"); categories the account isn't currently metering come
		// through with both windows null. Skip those so they don't clobber the
		// last snapshot that actually carried usage.
		if entry.Type == "event_msg" && entry.Payload.Type == "token_count" &&
			entry.Payload.RateLimits != nil &&
			(entry.Payload.RateLimits.Primary != nil || entry.Payload.RateLimits.Secondary != nil) {
			rl, found = *entry.Payload.RateLimits, true
		}
	}
	return rl, found
}

// latestRolloutPath finds the most recently written rollout under
// ~/.codex/sessions — quota is account-wide, so any fresh snapshot works.
func (a *Adapter) latestRolloutPath() string {
	root := filepath.Join(a.home(), ".codex", "sessions")
	var (
		newest    string
		newestMod time.Time
	)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(newestMod) {
			newestMod, newest = info.ModTime(), path
		}
		return nil
	})
	return newest
}

// exhaustedResetAt reports the reset instant an actually exhausted window
// forces (the "limited" [L] signal); mirrors Claude's 429-only detection.
func (a *Adapter) exhaustedResetAt(rolloutPath string, now time.Time) (time.Time, bool) {
	path := strings.TrimSpace(rolloutPath)
	if path == "" {
		path = a.latestRolloutPath()
	}
	rl, ok := rateLimitsFromRollout(path)
	if !ok {
		return time.Time{}, false
	}
	return agentclient.ExhaustedResetAt(rl.windows(), now)
}
