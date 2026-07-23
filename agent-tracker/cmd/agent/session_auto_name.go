package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/david/agent-tracker/internal/agentclient"
)

const (
	autoNameMaxRunes       = 48
	autoNamePromptMaxRunes = 4000
	autoNameModelTimeout   = 35 * time.Second
	autoNameRunningStale   = 2 * time.Minute
	autoNameFailureBackoff = 10 * time.Minute

	optGeneratedName        = "@agent_generated_name"
	optGeneratedNameSession = "@agent_generated_name_session"
	optAutoNameState        = "@agent_auto_name_state"
	optAutoNameAttemptAt    = "@agent_auto_name_attempt_at"
	optAutoNameNative       = "@agent_auto_name_native"
)

type autoNameModel struct {
	Provider      string
	Model         string
	Engine        string
	Bare          bool
	MinimalConfig bool
}

var autoNameModels = []autoNameModel{
	{Provider: "openai", Model: "gpt-5.6-luna", Engine: "codex", MinimalConfig: true},
	{Provider: "deepseek", Model: "deepseek-v4-flash[1m]", Engine: "claude", Bare: true},
}

type autoNameModelRunner func(context.Context, autoNameModel, string) (string, error)

type nativeNameWriteResult struct {
	KeepGenerated bool
	Native        bool
}

func selectAgentSessionTitle(defaultTitle string, native agentclient.SessionNameState, generated string, trackerWroteNative bool) (string, bool) {
	defaultTitle = strings.TrimSpace(defaultTitle)
	generated = strings.TrimSpace(generated)
	if native.Source == agentclient.SessionNameUser && strings.TrimSpace(native.Value) != "" {
		nativeName := strings.TrimSpace(native.Value)
		if trackerWroteNative && generated != "" && nativeName == generated {
			return generated, false
		}
		return nativeName, true
	}
	if generated != "" {
		return generated, false
	}
	return defaultTitle, false
}

func boundAutoNamePrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	runes := []rune(prompt)
	if len(runes) > autoNamePromptMaxRunes {
		prompt = string(runes[:autoNamePromptMaxRunes])
	}
	return prompt
}

