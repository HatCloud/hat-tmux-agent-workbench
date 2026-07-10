package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/david/agent-tracker/internal/paths"
)

type appConfig struct {
	Keys        keyConfig          `json:"keys"`
	Devices     []string           `json:"devices,omitempty"`
	StatusRight *statusRightConfig `json:"status_right,omitempty"`
	WindowName  *windowNameConfig  `json:"window_name,omitempty"`
	WindowNav   *windowNavConfig   `json:"window_nav,omitempty"`
	// LayoutDefault: 新建 agent 窗口的默认布局模式（auto/landscape/portrait，空=auto）。
	LayoutDefault string `json:"layout_default,omitempty"`
	// StatusPosition: tmux status line 位置策略（auto/top/bottom，空=auto；auto 跟随布局朝向）。
	StatusPosition string `json:"status_position,omitempty"`
	// TimerTimezone: timer 墙上时间的时区（auto/IANA/UTC offset，空=UTC+8）。
	TimerTimezone string `json:"timer_timezone,omitempty"`
	// IconSet: 状态栏图标集（nerd/emoji/ascii，空=nerd）。唯一权威在 config，读取见 activeIconSet()。
	IconSet string `json:"icon_set,omitempty"`
}

func layoutDefaultSetting(cfg appConfig) string {
	if cfg.LayoutDefault == "" {
		return "auto"
	}
	return cfg.LayoutDefault
}

func statusPositionSetting(cfg appConfig) string {
	if cfg.StatusPosition == "" {
		return "auto"
	}
	return cfg.StatusPosition
}

func timerTimezoneSetting(cfg appConfig) string {
	if strings.TrimSpace(cfg.TimerTimezone) == "" {
		return "UTC+8"
	}
	return strings.TrimSpace(cfg.TimerTimezone)
}

func iconSetSetting(cfg appConfig) string {
	switch strings.TrimSpace(cfg.IconSet) {
	case "emoji", "ascii":
		return strings.TrimSpace(cfg.IconSet)
	default:
		return "nerd"
	}
}

// nextInCycle returns the value after cur in opts, wrapping around.
func nextInCycle(cur string, opts []string) string {
	for i, o := range opts {
		if o == cur {
			return opts[(i+1)%len(opts)]
		}
	}
	return opts[0]
}

// cycleLayoutDefault advances auto→landscape→portrait→auto and persists it.
func cycleLayoutDefault() (string, error) {
	next := ""
	err := updateAppConfig(func(cfg *appConfig) {
		next = nextInCycle(layoutDefaultSetting(*cfg), []string{"auto", "landscape", "portrait"})
		if next == "auto" {
			cfg.LayoutDefault = ""
		} else {
			cfg.LayoutDefault = next
		}
	})
	return next, err
}

// cycleStatusPosition advances auto→top→bottom→auto and persists it.
func cycleStatusPosition() (string, error) {
	next := ""
	err := updateAppConfig(func(cfg *appConfig) {
		next = nextInCycle(statusPositionSetting(*cfg), []string{"auto", "top", "bottom"})
		if next == "auto" {
			cfg.StatusPosition = ""
		} else {
			cfg.StatusPosition = next
		}
	})
	return next, err
}

// cycleIconSet advances nerd→emoji→ascii→nerd and persists it.
func cycleIconSet() (string, error) {
	next := ""
	err := updateAppConfig(func(cfg *appConfig) {
		next = nextInCycle(iconSetSetting(*cfg), []string{"nerd", "emoji", "ascii"})
		if next == "nerd" {
			cfg.IconSet = ""
		} else {
			cfg.IconSet = next
		}
	})
	return next, err
}

// windowNavConfig persists the prefix-w panel's grouping/sorting choices.
// Empty fields fall back to defaults (session / activity / desc).
type windowNavConfig struct {
	GroupBy  string `json:"group_by,omitempty"`
	OrderBy  string `json:"order_by,omitempty"`
	OrderDir string `json:"order_dir,omitempty"`
}

func windowNavSettings(cfg appConfig) (groupBy, orderBy, orderDir string) {
	groupBy, orderBy, orderDir = "session", "activity", "desc"
	if cfg.WindowNav == nil {
		return
	}
	if cfg.WindowNav.GroupBy != "" {
		groupBy = cfg.WindowNav.GroupBy
	}
	if cfg.WindowNav.OrderBy != "" {
		orderBy = cfg.WindowNav.OrderBy
	}
	if cfg.WindowNav.OrderDir != "" {
		orderDir = cfg.WindowNav.OrderDir
	}
	return
}

