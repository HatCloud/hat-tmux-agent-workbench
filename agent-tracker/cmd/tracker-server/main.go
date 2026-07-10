package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/david/agent-tracker/internal/ipc"
	"github.com/david/agent-tracker/internal/paths"
)

const (
	statusInProgress = "in_progress"
	statusCompleted  = "completed"

	// completionGraceWindow：busy→idle 完成判定的宽限去抖窗口。首次观测到 idle 时
	// 只置 PendingCompleteAt、不通知；只有 idle 连续持续超过本窗口才真正提交完成并通知。
	// turn 边界等待后台任务/subagent 返回时的瞬态 idle（实测 <1 轮询）会在下一轮被
	// busy/shell/asking 清除，从而不误发「✅ 任务已完成」。每秒轮询下约需 2–3 个连续
	// idle 轮询才提交，对真正完成增加 ≤ ~2s 延迟。
	completionGraceWindow = 2 * time.Second
)

type taskRecord struct {
	SessionID      string
	SessionName    string
	WindowID       string
	WindowName     string
	Pane           string
	Summary        string
	CompletionNote string
	StartedAt      time.Time
	CompletedAt    *time.Time
	// PendingCompleteAt：首次在 in_progress 任务上观测到 idle 的时刻；非 nil 表示
	// 完成判定处于宽限期。活动信号（busy/shell/asking）到来即清空、作废本次待发完成。
	PendingCompleteAt *time.Time
	Status            string
	Asking            bool
	Acknowledged      bool
}

// graceElapsed 报告自 pendingAt 起是否已满 completionGraceWindow。
func graceElapsed(pendingAt time.Time, now time.Time) bool {
	return !now.Before(pendingAt.Add(completionGraceWindow))
}

type storedSettings struct {
	NotificationsEnabled  *bool   `json:"notifications_enabled,omitempty"`
	NotificationGroupMode *string `json:"notification_group_mode,omitempty"`
}

// Notification grouping modes. "single" keeps one notification that newer ones
// replace; "per_window" gives each tmux window its own notification so they
// coexist in Notification Center.
const (
	notificationGroupSingle    = "single"
	notificationGroupPerWindow = "per_window"
)

type tmuxTarget struct {
	SessionName string
	SessionID   string
	WindowName  string
	WindowID    string
	PaneID      string
	WindowIndex string
	PaneIndex   string
}

type uiSubscriber struct {
	enc *json.Encoder
}

type server struct {
	mu                    sync.Mutex
	socketPath            string
	notificationsEnabled  bool
	notificationGroupMode string
	tasks                 map[string]*taskRecord
	subscribers           map[*uiSubscriber]struct{}
	settingsPath          string
	// notifiedWindows tracks windows that currently have an outstanding
	// per-window system notification, so it can be removed the moment that
	// window's 🔔 clears by ANY path (acknowledge, a new turn restarting a
	// completed task, task deletion) — keeping notification and 🔔 同生同灭.
	notifiedWindows map[string]bool
	// remoteBellNotified tracks ssh windows that currently carry a local
	// notification mirroring a remote machine's 🔔, so it can be removed when the
	// remote bell clears or the window closes. See remote_bell.go.
	remoteBellNotified map[string]bool
}

func newServer() *server {
	return &server{
		socketPath:            socketPath(),
		notificationsEnabled:  true,
		notificationGroupMode: notificationGroupSingle,
		tasks:                 make(map[string]*taskRecord),
		subscribers:           make(map[*uiSubscriber]struct{}),
		settingsPath:          settingsStorePath(),
		notifiedWindows:       make(map[string]bool),
		remoteBellNotified:    make(map[string]bool),
	}
}

func main() {
	srv := newServer()
	if err := srv.run(); err != nil {
		log.Fatal(err)
	}
}

func (s *server) run() error {
	if err := s.loadSettings(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(s.socketPath); err != nil {
		return err
	}
	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		return err
	}
	defer ln.Close()
	defer os.Remove(s.socketPath)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Mirror remote machines' 🔔 onto their local ssh windows (see remote_bell.go).
	go s.pollRemoteBells()

	errCh := make(chan error, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				errCh <- err
				return
			}
			go s.handleConn(conn)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
		return fmt.Errorf("tracker-server stopped: %s", sig)
	}
}

func (s *server) handleConn(conn net.Conn) {
	defer conn.Close()

	dec := json.NewDecoder(bufio.NewReader(conn))
	enc := json.NewEncoder(conn)

	var sub *uiSubscriber
	defer func() {
		if sub != nil {
			s.removeSubscriber(sub)
		}
	}()

	for {
		var env ipc.Envelope
		if err := dec.Decode(&env); err != nil {
			return
		}
		switch env.Kind {
		case "command":
			if err := s.handleCommand(env); err != nil {
				log.Printf("command error: %v", err)
			}
			reply := ipc.Envelope{Kind: "ack"}
			if err := enc.Encode(&reply); err != nil {
				return
			}
		case "ui-register":
			if sub == nil {
				sub = &uiSubscriber{enc: enc}
				s.addSubscriber(sub)
			}
			if err := s.sendStateTo(sub); err != nil {
				return
			}
		default:
			log.Printf("unknown message: %+v", env)
		}
	}
}

