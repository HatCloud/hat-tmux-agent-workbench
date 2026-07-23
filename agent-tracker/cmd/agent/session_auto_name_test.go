package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/david/agent-tracker/internal/agentclient"
)

func TestSelectAgentSessionTitlePriority(t *testing.T) {
	cases := []struct {
		name        string
		def         string
		native      agentclient.SessionNameState
		generated   string
		nativeWrite bool
		want        string
		force       bool
	}{
		{"native user beats all", "default", agentclient.SessionNameState{Value: "native", Source: agentclient.SessionNameUser}, "generated", true, "native", true},
		{"our native write keeps generated provenance", "default", agentclient.SessionNameState{Value: "generated", Source: agentclient.SessionNameUser}, "generated", true, "generated", false},
		{"same text without native provenance is user", "default", agentclient.SessionNameState{Value: "generated", Source: agentclient.SessionNameUser}, "generated", false, "generated", true},
		{"generated beats default", "default", agentclient.SessionNameState{Source: agentclient.SessionNameNone}, "generated", false, "generated", false},
		{"default fallback", "default", agentclient.SessionNameState{Source: agentclient.SessionNameGenerated}, "", false, "default", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, force := selectAgentSessionTitle(tc.def, tc.native, tc.generated, tc.nativeWrite)
			if got != tc.want || force != tc.force {
				t.Fatalf("got (%q,%v), want (%q,%v)", got, force, tc.want, tc.force)
			}
		})
	}
}

func TestSelectAgentDisplayTitleAppliesManualPriorityOnce(t *testing.T) {
	if got := selectAgentDisplayTitle("native rename", "tmux custom", true); got != "native rename" {
		t.Fatalf("native user rename = %q, want highest priority", got)
	}
	if got := selectAgentDisplayTitle("tracker generated", "tmux custom", false); got != "tmux custom" {
		t.Fatalf("manual title = %q, want priority over generated/default", got)
	}
	if got := selectAgentDisplayTitle("agent default", "", false); got != "agent default" {
		t.Fatalf("default title = %q, want fallback", got)
	}
}

func TestAutoNameSettingDefaultsOn(t *testing.T) {
	if !autoNameSetting(appConfig{}) {
		t.Fatal("auto naming must default on")
	}
	disabled := false
	if autoNameSetting(appConfig{AutoName: &disabled}) {
		t.Fatal("explicit false must disable auto naming")
	}
}

func TestSessionNameCanAutoGenerateRequiresKnownProvenance(t *testing.T) {
	for _, source := range []agentclient.SessionNameSource{
		agentclient.SessionNameNone,
		agentclient.SessionNameGenerated,
	} {
		if !sessionNameCanAutoGenerate(agentclient.SessionNameState{Source: source}) {
			t.Fatalf("source %q should be eligible", source)
		}
	}
	for _, source := range []agentclient.SessionNameSource{
		agentclient.SessionNameUser,
		agentclient.SessionNameUnknown,
		"",
	} {
		if sessionNameCanAutoGenerate(agentclient.SessionNameState{Source: source}) {
			t.Fatalf("source %q must not be eligible", source)
		}
	}
}

func TestManualWindowNamePriority(t *testing.T) {
	if !manualWindowNameWins("[I] my custom", "[I] old auto", false, false) {
		t.Fatal("tmux manual name must beat generated/default names")
	}
	if manualWindowNameWins("[I] my custom", "[I] old auto", true, false) {
		t.Fatal("native user session name must beat tmux manual name")
	}
	if manualWindowNameWins("agent", "[I] old auto", false, false) {
		t.Fatal("launcher placeholder is not a manual name")
	}
	if !manualWindowNameWins("named before first sync", "", false, false) {
		t.Fatal("an initial name with automatic-rename off is manual")
	}
	if manualWindowNameWins("zsh", "", false, true) {
		t.Fatal("tmux's automatic command name is not manual")
	}
	if got := rememberedManualWindowName("[B] my custom", "[I] auto", "", false); got != "my custom" {
		t.Fatalf("remembered manual = %q", got)
	}
	if got := rememberedManualWindowName("[I] native", "[I] native", "my custom", false); got != "my custom" {
		t.Fatalf("hidden manual source was lost: %q", got)
	}
	if got := rememberedManualWindowName("", "[I] native", "my custom", false); got != "" {
		t.Fatalf("cleared manual source = %q", got)
	}
}

func TestCleanGeneratedSessionName(t *testing.T) {
	if got := cleanGeneratedSessionName("```\n\"  Agent # naming \\n extra  \"\n```"); got != "Agent naming extra" {
		t.Fatalf("cleaned = %q", got)
	}
	long := strings.Repeat("名", autoNameMaxRunes+10)
	if got := []rune(cleanGeneratedSessionName(long)); len(got) != autoNameMaxRunes {
		t.Fatalf("cleaned rune length = %d", len(got))
	}
}