func saveWindowNavSettings(groupBy, orderBy, orderDir string) {
	_ = updateAppConfig(func(cfg *appConfig) {
		if groupBy == "session" && orderBy == "activity" && orderDir == "desc" {
			cfg.WindowNav = nil // all defaults → keep config clean
			return
		}
		cfg.WindowNav = &windowNavConfig{GroupBy: groupBy, OrderBy: orderBy, OrderDir: orderDir}
	})
}

type statusRightConfig struct {
	CPU          *bool `json:"cpu,omitempty"`
	Network      *bool `json:"network,omitempty"`
	Memory       *bool `json:"memory,omitempty"`
	MemoryTotals *bool `json:"memory_totals,omitempty"`
	TodoPreview  *bool `json:"todo_preview,omitempty"`
	Todos        *bool `json:"todos,omitempty"`
	FlashMoe     *bool `json:"flash_moe,omitempty"`
	Host         *bool `json:"host,omitempty"`
}

const (
	windowNameOptionPath   = "wn_path"
	windowNameOptionModel  = "wn_model"
	windowNameOptionStatus = "wn_status"
)

type windowNameConfig struct {
	ShowPath   *bool `json:"show_path,omitempty"`
	ShowModel  *bool `json:"show_model,omitempty"`
	ShowStatus *bool `json:"show_status,omitempty"`
}

func (cfg *windowNameConfig) isDefault() bool {
	if cfg == nil {
		return true
	}
	return cfg.ShowPath == nil && cfg.ShowModel == nil && cfg.ShowStatus == nil
}

func windowNameShowPath(cfg appConfig) bool {
	if cfg.WindowName == nil {
		return true
	}
	return derefBool(cfg.WindowName.ShowPath, true)
}

func windowNameShowModel(cfg appConfig) bool {
	if cfg.WindowName == nil {
		return true
	}
	return derefBool(cfg.WindowName.ShowModel, true)
}

func windowNameShowStatus(cfg appConfig) bool {
	if cfg.WindowName == nil {
		return true
	}
	return derefBool(cfg.WindowName.ShowStatus, true)
}

func toggleWindowNameOption(option string) error {
	return updateAppConfig(func(cfg *appConfig) {
		if cfg.WindowName == nil {
			cfg.WindowName = &windowNameConfig{}
		}
		switch option {
		case windowNameOptionPath:
			enabled := !windowNameShowPath(*cfg)
			if enabled {
				cfg.WindowName.ShowPath = nil
			} else {
				cfg.WindowName.ShowPath = boolPtr(false)
			}
		case windowNameOptionModel:
			enabled := !windowNameShowModel(*cfg)
			if enabled {
				cfg.WindowName.ShowModel = nil
			} else {
				cfg.WindowName.ShowModel = boolPtr(false)
			}
		case windowNameOptionStatus:
			enabled := !windowNameShowStatus(*cfg)
			if enabled {
				cfg.WindowName.ShowStatus = nil
			} else {
				cfg.WindowName.ShowStatus = boolPtr(false)
			}
		}
		if cfg.WindowName.isDefault() {
			cfg.WindowName = nil
		}
	})
}