func (s *server) handleCommand(env ipc.Envelope) error {
	switch env.Command {
	case "start_task":
		target, err := requireSessionWindow(env)
		if err != nil {
			return err
		}
		summary := firstNonEmpty(env.Summary, env.Message)
		if summary == "" {
			return fmt.Errorf("start_task requires summary")
		}
		if err := s.startTask(target, summary); err != nil {
			return err
		}
		s.broadcastStateAsync()
		s.statusRefreshAsync()
		return nil
	case "finish_task":
		target, err := requireSessionWindow(env)
		if err != nil {
			return err
		}
		note := firstNonEmpty(env.Summary, env.Message)
		notify, err := s.finishTask(target, note)
		if err != nil {
			return err
		}
		if notify && s.notificationsAreEnabled() {
			go s.notifyResponded(target)
		}
		s.broadcastStateAsync()
		s.statusRefreshAsync()
		return nil
	case "notify":
		target, err := requireSessionWindow(env)
		if err != nil {
			return err
		}
		message := firstNonEmpty(env.Summary, env.Message)
		if message == "" {
			return fmt.Errorf("notify requires summary")
		}
		if s.notificationsAreEnabled() {
			if err := sendSystemNotification(notificationTitleForTarget(target), message, notificationActionForTarget(target), s.notificationGroup(target.WindowID)); err != nil {
				return err
			}
		}
		return nil
	case "update_task":
		target, err := requireSessionWindow(env)
		if err != nil {
			return err
		}
		summary := firstNonEmpty(env.Summary, env.Message)
		if summary == "" {
			return fmt.Errorf("update_task requires summary")
		}
		if err := s.updateTaskSummary(target, summary); err != nil {
			return err
		}
		s.broadcastStateAsync()
		s.statusRefreshAsync()
		return nil
	case "mark_asking":
		target, err := requireSessionWindow(env)
		if err != nil {
			return err
		}
		if changed := s.markTaskAsking(target, env.Asking); changed {
			// Like the 🔔: only alert when the user isn't already watching this window.
			if env.Asking && s.notificationsAreEnabled() && !windowIsBeingWatched(target.WindowID) {
				go s.notifyAsking(target)
			}
			s.broadcastStateAsync()
			s.statusRefreshAsync()
		}
		return nil
	case "set_notification_group_mode":
		if err := s.setNotificationGroupMode(env.Message); err != nil {
			return err
		}
		s.broadcastStateAsync()
		return nil
	case "notifications_toggle":
		enabled, err := s.toggleNotifications()
		if err != nil {
			return err
		}
		if client := strings.TrimSpace(env.Client); client != "" {
			status := "OFF"
			if enabled {
				status = "ON"
			}
			if err := runTmux("display-message", "-c", client, "push notifications: "+status); err != nil {
				log.Printf("notification toggle message error: %v", err)
			}
		}
		s.broadcastStateAsync()
		return nil
	case "acknowledge":
		target, err := requireSessionWindow(env)
		if err != nil {
			return err
		}
		if s.acknowledgeTask(target.SessionID, target.WindowID, target.PaneID) {
			go s.clearWindowNotification(target.WindowID)
		}
		s.broadcastStateAsync()
		s.statusRefreshAsync()
		return nil
	case "delete_task":
		target, err := requireSessionWindow(env)
		if err != nil {
			return err
		}
		if err := s.deleteTask(target.SessionID, target.WindowID, target.PaneID); err != nil {
			return err
		}
		s.broadcastStateAsync()
		s.statusRefreshAsync()
		return nil
	default:
		return fmt.Errorf("unknown command %q", env.Command)
	}
}

func (s *server) startTask(target tmuxTarget, summary string) error {
	if target.SessionID == "" || target.WindowID == "" {
		return fmt.Errorf("cannot create task: missing session or window ID")
	}
	target = normalizeTargetNames(target)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	key := taskKey(target.SessionID, target.WindowID, target.PaneID)
	t, ok := s.tasks[key]
	if !ok {
		s.tasks[key] = &taskRecord{
			SessionID:    target.SessionID,
			SessionName:  strings.TrimSpace(target.SessionName),
			WindowID:     target.WindowID,
			WindowName:   strings.TrimSpace(target.WindowName),
			Pane:         target.PaneID,
			Summary:      summary,
			StartedAt:    now,
			Status:       statusInProgress,
			Acknowledged: true,
		}
		return nil
	}
	mergeTaskNamesFromTarget(t, target)
	if !(t.Status == statusInProgress && strings.TrimSpace(t.Summary) != "") {
		t.Summary = summary
	}
	t.StartedAt = now
	t.Status = statusInProgress
	t.CompletedAt = nil
	t.CompletionNote = ""
	t.PendingCompleteAt = nil // 重新活动，作废任何待发完成
	t.Acknowledged = true
	return nil
}