func cleanGeneratedSessionName(name string) string {
	name = strings.ReplaceAll(name, "```", " ")
	name = strings.ReplaceAll(name, `\n`, " ")
	name = strings.Trim(name, " \t\r\n`'\"")
	var b strings.Builder
	space := false
	for _, r := range name {
		if r == '#' || unicode.IsControl(r) {
			continue
		}
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	cleaned := strings.Trim(b.String(), " -–—_:：;；,.，。/\\|'\"")
	runes := []rune(cleaned)
	if len(runes) > autoNameMaxRunes {
		cleaned = string(runes[:autoNameMaxRunes])
	}
	return strings.TrimSpace(cleaned)
}

func generateSessionName(ctx context.Context, prompt string, run autoNameModelRunner) (string, error) {
	prompt = boundAutoNamePrompt(prompt)
	if prompt == "" {
		return "", fmt.Errorf("empty naming context")
	}
	var errs []error
	for _, model := range autoNameModels {
		modelCtx, cancel := context.WithTimeout(ctx, autoNameModelTimeout)
		name, err := run(modelCtx, model, prompt)
		cancel()
		if err == nil {
			if name = cleanGeneratedSessionName(name); name != "" {
				return name, nil
			}
			err = fmt.Errorf("model returned an empty name")
		}
		errs = append(errs, fmt.Errorf("%s/%s: %w", model.Provider, model.Model, err))
	}
	return "", errors.Join(errs...)
}

func attemptNativeSessionName(ctx context.Context, adapter agentclient.Adapter, live agentclient.LiveSession, name string) nativeNameWriteResult {
	result := nativeNameWriteResult{KeepGenerated: true}
	if adapter == nil {
		return result
	}
	state, err := adapter.SessionName(live)
	if err != nil {
		return result
	}
	if state.Source == agentclient.SessionNameUser && state.Value != "" {
		result.KeepGenerated = state.Value == name
		result.Native = result.KeepGenerated
		return result
	}
	if !state.Writable {
		return result
	}
	err = adapter.SetSessionName(ctx, live, name)
	if err == nil {
		result.Native = true
		return result
	}
	if !errors.Is(err, agentclient.ErrSessionAlreadyNamed) {
		return result
	}
	current, readErr := adapter.SessionName(live)
	if readErr == nil && current.Source == agentclient.SessionNameUser && current.Value != "" {
		result.KeepGenerated = current.Value == name
		result.Native = result.KeepGenerated
	}
	return result
}

func autoNameSessionFingerprint(s *agentclient.LiveSession) string {
	if s == nil || strings.TrimSpace(s.Client) == "" || strings.TrimSpace(s.SessionKey) == "" {
		return ""
	}
	return strings.TrimSpace(s.Client) + ":" + strings.TrimSpace(s.SessionKey)
}

func generatedNameForSession(windowID string, live *agentclient.LiveSession) string {
	fingerprint := autoNameSessionFingerprint(live)
	if fingerprint == "" || tmuxWindowOption(windowID, optGeneratedNameSession) != fingerprint {
		return ""
	}
	return cleanGeneratedSessionName(tmuxWindowOption(windowID, optGeneratedName))
}

func trackerWroteGeneratedNameNatively(windowID string, live *agentclient.LiveSession, generated string) bool {
	return generated != "" && autoNameSessionFingerprint(live) != "" &&
		tmuxWindowOption(windowID, optAutoNameNative) == "1"
}

func observeNativeSessionRename(windowID string, live *agentclient.LiveSession, generated string) {
	if !trackerWroteGeneratedNameNatively(windowID, live, generated) || live == nil ||
		live.Name.Source != agentclient.SessionNameUser {
		return
	}
	if name := strings.TrimSpace(live.Name.Value); name != "" && name != generated {
		unsetWindowOption(windowID, optAutoNameNative)
	}
}

func resolveAgentSessionTitle(windowID string, live *agentclient.LiveSession) (string, bool) {
	if live == nil {
		return "", false
	}
	generated := generatedNameForSession(windowID, live)
	observeNativeSessionRename(windowID, live, generated)
	return selectAgentSessionTitle(live.Title, live.Name, generated,
		trackerWroteGeneratedNameNatively(windowID, live, generated))
}

func selectAgentDisplayTitle(sessionTitle, manualTitle string, nativeSessionNameWins bool) string {
	manualTitle = strings.TrimSpace(manualTitle)
	if !nativeSessionNameWins && manualTitle != "" {
		return manualTitle
	}
	return strings.TrimSpace(sessionTitle)
}

func autoNameAttemptDue(state string, attemptedAt int64, now time.Time) bool {
	if attemptedAt <= 0 {
		return true
	}
	age := now.Sub(time.Unix(attemptedAt, 0))
	switch state {
	case "running":
		return age >= autoNameRunningStale
	case "failed":
		return age >= autoNameFailureBackoff
	case "done":
		return false
	default:
		return true
	}
}

func sessionNameCanAutoGenerate(state agentclient.SessionNameState) bool {
	return state.Source == agentclient.SessionNameNone ||
		state.Source == agentclient.SessionNameGenerated
}

func maybeStartAutoName(windowID, aiPane string, live *agentclient.LiveSession) {
	if !autoNameSetting(loadAppConfig()) || live == nil || aiPane == "" {
		return
	}
	fingerprint := autoNameSessionFingerprint(live)
	if fingerprint == "" {
		return
	}
	storedSession := tmuxWindowOption(windowID, optGeneratedNameSession)
	if storedSession != "" && storedSession != fingerprint {
		unsetWindowOption(windowID, optGeneratedName)
		unsetWindowOption(windowID, optGeneratedNameSession)
		unsetWindowOption(windowID, optAutoNameState)
		unsetWindowOption(windowID, optAutoNameAttemptAt)
		unsetWindowOption(windowID, optAutoNameNative)
		storedSession = ""
	}
	if generatedNameForSession(windowID, live) != "" {
		return
	}
	if !sessionNameCanAutoGenerate(live.Name) {
		return
	}
	adapter := agentclient.DefaultRegistry().AdapterByID(live.Client)
	fp, ok := adapter.(agentclient.FirstPrompter)
	if !ok || strings.TrimSpace(fp.FirstPrompt(*live)) == "" {
		return
	}
	attemptedAt, _ := strconv.ParseInt(tmuxWindowOption(windowID, optAutoNameAttemptAt), 10, 64)
	if storedSession == fingerprint && !autoNameAttemptDue(tmuxWindowOption(windowID, optAutoNameState), attemptedAt, time.Now()) {
		return
	}
	setWindowOption(windowID, optGeneratedNameSession, fingerprint)
	setWindowOption(windowID, optAutoNameState, "running")
	setWindowOption(windowID, optAutoNameAttemptAt, strconv.FormatInt(time.Now().Unix(), 10))

	exe, err := os.Executable()
	if err != nil {
		setWindowOption(windowID, optAutoNameState, "failed")
		return
	}
	cmd := exec.Command(exe, "tmux", "auto-name-session",
		"--window", windowID, "--pane", aiPane, "--session", fingerprint)
	if err := cmd.Start(); err != nil {
		setWindowOption(windowID, optAutoNameState, "failed")
		return
	}
	_ = cmd.Process.Release()
}

func autoNameLockPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("agent-auto-name-%d.lock", os.Getuid()))
}