type keyConfig struct {
	MoveLeft   string `json:"move_left"`
	MoveRight  string `json:"move_right"`
	MoveUp     string `json:"move_up"`
	MoveDown   string `json:"move_down"`
	Edit       string `json:"edit"`
	Cancel     string `json:"cancel"`
	AddTodo    string `json:"add_todo"`
	ToggleTodo string `json:"toggle_todo"`
	Destroy    string `json:"destroy"`
	Confirm    string `json:"confirm"`
	Back       string `json:"back"`
	DeleteTodo string `json:"delete_todo"`
	Help       string `json:"help"`
	FocusAI    string `json:"focus_ai"`
	FocusGit   string `json:"focus_git"`
	FocusRun   string `json:"focus_run"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agent <palette|windows|window-timer|tmux|tracker|ime>")
	}
	switch args[0] {
	case "palette":
		return runPalette(args[1:])
	case "windows":
		return runWindowNavDirect(args[1:])
	case "window-timer":
		return runWindowTimerDirect(args[1:])
	case "tmux":
		return runTmuxCommand(args[1:])
	case "tracker":
		return runTracker(args[1:])
	case "ime":
		return runImeCommand(args[1:])
	default:
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
}

func runTmuxCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agent tmux <on-focus|right-status|sync-names|reflow-focus|layout-default|status-position>")
	}
	switch args[0] {
	case "on-focus":
		return runTmuxOnFocus(args[1:])
	case "right-status":
		return runTmuxRightStatus(args[1:])
	case "sync-names":
		return runTmuxSyncNames(args[1:])
	case "reflow-focus":
		return runTmuxReflowFocus(args[1:])
	case "layout-default":
		fmt.Println(layoutDefaultSetting(loadAppConfig()))
		return nil
	case "status-position":
		fmt.Println(statusPositionSetting(loadAppConfig()))
		return nil
	default:
		return fmt.Errorf("unknown tmux subcommand: %s", args[0])
	}
}

// runTmuxReflowFocus reconciles a single window's layout orientation with its
// configured mode. Wired to focus/resize hooks so a window adapts the moment it's
// selected after the terminal switched between portrait and landscape.
func runTmuxReflowFocus(args []string) error {
	fs := flag.NewFlagSet("agent tmux reflow-focus", flag.ContinueOnError)
	window := fs.String("window", "", "tmux window id to reconcile")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *window == "" {
		return fmt.Errorf("--window is required")
	}
	// Trailing-debounce: a fullscreen drag fires many window-resized events in a
	// row. Only the last reflow request (after the size settles) should reflow.
	if reflowDebounceClaim(*window) {
		reconcileWindowOrientation(*window)
	}
	return nil
}

// composeWindowName builds the window name for the no-live-session path.
// Returns session label (if any) or project name; no index prefix since tmux
// status bar already shows the window index before the name.
func composeWindowName(client, _ string, project, sessionName string) string {
	if strings.TrimSpace(client) == "" {
		return ""
	}
	_, label := splitSessionLabel(sessionName)
	if label != "" {
		return label
	}
	return abbrevProject(project)
}

// agentNameBase returns the abbreviated project name when client is set.
// The provider parameter is retained for signature compatibility but no longer shown.
func agentNameBase(client, _, project string) string {
	if strings.TrimSpace(client) == "" {
		return ""
	}
	return abbrevProject(project)
}

var datePrefixRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-`)

// abbrevProject strips a leading YYYY-MM-DD- date prefix (common in task dirs)
// then truncates over 15 runes with an ellipsis.
// abbrevPath abbreviates intermediate path segments to their first rune
// (2 runes for hidden dirs starting with '.'), replacing $HOME with '~'.
// ~/Projects/foo → ~/P/foo, ~/.hat-config/x → ~/.h/x
func abbrevPath(path string) string {
	home := homeDir()
	if strings.HasPrefix(path, home) {
		path = "~" + path[len(home):]
	}
	parts := strings.Split(path, "/")
	if len(parts) <= 2 {
		return path
	}
	result := parts[0]
	for _, seg := range parts[1 : len(parts)-1] {
		r := []rune(seg)
		switch {
		case len(r) == 0:
			result += "/"
		case r[0] == '.' && len(r) > 1:
			result += "/" + string(r[:2]) // ".h" for ".hat-config"
		default:
			result += "/" + string(r[:1])
		}
	}
	return result + "/" + parts[len(parts)-1]
}

func abbrevProject(project string) string {
	p := datePrefixRe.ReplaceAllString(strings.TrimSpace(project), "")
	const max = 15
	if r := []rune(p); len(r) > max {
		return string(r[:max]) + "…"
	}
	return p
}

// projectNameCache caches projectDisplayName results for `path` so the 1s
// poll loop doesn't fork `git` for every window. TTL is generous because
// worktree membership is stable for a given path; the cache is keyed on
// the cleaned absolute path.
var (
	projectNameCacheMu sync.RWMutex
	projectNameCache   = map[string]projectNameCacheEntry{}
)

type projectNameCacheEntry struct {
	name      string
	expiresAt time.Time
}

const projectNameCacheTTL = 60 * time.Second