func (s *server) updateTaskSummary(target tmuxTarget, summary string) error {
	if target.SessionID == "" || target.WindowID == "" {
		return fmt.Errorf("cannot update task: missing session or window ID")
	}
	target = normalizeTargetNames(target)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	key := taskKey(target.SessionID, target.WindowID, target.PaneID)
	t, ok := s.tasks[key]
	if !ok {
		t = &taskRecord{
			SessionID:    target.SessionID,
			SessionName:  strings.TrimSpace(target.SessionName),
			WindowID:     target.WindowID,
			WindowName:   strings.TrimSpace(target.WindowName),
			Pane:         target.PaneID,
			StartedAt:    now,
			Status:       statusInProgress,
			Acknowledged: true,
		}
		s.tasks[key] = t
	}
	mergeTaskNamesFromTarget(t, target)
	t.Summary = summary
	if t.Status == "" {
		t.Status = statusInProgress
	}
	if t.StartedAt.IsZero() {
		t.StartedAt = now
	}
	return nil
}

// isWindowWatched 间接指向 windowIsBeingWatched，便于单测注入替身——该函数会 shell
// 出去跑 tmux/lsappinfo，注入后测试不依赖外部环境即可确定性验证完成通知路径。
var isWindowWatched = windowIsBeingWatched

// finishTask 由 reconcileTask 在 Claude 进入 idle 时每秒重发的 finish_task 驱动。
// 它不再「见 idle 即完成」，而是带宽限去抖：首次 idle 仅开始计时（不通知），只有 idle
// 持续超过 completionGraceWindow 才真正提交完成并决定是否通知。turn 边界等待后台
// 任务/subagent 的瞬态 idle 会在下一轮被 busy/shell/asking 清掉 PendingCompleteAt，
// 故不误发完成通知。非 in_progress 任务（含已 completed）的 idle 一律 no-op。
func (s *server) finishTask(target tmuxTarget, note string) (bool, error) {
	if target.SessionID == "" || target.WindowID == "" {
		return false, nil // silently ignore - pane likely died
	}
	target = normalizeTargetNames(target)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	key := taskKey(target.SessionID, target.WindowID, target.PaneID)
	t, ok := s.tasks[key]
	// 非 in_progress（不存在 / 已 completed / 其它）→ no-op：既不凭空造 completed
	// 任务，也对已完成幂等不重复通知。
	if !ok || t.Status != statusInProgress {
		return false, nil
	}
	if t.Summary == "" {
		t.Summary = note
	}
	mergeTaskNamesFromTarget(t, target)

	// 宽限第一段：首次 idle，只开始计时，不改 Status、不通知。
	if t.PendingCompleteAt == nil {
		t.PendingCompleteAt = &now
		return false, nil
	}
	// 宽限未满：继续等待后续 idle 轮询。
	if !graceElapsed(*t.PendingCompleteAt, now) {
		return false, nil
	}

	// 宽限已满：真正提交完成。
	t.Status = statusCompleted
	t.Asking = false // a finished task is no longer waiting for input
	t.CompletedAt = &now
	t.PendingCompleteAt = nil
	if note != "" {
		t.CompletionNote = note
	}
	// Auto-acknowledge only if the user is actually watching: this window selected
	// AND the terminal frontmost. Finishing while you watch shouldn't raise a 🔔
	// you can't clear without leaving and returning; but a selected window whose
	// terminal is backgrounded is NOT watched, so it should still ring + notify.
	t.Acknowledged = isWindowWatched(target.WindowID)
	// The completion notification rides the exact same condition as the 🔔:
	// raise it only on a fresh completion the user isn't already watching. Bell
	// and notification are decided together here, cleared together in acknowledge.
	return !t.Acknowledged, nil
}

func (s *server) markTaskAsking(target tmuxTarget, asking bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := taskKey(target.SessionID, target.WindowID, target.PaneID)
	t, ok := s.tasks[key]
	if !ok || t.Status != statusInProgress {
		return false
	}
	// 活动信号到来即作废待发完成——必须先于下面的 Asking==asking early-return，
	// 否则 shell/busy 路径（Asking 已为 false、值未变）会在 early-return 处跳过清除，
	// 让 turn 边界瞬态 idle 留下的 PendingCompleteAt 残留并最终误发完成通知。
	t.PendingCompleteAt = nil
	if t.Asking == asking {
		return false
	}
	t.Asking = asking
	if asking {
		t.Acknowledged = false // entering asking state needs fresh attention
	}
	return true
}

// acknowledgeTask marks tasks as read when the user focuses a window. The 🔔 is
// rendered per-window, so acknowledgement is window-scoped: focusing any pane in
// the window clears every task under it, not just the one on the focused pane
// (the user may land on the git/run pane while the agent task lives on the ai pane).
// acknowledgeTask marks a window's tasks read and reports whether any were
// previously unread (so callers can clear the window's notification only when
// there was actually something to clear).
func (s *server) acknowledgeTask(sessionID, windowID, paneID string) bool {
	_ = paneID // window-scoped: pane intentionally ignored
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, t := range s.tasks {
		if t.SessionID == sessionID && t.WindowID == windowID {
			if !t.Acknowledged {
				changed = true
			}
			t.Acknowledged = true
		}
	}
	return changed
}

// clearWindowNotification removes a window's system notification when the user
// focuses it — mirrors the per-window 🔔 clear. Only meaningful in per_window
// grouping mode; single mode shares one group the next notification replaces.
func (s *server) clearWindowNotification(windowID string) {
	s.mu.Lock()
	mode := s.notificationGroupMode
	delete(s.notifiedWindows, windowID)
	s.mu.Unlock()
	if mode != notificationGroupPerWindow || strings.TrimSpace(windowID) == "" {
		return
	}
	removeNotificationGroup(s.notificationGroup(windowID))
}