func runAutoNameSession(args []string) error {
	fs := flag.NewFlagSet("auto-name-session", flag.ContinueOnError)
	var windowID, paneID, fingerprint string
	fs.StringVar(&windowID, "window", "", "tmux window id")
	fs.StringVar(&paneID, "pane", "", "tmux pane id")
	fs.StringVar(&fingerprint, "session", "", "client:session fingerprint")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if windowID == "" || paneID == "" || fingerprint == "" {
		return fmt.Errorf("auto-name-session requires --window, --pane and --session")
	}
	release, acquired, err := acquireSyncNamesLock(autoNameLockPath(), true)
	if err != nil || !acquired {
		return err
	}
	defer release()
	if tmuxWindowOption(windowID, optGeneratedNameSession) != fingerprint {
		return nil
	}
	idx := agentclient.BuildIndex()
	tag := tmuxWindowOption(windowID, "@agent_client")
	live, ok := agentclient.DefaultRegistry().DetectForPane(idx, panePID(paneID), tag)
	if !ok || autoNameSessionFingerprint(&live) != fingerprint {
		setWindowOption(windowID, optAutoNameState, "failed")
		return nil
	}
	adapter := agentclient.DefaultRegistry().AdapterByID(live.Client)
	fp, ok := adapter.(agentclient.FirstPrompter)
	if !ok {
		setWindowOption(windowID, optAutoNameState, "failed")
		return nil
	}
	prompt := fp.FirstPrompt(live)
	name, err := generateSessionName(context.Background(), prompt, runAutoNameModel)
	if err != nil {
		setWindowOption(windowID, optAutoNameState, "failed")
		return nil
	}

	idx = agentclient.BuildIndex()
	live, ok = agentclient.DefaultRegistry().DetectForPane(idx, panePID(paneID), tag)
	if !ok || autoNameSessionFingerprint(&live) != fingerprint ||
		tmuxWindowOption(windowID, optGeneratedNameSession) != fingerprint {
		return nil
	}
	if !sessionNameCanAutoGenerate(live.Name) {
		state := "failed"
		if live.Name.Source == agentclient.SessionNameUser {
			state = "done"
		}
		setWindowOption(windowID, optAutoNameState, state)
		return nil
	}

	// Persist provenance before the native write. If the adapter cannot write,
	// this same value is the tracker-owned priority-3 fallback.
	setWindowOption(windowID, optGeneratedName, name)
	setWindowOption(windowID, optAutoNameNative, "0")
	writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	writeResult := attemptNativeSessionName(writeCtx, adapter, live, name)
	cancel()
	if !writeResult.KeepGenerated {
		unsetWindowOption(windowID, optGeneratedName)
	}
	if writeResult.Native {
		setWindowOption(windowID, optAutoNameNative, "1")
	}
	setWindowOption(windowID, optAutoNameState, "done")
	_ = runTmuxSyncNames(nil)
	return nil
}

func runAutoNameModel(ctx context.Context, model autoNameModel, prompt string) (string, error) {
	tmp, err := os.MkdirTemp("", "agent-auto-name-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	tasksPath := filepath.Join(tmp, "tasks.json")
	outPath := filepath.Join(tmp, "out.json")
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{"name": map[string]any{
			"type": "string", "minLength": 1, "maxLength": autoNameMaxRunes,
		}},
		"required":             []string{"name"},
		"additionalProperties": false,
	}
	namingPrompt := "为下面的 AI 编程会话生成一个易识别的短名称。保留关键项目、功能或故障名；中文用 4-12 个字，英文用 2-6 个词。不要状态前缀、引号、序号、解释或句号。\n\n会话内容：\n" + boundAutoNamePrompt(prompt)
	tasks := []map[string]any{{
		"name": "session-name", "provider": model.Provider, "model": model.Model,
		"engine": model.Engine, "level": "probe", "prompt": namingPrompt,
		"schema": schema, "bare": model.Bare, "minimal_config": model.MinimalConfig,
		"cwd": tmp,
	}}
	data, err := json.Marshal(tasks)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(tasksPath, data, 0o600); err != nil {
		return "", err
	}
	hl, err := findAgentHL()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, hl, "dispatch", "--tasks", tasksPath, "--output", outPath,
		"--max-parallel", "1", "--timeout", "25", "--batch-timeout", "30")
	cmd.Env = append(os.Environ(), "HL_TASK_REF=agent-session-auto-name")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("agent-hl dispatch: %w: %s", err, strings.TrimSpace(string(output)))
	}
	outData, err := os.ReadFile(outPath)
	if err != nil {
		return "", err
	}
	var result struct {
		Results []struct {
			Status           string `json:"status"`
			StructuredOutput struct {
				Name string `json:"name"`
			} `json:"structured_output"`
		} `json:"results"`
	}
	if json.Unmarshal(outData, &result) != nil || len(result.Results) == 0 || result.Results[0].Status != "ok" {
		return "", fmt.Errorf("agent-hl returned no successful structured result")
	}
	return result.Results[0].StructuredOutput.Name, nil
}

func findAgentHL() (string, error) {
	if path, err := exec.LookPath("agent-hl-cli"); err == nil {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	for _, path := range []string{
		filepath.Join(home, ".hat-env", "bin", "agent-hl", "agent-hl-cli"),
		filepath.Join(home, ".claude", "bin", "agent-hl", "agent-hl-cli"),
	} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("agent-hl-cli not found")
}
