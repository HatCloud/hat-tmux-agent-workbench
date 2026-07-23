package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/david/agent-tracker/internal/agentclient"
)

// writeSession writes a sessions/<pid>.json and its project JSONL under home,
// returning the JSONL path.
func writeSession(t *testing.T, home string, pid int, metaJSON, jsonlBody string) string {
	t.Helper()
	sessions := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(sessions, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, fmt.Sprintf("%d.json", pid)), []byte(metaJSON), 0644); err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(home, ".claude", "projects", "-proj")
	if err := os.MkdirAll(projects, 0755); err != nil {
		t.Fatal(err)
	}
	jsonl := filepath.Join(projects, "sid-1.jsonl")
	if err := os.WriteFile(jsonl, []byte(jsonlBody), 0644); err != nil {
		t.Fatal(err)
	}
	return jsonl
}

func testIndex(pid int) *agentclient.Index {
	return &agentclient.Index{
		Children: map[int][]int{100: {pid}},
		Commands: map[int]string{pid: "claude"},
		SideCar:  map[string]any{"claude.providers": map[string]string{}},
	}
}

// TestScanModelAndTitleRealShape pins the ai-title record to its real on-disk
// shape ({"type":"ai-title","aiTitle":"…"}) — the pre-migration adapter parsed
// a nonexistent "title"/"name" field and never saw AI titles.
func TestScanModelAndTitleRealShape(t *testing.T) {
	body := `{"type":"ai-title","aiTitle":"系统探针检测","sessionId":"sid-1"}` + "\n" +
		`{"type":"assistant","message":{"model":"claude-sonnet-4-6"}}` + "\n" +
		`{"type":"ai-title","aiTitle":"latest title","sessionId":"sid-1"}` + "\n"
	model, title := scanModelAndTitle(strings.NewReader(body), false)
	if model != "claude-sonnet-4-6" {
		t.Fatalf("model = %q", model)
	}
	if title != "latest title" {
		t.Fatalf("aiTitle = %q, want the latest ai-title record", title)
	}
}