// windowHasBellLocked reports whether windowID still shows a 🔔: any task under
// it that is completed-unacknowledged or asking-unacknowledged. Caller holds s.mu.
func (s *server) windowHasBellLocked(windowID string) bool {
	for _, t := range s.tasks {
		if t.WindowID != windowID || t.Acknowledged {
			continue
		}
		if t.Status == statusCompleted {
			return true
		}
		if t.Status == statusInProgress && t.Asking {
			return true
		}
	}
	return false
}

// reconcileNotifications removes lingering per-window notifications for windows
// whose 🔔 has cleared through any path other than acknowledge — a new turn
// restarting a completed task, a deleted task, or a cleared asking flag. This is
// the single sweep that keeps notification and 🔔 同生同灭; it runs on every
// state change via broadcastState.
func (s *server) reconcileNotifications() {
	s.mu.Lock()
	if s.notificationGroupMode != notificationGroupPerWindow {
		// single mode shares one group (replaced, not removed) — nothing to sweep.
		if len(s.notifiedWindows) > 0 {
			s.notifiedWindows = make(map[string]bool)
		}
		s.mu.Unlock()
		return
	}
	var stale []string
	for wid := range s.notifiedWindows {
		if !s.windowHasBellLocked(wid) {
			stale = append(stale, wid)
			delete(s.notifiedWindows, wid)
		}
	}
	groups := make([]string, len(stale))
	for i, wid := range stale {
		groups[i] = "agent-tracker-" + wid
	}
	s.mu.Unlock()
	for _, g := range groups {
		removeNotificationGroup(g)
	}
}

func removeNotificationGroup(group string) {
	if runtime.GOOS != "darwin" || strings.TrimSpace(group) == "" {
		return
	}
	bin, err := exec.LookPath("terminal-notifier")
	if err != nil {
		return
	}
	_ = exec.Command(bin, "-remove", group).Run()
}

func (s *server) deleteTask(sessionID, windowID, paneID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, taskKey(sessionID, windowID, paneID))
	return nil
}

func normalizeTargetNames(target tmuxTarget) tmuxTarget {
	if strings.TrimSpace(target.SessionName) == strings.TrimSpace(target.SessionID) {
		target.SessionName = ""
	}
	if strings.TrimSpace(target.WindowName) == strings.TrimSpace(target.WindowID) {
		target.WindowName = ""
	}
	return target
}

func mergeTaskNamesFromTarget(task *taskRecord, target tmuxTarget) {
	if task == nil {
		return
	}
	if sessionName := strings.TrimSpace(target.SessionName); sessionName != "" {
		task.SessionName = sessionName
	}
	if windowName := strings.TrimSpace(target.WindowName); windowName != "" {
		task.WindowName = windowName
	}
}

func (s *server) loadSettings() error {
	data, err := os.ReadFile(s.settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var stored storedSettings
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	s.mu.Lock()
	if stored.NotificationsEnabled != nil {
		s.notificationsEnabled = *stored.NotificationsEnabled
	}
	if stored.NotificationGroupMode != nil {
		s.notificationGroupMode = normalizeGroupMode(*stored.NotificationGroupMode)
	}
	s.mu.Unlock()
	return nil
}

func normalizeGroupMode(mode string) string {
	if strings.TrimSpace(mode) == notificationGroupPerWindow {
		return notificationGroupPerWindow
	}
	return notificationGroupSingle
}

// saveSettingsLocked persists every stored setting; callers hold s.mu.
func (s *server) saveSettingsLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.settingsPath), 0o755); err != nil {
		return err
	}
	enabled := s.notificationsEnabled
	mode := s.notificationGroupMode
	data, err := json.MarshalIndent(storedSettings{
		NotificationsEnabled:  &enabled,
		NotificationGroupMode: &mode,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.settingsPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.settingsPath)
}

func (s *server) setNotificationGroupMode(mode string) error {
	mode = normalizeGroupMode(mode)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notificationGroupMode = mode
	return s.saveSettingsLocked()
}

// notificationGroup returns the terminal-notifier -group for a target. In
// per_window mode each window gets its own group so notifications coexist;
// otherwise a single shared group means newer notifications replace older ones.
func (s *server) notificationGroup(windowID string) string {
	s.mu.Lock()
	mode := s.notificationGroupMode
	s.mu.Unlock()
	if mode == notificationGroupPerWindow {
		if wid := strings.TrimSpace(windowID); wid != "" {
			return "agent-tracker-" + wid
		}
	}
	return "agent-tracker"
}

func (s *server) notificationsAreEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notificationsEnabled
}

func (s *server) toggleNotifications() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notificationsEnabled = !s.notificationsEnabled
	if err := s.saveSettingsLocked(); err != nil {
		return false, err
	}
	return s.notificationsEnabled, nil
}

