package claude

import (
	"encoding/json"
	"os"
	"time"
)

// Terminal-turn error detection for [E] / auto-retry. Claude has no error
// status field — when a turn dies on an API error (5xx/529 overloaded) Claude
// Code exhausts its own internal backoff, then writes a synthetic assistant
// record with error/apiErrorStatus/isApiErrorMessage into the project session
// JSONL and stops. We detect that terminal record here; 429 records are the
// "limited" status and are handled by quota.go instead.

// turnError describes the terminal API error a Claude turn died on.
type turnError struct {
	Type   string // the JSONL `error` field, e.g. "server_error", "rate_limit"
	Status int    // apiErrorStatus (HTTP code)
	At     time.Time
}

// retryable reports whether auto-retry should attempt this error. Keyed on the
// JSONL `error` category, because transient failures often carry NO HTTP status
// (apiErrorStatus absent → Status==0): e.g. "Connection closed mid-response",
// "Server error mid-response", "socket connection closed", "operation timed out".
// Retryable (transient):
//   - server_error at any status — 500/529 overloaded and the status-less
//     connection/mid-response drops.
//   - unknown WITHOUT a status — socket-closed / timeout network blips.
//   - any explicit 5xx (defensive).
//
// Not retryable: 429 (→ "limited", handled separately), authentication_failed,
// invalid_request, model_not_found, max_output_tokens, and unknown WITH a 4xx
// status (402 billing / 400 bad request) — retrying can't fix those.
func (e turnError) retryable() bool {
	if e.Status == 429 {
		return false
	}
	switch e.Type {
	case "server_error":
		return true
	case "unknown":
		return e.Status == 0 // socket closed / timeout; exclude 4xx billing/bad-request
	}
	return e.Status >= 500
}

// scanTurnError walks JSONL records (oldest→newest within the scanned tail)
// and returns the terminal API error the latest turn ended on, if any. A later
// non-error assistant or user message supersedes an earlier error (a subsequent
// turn got through, or the user already retried), so the error is only returned
// when it is the last meaningful turn outcome.
func scanTurnError(lines [][]byte) (turnError, bool) {
	var (
		cur  turnError
		have bool
	)
	for _, line := range lines {
		var entry struct {
			Type           string `json:"type"`
			Timestamp      string `json:"timestamp"`
			Error          string `json:"error"`
			APIErrorStatus int    `json:"apiErrorStatus"`
			IsAPIError     bool   `json:"isApiErrorMessage"`
		}
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		if entry.Type != "assistant" && entry.Type != "user" {
			continue
		}
		isErr := entry.IsAPIError || (entry.Error != "" && entry.APIErrorStatus != 0)
		if isErr && entry.APIErrorStatus != 429 {
			at, err := time.Parse(time.RFC3339, entry.Timestamp)
			if err != nil {
				continue
			}
			cur = turnError{Type: entry.Error, Status: entry.APIErrorStatus, At: at}
			have = true
		} else {
			// A normal message (or a 429, which is "limited" not "error")
			// supersedes any earlier terminal error.
			have = false
		}
	}
	return cur, have
}

// turnErrorFromJSONL reads the tail of a session JSONL and reports the terminal
// API error its latest turn stopped on, if any.
func turnErrorFromJSONL(path string) (turnError, bool) {
	f, err := os.Open(path)
	if err != nil {
		return turnError{}, false
	}
	defer f.Close()
	scanner := tailScanner(f, jsonlTailBytes)
	var lines [][]byte
	for scanner.Scan() {
		lines = append(lines, append([]byte(nil), scanner.Bytes()...))
	}
	return scanTurnError(lines)
}