func TestDetectFillsTitleModelStatus(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, 4242,
		`{"pid":4242,"name":"","status":"idle","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		`{"type":"ai-title","aiTitle":"auto title"}`+"\n"+
			`{"type":"assistant","message":{"model":"claude-opus-4-8"}}`+"\n")
	a := &Adapter{Home: home}
	s, ok := a.Detect(testIndex(4242), 100)
	if !ok {
		t.Fatal("expected detect")
	}
	if s.Client != "claude" || s.Title != "auto title" || s.Model != "claude-opus-4-8" {
		t.Fatalf("session: %+v", s)
	}
	if s.PersistTitle != "" {
		t.Fatalf("ai-title must not be persistable (PersistTitle=%q)", s.PersistTitle)
	}
	if s.Status != agentclient.StatusIdle || s.LimitResetAt != nil || s.Error != nil {
		t.Fatalf("clean idle session: %+v", s)
	}
	if s.CWD != "/proj" || s.SessionKey != "sid-1" || s.SourcePath == "" {
		t.Fatalf("addressing fields: %+v", s)
	}
}

func TestDerivedMetadataNameRemainsAutoNameEligible(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, 4242,
		`{"pid":4242,"name":"proj-1","nameSource":"derived","status":"idle","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		`{"type":"ai-title","aiTitle":"useful default title"}`+"\n")
	a := &Adapter{Home: home}
	s, ok := a.Detect(testIndex(4242), 100)
	if !ok {
		t.Fatal("expected detect")
	}
	if s.Name.Source != agentclient.SessionNameGenerated {
		t.Fatalf("derived metadata source = %q, want generated", s.Name.Source)
	}
	state, err := a.SessionName(s)
	if err != nil || state.Source != agentclient.SessionNameGenerated || !state.Writable {
		t.Fatalf("derived session name = %+v err=%v", state, err)
	}
}

func TestSessionNamingUsesCustomTitleNotAITitle(t *testing.T) {
	home := t.TempDir()
	jsonl := writeSession(t, home, 4242,
		`{"pid":4242,"name":"","status":"idle","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		`{"type":"ai-title","aiTitle":"agent default"}`+"\n")
	a := &Adapter{Home: home}
	s, ok := a.Detect(testIndex(4242), 100)
	if !ok || s.Title != "agent default" {
		t.Fatalf("initial detect = %+v ok=%v", s, ok)
	}
	state, err := a.SessionName(s)
	if err != nil || state.Source != agentclient.SessionNameNone || !state.Writable {
		t.Fatalf("unnamed state = %+v err=%v", state, err)
	}
	if err := a.SetSessionName(context.Background(), s, "Meaningful Name"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(jsonl)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"type":"custom-title"`) ||
		!strings.Contains(string(body), `"customTitle":"Meaningful Name"`) {
		t.Fatalf("custom-title record missing: %s", body)
	}
	s, ok = a.Detect(testIndex(4242), 100)
	if !ok || s.Title != "Meaningful Name" || s.PersistTitle != "Meaningful Name" {
		t.Fatalf("custom title must outrank ai-title: %+v ok=%v", s, ok)
	}
	state, err = a.SessionName(s)
	if err != nil || state.Value != "Meaningful Name" || state.Source != agentclient.SessionNameUser {
		t.Fatalf("named state = %+v err=%v", state, err)
	}
	if err := a.SetSessionName(context.Background(), s, "overwrite"); err != agentclient.ErrSessionAlreadyNamed {
		t.Fatalf("second SetSessionName err=%v, want ErrSessionAlreadyNamed", err)
	}
}

func TestSetSessionNameHonorsCanceledContext(t *testing.T) {
	home := t.TempDir()
	jsonl := writeSession(t, home, 4242,
		`{"pid":4242,"name":"","status":"idle","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		`{"type":"ai-title","aiTitle":"agent default"}`+"\n")
	a := &Adapter{Home: home}
	s, ok := a.Detect(testIndex(4242), 100)
	if !ok {
		t.Fatal("expected detect")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.SetSessionName(ctx, s, "must not write"); err != context.Canceled {
		t.Fatalf("SetSessionName err=%v, want canceled", err)
	}
	body, err := os.ReadFile(jsonl)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "custom-title") {
		t.Fatalf("canceled write changed transcript: %s", body)
	}
}

func TestDetectHeadlessRejected(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, 4242,
		`{"pid":4242,"name":"","status":"busy","sessionId":"sid-1","cwd":"/proj","entrypoint":"sdk-cli"}`,
		"")
	a := &Adapter{Home: home}
	if _, ok := a.Detect(testIndex(4242), 100); ok {
		t.Fatal("sdk-cli session must not detect")
	}
}

// TestDetectLimited: an unresolved 429 as the latest turn → StatusLimited with
// the exact reset instant (AC-3).
func TestDetectLimited(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, 4242,
		`{"pid":4242,"name":"n","status":"idle","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		`{"type":"assistant","timestamp":"2030-01-01T00:00:00Z","error":"rate_limit","apiErrorStatus":429,`+
			`"message":{"content":[{"type":"text","text":"You've hit your session limit · resets 3am (UTC)"}]}}`+"\n")
	a := &Adapter{Home: home}
	s, ok := a.Detect(testIndex(4242), 100)
	if !ok {
		t.Fatal("expected detect")
	}
	if s.Status != agentclient.StatusLimited || s.LimitResetAt == nil {
		t.Fatalf("expected limited, got %+v", s)
	}
	want := time.Date(2030, 1, 1, 3, 0, 0, 0, time.UTC)
	if !s.LimitResetAt.Equal(want) {
		t.Fatalf("LimitResetAt = %v, want %v", s.LimitResetAt, want)
	}
}

// TestDetectError: a terminal 529 → StatusError with a retryable TurnError;
// busy sessions skip the overlay entirely (AC-3).
func TestDetectError(t *testing.T) {
	home := t.TempDir()
	errLine := `{"type":"assistant","timestamp":"2026-07-07T07:05:52Z","error":"server_error","apiErrorStatus":529,` +
		`"isApiErrorMessage":true,"message":{"content":[{"type":"text","text":"API Error: 529 Overloaded."}]}}`
	writeSession(t, home, 4242,
		`{"pid":4242,"name":"n","status":"idle","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		errLine+"\n")
	a := &Adapter{Home: home}
	s, ok := a.Detect(testIndex(4242), 100)
	if !ok {
		t.Fatal("expected detect")
	}
	if s.Status != agentclient.StatusError || s.Error == nil {
		t.Fatalf("expected error status, got %+v", s)
	}
	if s.Error.Status != 529 || s.Error.Type != "server_error" || !s.Error.Retryable {
		t.Fatalf("TurnError = %+v, want retryable server_error/529", s.Error)
	}
	wantAt := time.Date(2026, 7, 7, 7, 5, 52, 0, time.UTC)
	if !s.Error.At.Equal(wantAt) {
		t.Fatalf("Error.At = %v, want %v", s.Error.At, wantAt)
	}

	// Busy session: no [L]/[E] probing at all.
	writeSession(t, home, 4242,
		`{"pid":4242,"name":"n","status":"busy","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		errLine+"\n")
	s, ok = (&Adapter{Home: home}).Detect(testIndex(4242), 100)
	if !ok || s.Status != agentclient.StatusBusy || s.Error != nil || s.LimitResetAt != nil {
		t.Fatalf("busy must skip overlays: %+v ok=%v", s, ok)
	}
}

func TestFirstPrompt(t *testing.T) {
	home := t.TempDir()
	jsonl := writeSession(t, home, 4242,
		`{"pid":4242,"name":"n","status":"idle","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"first prompt"}]}}`+"\n"+
			`{"type":"user","message":{"role":"user","content":"second"}}`+"\n")
	a := &Adapter{Home: home}
	if got := a.FirstPrompt(agentclient.LiveSession{SourcePath: jsonl}); got != "first prompt" {
		t.Fatalf("FirstPrompt = %q", got)
	}
}

func TestFirstPromptSkipsLocalCommandRecords(t *testing.T) {
	home := t.TempDir()
	jsonl := writeSession(t, home, 4242,
		`{"pid":4242,"name":"proj-1","nameSource":"derived","status":"idle","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		`{"type":"user","message":{"role":"user","content":"<local-command-caveat>generated locally</local-command-caveat>"}}`+"\n"+
			`{"type":"user","message":{"role":"user","content":"<command-name>/model</command-name>"}}`+"\n"+
			`{"type":"user","message":{"role":"user","content":"<local-command-stdout>Set model to Opus</local-command-stdout>"}}`+"\n"+
			`{"type":"user","message":{"role":"user","content":"fix automatic session naming"}}`+"\n")
	a := &Adapter{Home: home}
	if got := a.FirstPrompt(agentclient.LiveSession{SourcePath: jsonl}); got != "fix automatic session naming" {
		t.Fatalf("FirstPrompt = %q", got)
	}
}

func TestFirstPromptWaitsWhenOnlyLocalCommandsExist(t *testing.T) {
	home := t.TempDir()
	jsonl := writeSession(t, home, 4242,
		`{"pid":4242,"name":"proj-1","nameSource":"derived","status":"idle","sessionId":"sid-1","cwd":"/proj","entrypoint":"cli"}`,
		`{"type":"user","message":{"role":"user","content":"<local-command-caveat>generated locally</local-command-caveat>"}}`+"\n"+
			`{"type":"user","message":{"role":"user","content":"<command-name>/model</command-name>"}}`+"\n"+
			`{"type":"user","message":{"role":"user","content":"<local-command-stdout>Set model to Opus</local-command-stdout>"}}`+"\n")
	a := &Adapter{Home: home}
	if got := a.FirstPrompt(agentclient.LiveSession{SourcePath: jsonl}); got != "" {
		t.Fatalf("FirstPrompt = %q, want empty until a real user prompt", got)
	}
}

func TestProviderFromPSEnv(t *testing.T) {
	providers := map[string]string{"https://api.minimaxi.com/anthropic": "minimax"}
	if got := providerFromPSEnv("PID TT ... ANTHROPIC_BASE_URL=https://api.minimaxi.com/anthropic PATH=/bin", providers); got != "minimax" {
		t.Fatalf("mapped url = %q, want minimax", got)
	}
	if got := providerFromPSEnv("ANTHROPIC_BASE_URL=https://unknown.example", providers); got != "" {
		t.Fatalf("unmapped url = %q, want empty", got)
	}
	if got := providerFromPSEnv("PATH=/bin HOME=/Users/x", providers); got != "anthropic" {
		t.Fatalf("no url = %q, want anthropic", got)
	}
}

func TestModelFromArgs(t *testing.T) {
	if got := modelFromArgs("claude --model claude-opus-4-8 --resume x"); got != "claude-opus-4-8" {
		t.Fatalf("got %q", got)
	}
	if got := modelFromArgs("claude --model=sonnet"); got != "sonnet" {
		t.Fatalf("got %q", got)
	}
	if got := modelFromArgs("claude"); got != "" {
		t.Fatalf("got %q", got)
	}
}

// headless（claude -p，entrypoint=sdk-cli）会话不得驱动窗口特性——曾被 pane
// 进程树匹配后劫持窗口状态，auto-retry 甚至把续跑消息注进 headless worker 的
// pane。kind 对两者都是 interactive，entrypoint 是唯一判据；空值兼容旧版本。
func TestIsWindowAgentSession(t *testing.T) {
	if !isWindowAgentSession(sessionMeta{Entrypoint: "cli"}) {
		t.Fatalf("交互 TUI（cli）应视为窗口 agent 会话")
	}
	if !isWindowAgentSession(sessionMeta{}) {
		t.Fatalf("旧版本无 entrypoint 字段应兼容")
	}
	if isWindowAgentSession(sessionMeta{Entrypoint: "sdk-cli"}) {
		t.Fatalf("headless sdk-cli 不应驱动窗口特性")
	}
}