func (s *server) notifyResponded(target tmuxTarget) {
	target = s.fillTargetNamesFromTask(target)
	message := "✅ 任务已完成"
	if note := strings.TrimSpace(s.summaryForTask(target.SessionID, target.WindowID, target.PaneID)); note != "" {
		if title := notificationTitleForTarget(target); !strings.EqualFold(note, title) {
			message += " · " + note
		}
	}
	title := notificationTitleForTarget(target)
	action := notificationActionForTarget(target)
	if err := sendSystemNotification(title, message, action, s.notificationGroup(target.WindowID)); err != nil {
		log.Printf("notification error: %v", err)
		return
	}
	s.markWindowNotified(target.WindowID)
}

// notifyAsking fires when a task transitions into the asking/waiting state so
// the user knows an agent needs an answer to proceed.
func (s *server) notifyAsking(target tmuxTarget) {
	target = s.fillTargetNamesFromTask(target)
	title := notificationTitleForTarget(target)
	action := notificationActionForTarget(target)
	if err := sendSystemNotification(title, "❓ 有问题需要你回答", action, s.notificationGroup(target.WindowID)); err != nil {
		log.Printf("notification error: %v", err)
		return
	}
	s.markWindowNotified(target.WindowID)
}

// markWindowNotified records that windowID has an outstanding per-window
// notification, so reconcileNotifications can later remove it when the 🔔 clears.
func (s *server) markWindowNotified(windowID string) {
	if strings.TrimSpace(windowID) == "" {
		return
	}
	s.mu.Lock()
	if s.notificationGroupMode == notificationGroupPerWindow {
		s.notifiedWindows[windowID] = true
	}
	s.mu.Unlock()
}

func (s *server) fillTargetNamesFromTask(target tmuxTarget) tmuxTarget {
	target = normalizeTargetNames(target)
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, ok := s.tasks[taskKey(target.SessionID, target.WindowID, target.PaneID)]; ok {
		if strings.TrimSpace(target.SessionName) == "" {
			target.SessionName = strings.TrimSpace(task.SessionName)
		}
		if strings.TrimSpace(target.WindowName) == "" {
			target.WindowName = strings.TrimSpace(task.WindowName)
		}
	}
	return target
}

func notificationTitleForTarget(target tmuxTarget) string {
	if wid := strings.TrimSpace(target.WindowID); wid != "" {
		// Prefer the agent's full notification name (project/name (model), no
		// status prefix), persisted by agentWindowName independent of the
		// window-tab display toggles.
		if out, err := tmuxOutput("show-options", "-wqv", "-t", wid, "@agent_notify_name"); err == nil {
			if name := strings.TrimSpace(out); name != "" {
				return name
			}
		}
		// Fallback: the live tmux window name minus the [B]/[I] status prefix
		// (which the notification text itself already conveys).
		if out, err := tmuxOutput("display-message", "-t", wid, "-p", "#{window_name}"); err == nil {
			if name := strings.TrimSpace(stripNotificationStatusPrefix(strings.TrimSpace(out))); name != "" && name != wid {
				return name
			}
		}
	}

	target = normalizeTargetNames(target)
	session := strings.TrimSpace(target.SessionName)
	if session != "" {
		session = stripSessionIndexPrefix(session)
	}
	if session == "" {
		session = strings.TrimSpace(target.SessionID)
	}
	window := strings.TrimSpace(stripNotificationStatusPrefix(strings.TrimSpace(target.WindowName)))
	if window == "" {
		window = strings.TrimSpace(target.WindowID)
	}

	if session != "" && window != "" {
		return session + " - " + window
	}
	if session != "" {
		return session
	}
	if window != "" {
		return window
	}
	return "Tracker"
}

// stripNotificationStatusPrefix removes a leading [B]/[I]/[?]/[L] status marker
// (mirrors the agent's window-name prefix; see claude_session.go).
func stripNotificationStatusPrefix(name string) string {
	for _, p := range []string{"[B] ", "[I] ", "[?] ", "[L] "} {
		if strings.HasPrefix(name, p) {
			return strings.TrimPrefix(name, p)
		}
	}
	return name
}

func stripSessionIndexPrefix(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i == 0 {
		return name
	}

	j := i
	for j < len(name) && name[j] == ' ' {
		j++
	}
	if j >= len(name) || name[j] != '-' {
		return name
	}

	j++
	for j < len(name) && name[j] == ' ' {
		j++
	}

	trimmed := strings.TrimSpace(name[j:])
	if trimmed == "" {
		return name
	}
	return trimmed
}

func (s *server) summaryForTask(sessionID, windowID, paneID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[taskKey(sessionID, windowID, paneID)]; ok {
		note := strings.TrimSpace(t.CompletionNote)
		summary := strings.TrimSpace(t.Summary)
		if note != "" && !isGenericCompletionNote(note) {
			return note
		}
		if summary != "" {
			return summary
		}
		if note != "" {
			return note
		}
	}
	return ""
}

func isGenericCompletionNote(note string) bool {
	normalized := strings.ToLower(strings.TrimSpace(note))
	normalized = strings.Trim(normalized, ".!?,;:-_()[]{}\"'` ")
	if normalized == "" {
		return true
	}
	switch normalized {
	case "done", "complete", "completed", "finished", "fixed", "resolved", "ok", "okay", "success", "successful", "all set", "all good", "implemented", "updated", "shipped":
		return true
	default:
		return false
	}
}

func (s *server) broadcastStateAsync() {
	go s.broadcastState()
}