// mainRepoPath returns the main worktree root for `path` if path lives in a
// git worktree; otherwise returns "". A single git call returns both
// --git-common-dir and --git-dir; in a worktree they differ (git-dir points
// into `<main>/.git/worktrees/<name>`), in a non-worktree they are equal.
// main repo root = filepath.Dir(commonDir).
func mainRepoPath(path string) string {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "" || clean == "." {
		return ""
	}
	out, err := runCommandOutput(3*time.Second, "git", "-C", clean, "rev-parse", "--git-common-dir", "--git-dir")
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return ""
	}
	commonDir := strings.TrimSpace(lines[0])
	gitDir := strings.TrimSpace(lines[1])
	if commonDir == "" || gitDir == "" {
		return ""
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(clean, commonDir)
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(clean, gitDir)
	}
	if commonDir == gitDir {
		return ""
	}
	return filepath.Dir(commonDir)
}

// projectDisplayName returns the basename to use in window titles, dir
// columns, and palette subtitles: the main repo basename when path is in a
// git worktree, otherwise filepath.Base(path). Cached for projectNameCacheTTL.
func projectDisplayName(path string) string {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "" || clean == "." {
		return filepath.Base(path)
	}
	now := time.Now()
	projectNameCacheMu.RLock()
	if entry, ok := projectNameCache[clean]; ok && now.Before(entry.expiresAt) {
		projectNameCacheMu.RUnlock()
		return entry.name
	}
	projectNameCacheMu.RUnlock()
	name := filepath.Base(clean)
	if main := mainRepoPath(clean); main != "" {
		name = filepath.Base(main)
	}
	projectNameCacheMu.Lock()
	projectNameCache[clean] = projectNameCacheEntry{name: name, expiresAt: now.Add(projectNameCacheTTL)}
	projectNameCacheMu.Unlock()
	return name
}

func abbrevClient(client string) string {
	switch strings.ToLower(strings.TrimSpace(client)) {
	case "":
		return ""
	case "claude":
		return "CL"
	case "codex":
		return "CO"
	default:
		r := []rune(strings.ToUpper(strings.TrimSpace(client)))
		if len(r) >= 2 {
			return string(r[:2])
		}
		return string(r)
	}
}

// splitSessionLabel splits a "<index>-<label>" session name into its index and
// label parts. "1-refactor" → ("1","refactor"); "1"/"1-" → ("1",""); a name
// without a numeric prefix → ("", name).
func splitSessionLabel(name string) (string, string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ""
	}
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i == 0 {
		return "", name
	}
	if i == len(name) {
		return name, ""
	}
	if name[i] == '-' {
		return name[:i], strings.TrimSpace(name[i+1:])
	}
	return "", name
}

// applyOnFocusRename names the focused window from its @agent_client /
// @agent_provider window options plus the project (git root / pane path) and
// session name. No-op when @agent_client is unset (non-agent windows).
func applyOnFocusRename(sessionID, windowID, paneID string) {
	ci := buildClaudeIndex()
	aiPane := agentAIPane(windowID, &ci)
	if aiPane == "" {
		aiPane = paneID
	}
	name := agentWindowName(windowID, sessionID, aiPane, &ci)
	if name == "" {
		return
	}
	autoRenameWindow(windowID, name)
}

