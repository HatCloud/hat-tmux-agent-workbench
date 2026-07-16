package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanClaudeError(t *testing.T) {
	line := func(s string) []byte { return []byte(s) }
	err529 := `{"type":"assistant","error":"server_error","apiErrorStatus":529,"isApiErrorMessage":true,"timestamp":"2026-07-07T07:05:52Z","message":{"content":[{"type":"text","text":"API Error: 529 Overloaded."}]}}`
	err401 := `{"type":"assistant","error":"authentication_error","apiErrorStatus":401,"isApiErrorMessage":true,"timestamp":"2026-07-07T07:05:52Z"}`
	err429 := `{"type":"assistant","error":"rate_limit","apiErrorStatus":429,"isApiErrorMessage":true,"timestamp":"2026-07-07T07:05:52Z"}`
	okAsst := `{"type":"assistant","timestamp":"2026-07-07T07:06:00Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`
	userMsg := `{"type":"user","timestamp":"2026-07-07T07:06:10Z","message":{"role":"user","content":"继续"}}`

	t.Run("latest terminal 529 is an error", func(t *testing.T) {
		got, ok := scanClaudeError([][]byte{line(okAsst), line(err529)})
		if !ok || got.Status != 529 || !got.Retryable() {
			t.Fatalf("got %+v ok=%v, want retryable 529", got, ok)
		}
	})
	t.Run("later ok assistant supersedes an earlier error", func(t *testing.T) {
		if _, ok := scanClaudeError([][]byte{line(err529), line(okAsst)}); ok {
			t.Fatalf("expected no active error after a later successful turn")
		}
	})
	t.Run("later user message supersedes an earlier error", func(t *testing.T) {
		if _, ok := scanClaudeError([][]byte{line(err529), line(userMsg)}); ok {
			t.Fatalf("expected no active error after a later user retry")
		}
	})
	t.Run("429 is not an error here (it is limited)", func(t *testing.T) {
		if _, ok := scanClaudeError([][]byte{line(err429)}); ok {
			t.Fatalf("429 must not surface as error")
		}
	})
	t.Run("401 is an error but not retryable", func(t *testing.T) {
		got, ok := scanClaudeError([][]byte{line(err401)})
		if !ok || got.Status != 401 || got.Retryable() {
			t.Fatalf("got %+v ok=%v, want non-retryable 401", got, ok)
		}
	})
	t.Run("connection-closed (server_error, no status) is retryable", func(t *testing.T) {
		connErr := `{"type":"assistant","error":"server_error","isApiErrorMessage":true,"timestamp":"2026-07-07T07:05:52Z","message":{"content":[{"type":"text","text":"API Error: Connection closed mid-response. The response above may be incomplete."}]}}`
		got, ok := scanClaudeError([][]byte{line(connErr)})
		if !ok || got.Status != 0 || !got.Retryable() {
			t.Fatalf("got %+v ok=%v, want retryable server_error/0", got, ok)
		}
	})
}

// TestRetryable pins the retry policy to the real error taxonomy observed in
// Claude session JSONL (see error_retry.go Retryable).
func TestRetryable(t *testing.T) {
	cases := []struct {
		typ  string
		st   int
		want bool
	}{
		{"server_error", 529, true}, // overloaded
		{"server_error", 500, true}, // internal
		{"server_error", 0, true},   // connection closed / server mid-response
		{"unknown", 0, true},        // socket closed / operation timed out
		{"unknown", 402, false},     // billing
		{"unknown", 400, false},     // bad request
		{"rate_limit", 429, false},  // → limited, never retried here
		{"authentication_failed", 401, false},
		{"authentication_failed", 0, false},
		{"invalid_request", 400, false},
		{"invalid_request", 0, false}, // prompt too long
		{"model_not_found", 404, false},
		{"max_output_tokens", 0, false},
		{"", 0, false}, // tool-call parse failure (Claude already retried)
	}
	for _, c := range cases {
		got := claudeTurnError{Type: c.typ, Status: c.st}.Retryable()
		if got != c.want {
			t.Errorf("Retryable{type:%q status:%d} = %v, want %v", c.typ, c.st, got, c.want)
		}
	}
}

func TestClaudeErrorFromJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	body := `{"type":"user","timestamp":"2026-07-07T07:00:00Z","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"assistant","error":"server_error","apiErrorStatus":529,"isApiErrorMessage":true,"timestamp":"2026-07-07T07:05:52Z","message":{"content":[{"type":"text","text":"API Error: 529 Overloaded."}]}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := claudeErrorFromJSONL(path)
	if !ok || got.Status != 529 {
		t.Fatalf("claudeErrorFromJSONL = %+v ok=%v, want 529", got, ok)
	}
}

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