func (s *server) broadcastState() {
	// Sweep stale per-window notifications before publishing: any window whose
	// 🔔 cleared (by acknowledge, a restarted task, or a deletion) gets its
	// system notification removed in lock-step.
	s.reconcileNotifications()
	env := s.buildStateEnvelope()
	if env == nil {
		return
	}
	s.mu.Lock()
	subs := make([]*uiSubscriber, 0, len(s.subscribers))
	for sub := range s.subscribers {
		subs = append(subs, sub)
	}
	s.mu.Unlock()

	for _, sub := range subs {
		if err := sub.enc.Encode(env); err != nil {
			s.removeSubscriber(sub)
		}
	}
}

func (s *server) statusRefreshAsync() {
	go func() {
		if err := runTmux("refresh-client", "-S"); err != nil {
			log.Printf("status refresh error: %v", err)
		}
	}()
}

func (s *server) sendState(enc *json.Encoder) {
	env := s.buildStateEnvelope()
	if env == nil {
		return
	}
	if err := enc.Encode(env); err != nil {
		log.Printf("state send error: %v", err)
	}
}

func (s *server) sendStateTo(sub *uiSubscriber) error {
	env := s.buildStateEnvelope()
	if env == nil {
		return nil
	}
	if err := sub.enc.Encode(env); err != nil {
		s.removeSubscriber(sub)
		return err
	}
	return nil
}

func (s *server) buildStateEnvelope() *ipc.Envelope {
	s.mu.Lock()
	copies := make([]*taskRecord, 0, len(s.tasks))
	for _, task := range s.tasks {
		copy := *task
		copies = append(copies, &copy)
	}
	s.mu.Unlock()

	now := time.Now()
	tasks := make([]ipc.Task, 0, len(copies))
	nameCache := make(map[string][2]string)
	for _, t := range copies {
		started := ""
		if !t.StartedAt.IsZero() {
			started = t.StartedAt.Format(time.RFC3339)
		}
		completed := ""
		var duration time.Duration
		if t.CompletedAt != nil {
			completed = t.CompletedAt.Format(time.RFC3339)
			duration = t.CompletedAt.Sub(t.StartedAt)
		} else {
			duration = now.Sub(t.StartedAt)
		}
		if duration < 0 {
			duration = 0
		}
		sessionName := strings.TrimSpace(t.SessionName)
		windowName := strings.TrimSpace(t.WindowName)
		if sessionName == strings.TrimSpace(t.SessionID) {
			sessionName = ""
		}
		if windowName == strings.TrimSpace(t.WindowID) {
			windowName = ""
		}
		if sessionName == "" || windowName == "" {
			if cached, ok := nameCache[t.WindowID]; ok {
				if sessionName == "" {
					sessionName = cached[0]
				}
				if windowName == "" {
					windowName = cached[1]
				}
			} else {
				sessName, winName, err := tmuxNamesForWindow(t.WindowID)
				if err == nil {
					nameCache[t.WindowID] = [2]string{sessName, winName}
					if sessionName == "" {
						sessionName = sessName
					}
					if windowName == "" {
						windowName = winName
					}
				}
			}
		}
		if sessionName == "" {
			sessionName = t.SessionID
		}
		if windowName == "" {
			windowName = t.WindowID
		}

		tasks = append(tasks, ipc.Task{
			SessionID:       t.SessionID,
			Session:         sessionName,
			WindowID:        t.WindowID,
			Window:          windowName,
			Pane:            t.Pane,
			Status:          t.Status,
			Asking:          t.Asking,
			Summary:         t.Summary,
			CompletionNote:  t.CompletionNote,
			StartedAt:       started,
			CompletedAt:     completed,
			DurationSeconds: duration.Seconds(),
			Acknowledged:    t.Acknowledged,
		})
	}

	msg := stateSummary(tasks)
	return &ipc.Envelope{
		Kind:    "state",
		Message: msg,
		Tasks:   tasks,
	}
}

func (s *server) addSubscriber(sub *uiSubscriber) {
	s.mu.Lock()
	s.subscribers[sub] = struct{}{}
	s.mu.Unlock()
}

func (s *server) removeSubscriber(sub *uiSubscriber) {
	s.mu.Lock()
	delete(s.subscribers, sub)
	s.mu.Unlock()
}

type notificationAction struct {
	Command string
}

func notificationActionForTarget(target tmuxTarget) *notificationAction {
	session := strings.TrimSpace(target.SessionID)
	window := strings.TrimSpace(target.WindowID)
	pane := strings.TrimSpace(target.PaneID)
	if session == "" || window == "" || pane == "" {
		return nil
	}
	// terminal-notifier runs -execute via `/bin/sh -c <command>` with a minimal
	// PATH, so use an absolute tmux path and keep targets single-quoted: the
	// session id (e.g. $0) must reach tmux literally, not be expanded by sh.
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil || strings.TrimSpace(tmuxBin) == "" {
		tmuxBin = "tmux"
	}
	jump := fmt.Sprintf("%s switch-client -t %s && %s select-window -t %s && %s select-pane -t %s",
		shellQuote(tmuxBin), shellQuote(session), shellQuote(tmuxBin), shellQuote(window), shellQuote(tmuxBin), shellQuote(pane))
	// Bring the hosting terminal to the front on click. terminal-notifier's
	// -activate is unreliable on modern macOS, and -sender suppresses the banner
	// entirely when the sender app lacks notification permission (e.g. Ghostty),
	// so activate the real terminal via `open -b <bundleid>` instead.
	if bundle := frontendTerminalBundleID(); bundle != "" {
		jump = fmt.Sprintf("/usr/bin/open -b %s; %s", shellQuote(bundle), jump)
	}
	return &notificationAction{
		Command: jump,
	}
}