func tmuxWindowOption(windowID, opt string) string {
	if strings.TrimSpace(windowID) == "" {
		return ""
	}
	// show-options -v exits non-zero when the option is unset; treat as empty.
	out, err := runTmuxOutput(tmuxWindowOptionArgs(windowID, opt)...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func tmuxWindowOptionArgs(windowID, opt string) []string {
	return []string{"show-options", "-q", "-w", "-t", windowID, "-v", opt}
}

func tmuxSessionName(sessionID string) string {
	if strings.TrimSpace(sessionID) == "" {
		return ""
	}
	out, err := runTmuxOutput("display-message", "-p", "-t", sessionID, "#{session_name}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func tmuxProjectName(paneID string) string {
	path := ""
	if strings.TrimSpace(paneID) != "" {
		if out, err := runTmuxOutput("display-message", "-p", "-t", paneID, "#{pane_current_path}"); err == nil {
			path = strings.TrimSpace(out)
		}
	}
	if path == "" {
		return ""
	}
	return projectDisplayName(path)
}

func runTmuxOnFocus(args []string) error {
	fs := flag.NewFlagSet("agent tmux on-focus", flag.ContinueOnError)
	var sessionID, windowID, paneID string
	fs.StringVar(&sessionID, "session", "", "session id")
	fs.StringVar(&windowID, "window", "", "window id")
	fs.StringVar(&paneID, "pane", "", "pane id")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !tmuxWindowIsActive(sessionID, windowID) {
		return nil
	}
	// Name the focused window from @agent_client/@agent_provider + project +
	// session, independent of whether a palette agent is tracked here.
	applyOnFocusRename(sessionID, windowID, paneID)
	return nil
}

func tmuxWindowIsActive(sessionID, windowID string) bool {
	windowID = strings.TrimSpace(windowID)
	if windowID == "" {
		return true
	}
	target := strings.TrimSpace(sessionID)
	if target == "" {
		target = windowID
	}
	activeWindowID, err := runTmuxOutput("display-message", "-p", "-t", target, "#{window_id}")
	if err != nil {
		return false
	}
	return strings.TrimSpace(activeWindowID) == windowID
}

func tmuxSessionForWindow(windowID string) (sessionID, sessionName string, err error) {
	windowID = strings.TrimSpace(windowID)
	if windowID == "" {
		return "", "", fmt.Errorf("window id is required")
	}
	out, err := runTmuxOutput("display-message", "-p", "-t", windowID, "#{session_id}\n#{session_name}")
	if err != nil {
		return "", "", err
	}
	parts := strings.SplitN(strings.TrimRight(out, "\n"), "\n", 2)
	for len(parts) < 2 {
		parts = append(parts, "")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func selectTmuxWindow(windowID string) error {
	if err := runTmux("select-window", "-t", windowID); err != nil {
		return err
	}
	return nil
}

func configPath() string {
	return paths.ConfigFile()
}

func loadAppConfig() appConfig {
	cfg := appConfig{Keys: keyConfig{
		MoveLeft:   "n",
		MoveRight:  "i",
		MoveUp:     "u",
		MoveDown:   "e",
		Edit:       "Enter",
		Cancel:     "Escape",
		AddTodo:    "a",
		ToggleTodo: "x",
		Destroy:    "D",
		Confirm:    "y",
		Back:       "Escape",
		DeleteTodo: "d",
		Help:       "?",
		FocusAI:    "M-a",
		FocusGit:   "M-g",
		FocusRun:   "M-r",
	}}
	data, err := os.ReadFile(configPath())
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runTmux(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runTmuxOutput(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func runCommandOutput(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

func runCommandCombinedOutput(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// configWriteMu serializes writers in this process; the mkdir lock below
// serializes across processes (e.g. the Go tracker and the bash setup wizard).
var configWriteMu sync.Mutex

// configLockStale bounds how long a held config lock is trusted before a waiter
// treats it as abandoned (holder crashed) and preempts it.
const configLockStale = 5 * time.Second

func configLockDir() string {
	return filepath.Join(filepath.Dir(configPath()), ".config.lock.d")
}

// acquireConfigLock takes the cross-process mkdir lock, preempting a stale lock
// (mtime older than configLockStale). Returns a release func.
func acquireConfigLock() (func(), error) {
	dir := configLockDir()
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}
	for {
		if err := os.Mkdir(dir, 0o755); err == nil {
			return func() { _ = os.Remove(dir) }, nil
		} else if !os.IsExist(err) {
			return nil, err
		}
		if info, statErr := os.Stat(dir); statErr == nil && time.Since(info.ModTime()) > configLockStale {
			_ = os.Remove(dir) // preempt abandoned lock
			continue
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// appConfigKnownKeys returns the JSON keys owned by appConfig, so the writer can
// drop cleared (omitempty) owned keys while preserving unknown keys.
func appConfigKnownKeys() map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(appConfig{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}

// updateAppConfig atomically applies update to the persisted app config under a
// cross-process lock, preserving any config keys not owned by appConfig.
func updateAppConfig(update func(*appConfig)) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	release, err := acquireConfigLock()
	if err != nil {
		return err
	}
	defer release()

	path := configPath()

	// Preserve unknown keys: start from the on-disk object, overlay owned keys.
	raw := map[string]json.RawMessage{}
	if data, readErr := os.ReadFile(path); readErr == nil {
		_ = json.Unmarshal(data, &raw)
	}

	cfg := loadAppConfig()
	update(&cfg)

	known, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	ownedNow := map[string]json.RawMessage{}
	if err := json.Unmarshal(known, &ownedNow); err != nil {
		return err
	}
	for key := range appConfigKnownKeys() {
		delete(raw, key) // drop owned keys, then re-add the ones still set
	}
	for key, val := range ownedNow {
		raw[key] = val
	}

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
