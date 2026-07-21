package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanTurnError(t *testing.T) {
	line := func(s string) []byte { return []byte(s) }
	err529 := `{"type":"assistant","error":"server_error","apiErrorStatus":529,"isApiErrorMessage":true,"timestamp":"2026-07-07T07:05:52Z","message":{"content":[{"type":"text","text":"API Error: 529 Overloaded."}]}}`
	err401 := `{"type":"assistant","error":"authentication_error","apiErrorStatus":401,"isApiErrorMessage":true,"timestamp":"2026-07-07T07:05:52Z"}`
	err429 := `{"type":"assistant","error":"rate_limit","apiErrorStatus":429,"isApiErrorMessage":true,"timestamp":"2026-07-07T07:05:52Z"}`
	okAsst := `{"type":"assistant","timestamp":"2026-07-07T07:06:00Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`
	userMsg := `{"type":"user","timestamp":"2026-07-07T07:06:10Z","message":{"role":"user","content":"继续"}}`

	t.Run("latest terminal 529 is an error", func(t *testing.T) {
		got, ok := scanTurnError([][]byte{line(okAsst), line(err529)})
		if !ok || got.Status != 529 || !got.retryable() {
			t.Fatalf("got %+v ok=%v, want retryable 529", got, ok)
		}
	})
	t.Run("later ok assistant supersedes an earlier error", func(t *testing.T) {
		if _, ok := scanTurnError([][]byte{line(err529), line(okAsst)}); ok {
			t.Fatalf("expected no active error after a later successful turn")
		}
	})
	t.Run("later user message supersedes an earlier error", func(t *testing.T) {
		if _, ok := scanTurnError([][]byte{line(err529), line(userMsg)}); ok {
			t.Fatalf("expected no active error after a later user retry")
		}
	})
	t.Run("429 is not an error here (it is limited)", func(t *testing.T) {
		if _, ok := scanTurnError([][]byte{line(err429)}); ok {
			t.Fatalf("429 must not surface as error")
		}
	})
	t.Run("401 is an error but not retryable", func(t *testing.T) {
		got, ok := scanTurnError([][]byte{line(err401)})
		if !ok || got.Status != 401 || got.retryable() {
			t.Fatalf("got %+v ok=%v, want non-retryable 401", got, ok)
		}
	})
	t.Run("connection-closed (server_error, no status) is retryable", func(t *testing.T) {
		connErr := `{"type":"assistant","error":"server_error","isApiErrorMessage":true,"timestamp":"2026-07-07T07:05:52Z","message":{"content":[{"type":"text","text":"API Error: Connection closed mid-response. The response above may be incomplete."}]}}`
		got, ok := scanTurnError([][]byte{line(connErr)})
		if !ok || got.Status != 0 || !got.retryable() {
			t.Fatalf("got %+v ok=%v, want retryable server_error/0", got, ok)
		}
	})
}

// TestRetryable pins the retry policy to the real error taxonomy observed in
// Claude session JSONL (see error.go retryable).
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
		got := turnError{Type: c.typ, Status: c.st}.retryable()
		if got != c.want {
			t.Errorf("retryable{type:%q status:%d} = %v, want %v", c.typ, c.st, got, c.want)
		}
	}
}

func TestTurnErrorFromJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	body := `{"type":"user","timestamp":"2026-07-07T07:00:00Z","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"assistant","error":"server_error","apiErrorStatus":529,"isApiErrorMessage":true,"timestamp":"2026-07-07T07:05:52Z","message":{"content":[{"type":"text","text":"API Error: 529 Overloaded."}]}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := turnErrorFromJSONL(path)
	if !ok || got.Status != 529 {
		t.Fatalf("turnErrorFromJSONL = %+v ok=%v, want 529", got, ok)
	}
}