// frontendTerminalBundleID returns the macOS bundle identifier of the terminal
// hosting tmux, read from the tmux global environment where macOS records the
// launching GUI app's __CFBundleIdentifier. Empty when unavailable. Used to
// activate the real terminal (e.g. Ghostty) when a notification is clicked.
func frontendTerminalBundleID() string {
	out, err := tmuxOutput("show-environment", "-g", "__CFBundleIdentifier")
	if err != nil {
		return ""
	}
	const prefix = "__CFBundleIdentifier="
	line := strings.TrimSpace(out)
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(line, prefix))
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func sendSystemNotification(title, message string, action *notificationAction, group string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Tracker"
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = title
	}
	if strings.TrimSpace(group) == "" {
		group = "agent-tracker"
	}
	switch runtime.GOOS {
	case "darwin":
		if bin, err := exec.LookPath("terminal-notifier"); err == nil {
			args := []string{"-title", title, "-message", message, "-group", group}
			if action != nil && strings.TrimSpace(action.Command) != "" {
				args = append(args, "-execute", action.Command)
			}
			cmd := exec.Command(bin, args...)
			if err := cmd.Run(); err != nil {
				return err
			}
			return nil
		}
		scriptLines := []string{fmt.Sprintf("display notification %s with title %s", strconv.Quote(message), strconv.Quote(title))}
		cmd := exec.Command("osascript", "-e", strings.Join(scriptLines, "\n"))
		if err := cmd.Run(); err != nil {
			return err
		}
	case "linux":
		if _, err := exec.LookPath("notify-send"); err != nil {
			return err
		}
		cmd := exec.Command("notify-send", title, message)
		if err := cmd.Run(); err != nil {
			return err
		}
	default:
		return nil
	}
	return nil
}

func runTmux(args ...string) error {
	cmd := exec.Command("tmux", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			return fmt.Errorf("tmux %s: %v: %s", strings.Join(args, " "), err, trimmed)
		}
		return fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func isActivePane(paneID string) bool {
	clients, err := listClients()
	if err != nil {
		return false
	}
	for _, client := range clients {
		output, err := tmuxDisplay(client, "#{pane_id}")
		if err != nil {
			continue
		}
		if strings.TrimSpace(output) == paneID {
			return true
		}
	}
	return false
}

// isActiveWindow reports whether windowID is the active window of any attached
// client. Used for window-scoped auto-acknowledgement.
func isActiveWindow(windowID string) bool {
	windowID = strings.TrimSpace(windowID)
	if windowID == "" {
		return false
	}
	clients, err := listClients()
	if err != nil {
		return false
	}
	for _, client := range clients {
		output, err := tmuxDisplay(client, "#{window_id}")
		if err != nil {
			continue
		}
		if strings.TrimSpace(output) == windowID {
			return true
		}
	}
	return false
}

// windowIsBeingWatched reports whether the user is actually looking at windowID:
// it must be the selected tmux window AND the hosting terminal must be the
// frontmost macOS app. A selected window whose terminal sits in the background
// (user switched to another app) is NOT being watched — so finishing/asking
// there should still raise the 🔔 and a system notification. Both the bell
// auto-ack and the notification gate share this single definition.
func windowIsBeingWatched(windowID string) bool {
	return isActiveWindow(windowID) && terminalIsFrontmost()
}

// terminalIsFrontmost reports whether the terminal hosting tmux is the frontmost
// macOS app. Defaults to true (assume watched) when it cannot be determined, so
// detection gaps never silently change the existing behavior.
func terminalIsFrontmost() bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	term := strings.TrimSpace(frontendTerminalBundleID())
	front := frontmostBundleID()
	if term == "" || front == "" {
		return true
	}
	return strings.EqualFold(term, front)
}

// frontmostBundleID returns the bundle id of the frontmost macOS app via
// lsappinfo (no Automation/Accessibility permission needed). Empty on failure.
func frontmostBundleID() string {
	bin, err := exec.LookPath("lsappinfo")
	if err != nil {
		return ""
	}
	asn, err := exec.Command(bin, "front").Output()
	if err != nil || strings.TrimSpace(string(asn)) == "" {
		return ""
	}
	out, err := exec.Command(bin, "info", "-only", "bundleID", strings.TrimSpace(string(asn))).Output()
	if err != nil {
		return ""
	}
	// out form: "CFBundleIdentifier"="com.example.app"
	s := strings.TrimSpace(string(out))
	i := strings.LastIndex(s, "=\"")
	if i < 0 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(s[i+1:]), "\"")
}