func TestGenerateSessionNameFallsBackInOrder(t *testing.T) {
	var got []string
	run := func(_ context.Context, model autoNameModel, _ string) (string, error) {
		got = append(got, model.Provider+"/"+model.Model)
		if model.Provider == "openai" {
			return "", errors.New("luna unavailable")
		}
		return "DeepSeek result", nil
	}
	name, err := generateSessionName(context.Background(), "prompt", run)
	if err != nil || name != "DeepSeek result" {
		t.Fatalf("name=%q err=%v", name, err)
	}
	want := []string{"openai/gpt-5.6-luna", "deepseek/deepseek-v4-flash[1m]"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestRunAutoNameModelUsesIsolatedCWD(t *testing.T) {
	binDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "tasks.json")
	stub := filepath.Join(binDir, "agent-hl-cli")
	script := `#!/bin/sh
tasks=""
output=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tasks) tasks="$2"; shift 2 ;;
    --output) output="$2"; shift 2 ;;
    *) shift ;;
  esac
done
cp "$tasks" "$AUTONAME_CAPTURE"
printf '%s\n' '{"results":[{"status":"ok","structured_output":{"name":"isolated"}}]}' > "$output"
`
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AUTONAME_CAPTURE", capture)
	if _, err := runAutoNameModel(context.Background(), autoNameModels[0], "prompt"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var tasks []struct {
		CWD string `json:"cwd"`
	}
	if err := json.Unmarshal(data, &tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || strings.TrimSpace(tasks[0].CWD) == "" {
		t.Fatalf("auto-name task cwd = %q, want an isolated temporary directory", tasks[0].CWD)
	}
}

type fakeNamingAdapter struct {
	state    agentclient.SessionNameState
	readErr  error
	writeErr error
	writes   int
}

func (f *fakeNamingAdapter) ID() string { return "fake" }

func (f *fakeNamingAdapter) Detect(*agentclient.Index, int) (agentclient.LiveSession, bool) {
	return agentclient.LiveSession{}, false
}

func (f *fakeNamingAdapter) SessionName(agentclient.LiveSession) (agentclient.SessionNameState, error) {
	return f.state, f.readErr
}

func (f *fakeNamingAdapter) SetSessionName(context.Context, agentclient.LiveSession, string) error {
	f.writes++
	return f.writeErr
}

func TestAttemptNativeSessionNameFallback(t *testing.T) {
	unnamed := agentclient.SessionNameState{Source: agentclient.SessionNameNone, Writable: true}
	cases := []struct {
		name    string
		adapter *fakeNamingAdapter
		want    nativeNameWriteResult
		writes  int
	}{
		{"native success", &fakeNamingAdapter{state: unnamed}, nativeNameWriteResult{KeepGenerated: true, Native: true}, 1},
		{"unsupported keeps tracker alias", &fakeNamingAdapter{state: agentclient.SessionNameState{Source: agentclient.SessionNameUnknown}}, nativeNameWriteResult{KeepGenerated: true}, 0},
		{"write failure keeps tracker alias", &fakeNamingAdapter{state: unnamed, writeErr: errors.New("write failed")}, nativeNameWriteResult{KeepGenerated: true}, 1},
		{"user already named drops generated", &fakeNamingAdapter{state: agentclient.SessionNameState{Value: "user", Source: agentclient.SessionNameUser, Writable: true}}, nativeNameWriteResult{}, 0},
		{"our prior native value keeps provenance", &fakeNamingAdapter{state: agentclient.SessionNameState{Value: "generated", Source: agentclient.SessionNameUser, Writable: true}}, nativeNameWriteResult{KeepGenerated: true, Native: true}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := attemptNativeSessionName(context.Background(), tc.adapter, agentclient.LiveSession{}, "generated")
			if got != tc.want || tc.adapter.writes != tc.writes {
				t.Fatalf("got=%+v writes=%d, want=%+v writes=%d", got, tc.adapter.writes, tc.want, tc.writes)
			}
		})
	}
}

type racingNamingAdapter struct {
	reads int
}

func (r *racingNamingAdapter) ID() string { return "race" }

func (r *racingNamingAdapter) Detect(*agentclient.Index, int) (agentclient.LiveSession, bool) {
	return agentclient.LiveSession{}, false
}

func (r *racingNamingAdapter) SessionName(agentclient.LiveSession) (agentclient.SessionNameState, error) {
	r.reads++
	if r.reads == 1 {
		return agentclient.SessionNameState{Source: agentclient.SessionNameNone, Writable: true}, nil
	}
	return agentclient.SessionNameState{Value: "user won race", Source: agentclient.SessionNameUser, Writable: true}, nil
}

func (r *racingNamingAdapter) SetSessionName(context.Context, agentclient.LiveSession, string) error {
	return agentclient.ErrSessionAlreadyNamed
}

func TestAttemptNativeSessionNameConcurrentUserRenameWins(t *testing.T) {
	a := &racingNamingAdapter{}
	got := attemptNativeSessionName(context.Background(), a, agentclient.LiveSession{}, "generated")
	if got.KeepGenerated || got.Native || a.reads != 2 {
		t.Fatalf("got=%+v reads=%d", got, a.reads)
	}
}

func TestBoundAutoNamePrompt(t *testing.T) {
	input := strings.Repeat("x", autoNamePromptMaxRunes+50)
	if got := []rune(boundAutoNamePrompt(input)); len(got) != autoNamePromptMaxRunes {
		t.Fatalf("bounded rune length = %d", len(got))
	}
}

func TestAutoNameAttemptDueBackoff(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		state string
		age   time.Duration
		want  bool
	}{
		{"running", autoNameRunningStale - time.Second, false},
		{"running", autoNameRunningStale, true},
		{"failed", autoNameFailureBackoff - time.Second, false},
		{"failed", autoNameFailureBackoff, true},
		{"done", 24 * time.Hour, false},
		{"", time.Second, true},
	}
	for _, tc := range cases {
		if got := autoNameAttemptDue(tc.state, now.Add(-tc.age).Unix(), now); got != tc.want {
			t.Fatalf("state=%q age=%s: got %v want %v", tc.state, tc.age, got, tc.want)
		}
	}
}