func tmuxOutput(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func tmuxDisplay(client, format string) (string, error) {
	cmd := exec.Command("tmux", "display-message", "-p", "-c", client, format)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("display-message %s: %w (%s)", format, err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func listClients() ([]string, error) {
	cmd := exec.Command("tmux", "list-clients", "-F", "#{client_tty}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var clients []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			clients = append(clients, trimmed)
		}
	}
	return clients, nil
}

func socketPath() string {
	return paths.SocketPath()
}

func settingsStorePath() string {
	return paths.SettingsStore()
}

func taskKey(sessionID, windowID, paneID string) string {
	return strings.Join([]string{sessionID, windowID, paneID}, "|")
}

func requireSessionWindow(env ipc.Envelope) (tmuxTarget, error) {
	ctx := normalizeTargetNames(tmuxTarget{
		SessionName: strings.TrimSpace(env.Session),
		SessionID:   strings.TrimSpace(env.SessionID),
		WindowName:  strings.TrimSpace(env.Window),
		WindowID:    strings.TrimSpace(env.WindowID),
		PaneID:      strings.TrimSpace(env.Pane),
	})

	fetchOrder := []string{}
	if ctx.PaneID != "" {
		fetchOrder = append(fetchOrder, ctx.PaneID)
	}
	if ctx.WindowID != "" {
		fetchOrder = append(fetchOrder, ctx.WindowID)
	}
	fetchOrder = append(fetchOrder, "")

	for _, target := range fetchOrder {
		if ctx.complete() {
			break
		}
		info, err := detectTmuxTarget(target)
		if err != nil {
			if target == "" {
				return tmuxTarget{}, err
			}
			continue
		}
		ctx = ctx.merge(info)
	}

	if ctx.SessionID == "" || ctx.WindowID == "" {
		return tmuxTarget{}, fmt.Errorf("session and window required")
	}

	if ctx.SessionName == "" || ctx.WindowName == "" {
		if info, err := detectTmuxTarget(ctx.WindowID); err == nil {
			ctx = ctx.merge(normalizeTargetNames(info))
		}
	}

	if ctx.SessionName == "" {
		ctx.SessionName = ctx.SessionID
	}
	if ctx.WindowName == "" {
		ctx.WindowName = ctx.WindowID
	}
	if strings.TrimSpace(ctx.PaneID) == "" {
		return tmuxTarget{}, fmt.Errorf("pane identifier required")
	}

	return ctx, nil
}

func (t tmuxTarget) complete() bool {
	return t.SessionName != "" && t.SessionID != "" && t.WindowName != "" && t.WindowID != "" && t.PaneID != ""
}

func (t tmuxTarget) merge(other tmuxTarget) tmuxTarget {
	if t.SessionName == "" {
		t.SessionName = other.SessionName
	}
	if t.SessionID == "" {
		t.SessionID = other.SessionID
	}
	if t.WindowName == "" {
		t.WindowName = other.WindowName
	}
	if t.WindowID == "" {
		t.WindowID = other.WindowID
	}
	if t.PaneID == "" {
		t.PaneID = other.PaneID
	}
	if t.WindowIndex == "" {
		t.WindowIndex = other.WindowIndex
	}
	if t.PaneIndex == "" {
		t.PaneIndex = other.PaneIndex
	}
	return t
}

func detectTmuxTarget(target string) (tmuxTarget, error) {
	format := "#{session_name}:::#{session_id}:::#{window_name}:::#{window_id}:::#{pane_id}:::#{window_index}:::#{pane_index}"
	output, err := tmuxQuery(strings.TrimSpace(target), format)
	if err != nil {
		return tmuxTarget{}, err
	}
	parts := strings.Split(strings.TrimSpace(output), ":::")
	if len(parts) != 7 {
		return tmuxTarget{}, fmt.Errorf("unexpected tmux response: %s", strings.TrimSpace(output))
	}
	return tmuxTarget{
		SessionName: strings.TrimSpace(parts[0]),
		SessionID:   strings.TrimSpace(parts[1]),
		WindowName:  strings.TrimSpace(parts[2]),
		WindowID:    strings.TrimSpace(parts[3]),
		PaneID:      strings.TrimSpace(parts[4]),
		WindowIndex: strings.TrimSpace(parts[5]),
		PaneIndex:   strings.TrimSpace(parts[6]),
	}, nil
}

func tmuxNamesForWindow(windowID string) (string, string, error) {
	if strings.TrimSpace(windowID) == "" {
		return "", "", fmt.Errorf("window id required")
	}
	output, err := tmuxQuery(strings.TrimSpace(windowID), "#{session_name}:::#{window_name}")
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(strings.TrimSpace(output), ":::")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("unexpected tmux response: %s", strings.TrimSpace(output))
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func tmuxQuery(target, format string) (string, error) {
	args := []string{"display-message", "-p"}
	if target != "" {
		args = append(args, "-t", target)
	}
	args = append(args, format)
	cmd := exec.Command("tmux", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func stateSummary(tasks []ipc.Task) string {
	inProgress := 0
	waiting := 0
	for _, t := range tasks {
		switch t.Status {
		case statusInProgress:
			inProgress++
		case statusCompleted:
			if !t.Acknowledged {
				waiting++
			}
		}
	}
	return fmt.Sprintf("Active %d · Waiting %d · %s", inProgress, waiting, time.Now().Format(time.Kitchen))
}
