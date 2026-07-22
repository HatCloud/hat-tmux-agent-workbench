package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/david/agent-tracker/internal/agentclient"
	"github.com/david/agent-tracker/internal/ipc"
)

// sync-names 轮询主循环与 daemon 对账：flock 单飞、periodic 限流、
// reconcileActions 状态机。从 claude_session.go 拆出。

const syncNamesMaxRun = 20 * time.Second

func syncNamesLockPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("agent-sync-names-%d.lock", os.Getuid()))
}

func syncNamesLastStartPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("agent-sync-names-%d.last", os.Getuid()))
}

// acquireSyncNamesLock uses a kernel flock instead of a mkdir/PID lock. The
// kernel releases it automatically if the process exits or is killed, so a crash
// cannot leave sync-names permanently disabled.
//
// blocking=false (periodic/nav triggers): an overlapping trigger is dropped
// rather than queued, bounding the worker count at one — the in-flight pass
// already covers it. blocking=true (event-driven --wait): the caller WAITS for
// the in-flight pass and then runs its own, so a status transition that lands
// mid-pass (which the in-flight pass read before the change) is still detected
// promptly instead of waiting for the next periodic tick.
func acquireSyncNamesLock(path string, blocking bool) (release func(), acquired bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	how := syscall.LOCK_EX
	if !blocking {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		_ = f.Close()
		if !blocking && (err == syscall.EWOULDBLOCK || err == syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		})
	}, true, nil
}

func syncNamesPeriodicDue(path string, now time.Time, interval time.Duration) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	last, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return true
	}
	return !now.Before(time.Unix(0, last).Add(interval))
}

func markSyncNamesStarted(path string, now time.Time) error {
	return os.WriteFile(path, []byte(strconv.FormatInt(now.UnixNano(), 10)), 0o600)
}

func hasSyncArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// runTmuxSyncNames re-syncs every window's name from its AI pane's live agent
// session (or launcher @agent_client). The status bar invokes --periodic on its
// refresh cadence, which is rate-limited by the configured poll interval;
// navigation hooks remain immediate.
// All callers share a non-blocking kernel lock, so slow passes are coalesced and
// can never accumulate into a process storm.
func runTmuxSyncNames(args []string) error {
	periodic := hasSyncArg(args, "--periodic")
	// Event-driven callers pass --wait: block for any in-flight pass so a
	// transition landing mid-pass is still caught this pass, not next tick.
	wait := hasSyncArg(args, "--wait")
	release, acquired, err := acquireSyncNamesLock(syncNamesLockPath(), wait)
	if err != nil || !acquired {
		return nil
	}
	defer release()

	now := time.Now()
	// The status bar triggers --periodic every second; the configured poll
	// interval (default 3s) rate-limits how often a full sync pass actually runs,
	// so it is the primary cadence driving window naming + task/state refresh.
	// Navigation hooks (non-periodic) stay immediate.
	if periodic && !syncNamesPeriodicDue(syncNamesLastStartPath(), now, pollIntervalDuration(loadAppConfig())) {
		return nil
	}
	_ = markSyncNamesStarted(syncNamesLastStartPath(), now)
	deadline := now.Add(syncNamesMaxRun)

	// One batched option read per window for this pass (see window_opts.go).
	beginWindowOptMemo()
	defer endWindowOptMemo()

	out, err := runTmuxOutput("list-windows", "-a", "-F", "#{session_id}::#{window_id}")
	if err != nil {
		return nil
	}
	// One process Index for all adapters this pass (one ps; per-adapter sidecars
	// — Claude sessions dir, Codex lsof batch — load lazily and memoize on it).
	acIdx := agentclient.BuildIndex()
	// Publish the busy-shell allowlist into the Index SideCar so the claude
	// adapter can upgrade shell/idle→busy for allowlisted background tasks.
	injectBusyShellPatterns(acIdx, loadAppConfig())
	// Daemon task status per pane, to drive the 🔔 (completed-unread) icon from
	// the live agent busy/idle status.
	taskByPane := map[string]string{}
	if st, err := trackerLoadState(""); err == nil && st != nil {
		for _, tk := range st.Tasks {
			taskByPane[tk.Pane] = tk.Status
		}
	}
	checkAndFireTimers(acIdx)

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if time.Now().After(deadline) {
			break
		}
		parts := strings.SplitN(strings.TrimSpace(line), "::", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		sessionID, windowID := parts[0], parts[1]
		aiPane := agentAIPane(windowID, acIdx)
		// One registry Detect per window, shared by naming and reconcile below.
		var live *agentclient.LiveSession
		if aiPane != "" {
			tag := tmuxWindowOption(windowID, "@agent_client")
			if l, ok := agentclient.DefaultRegistry().DetectForPane(acIdx, panePID(aiPane), tag); ok {
				live = &l
			}
		}
		if live != nil {
			maybeStartAutoName(windowID, aiPane, live)
		}
		if name, nativeSessionNameWins := agentWindowName(windowID, sessionID, aiPane, live); name != "" {
			autoRenameWindowPriority(windowID, name, nativeSessionNameWins)
		} else if strings.TrimSpace(tmuxWindowOption(windowID, "@agent_window_name_auto")) != "" {
			// A window we previously auto-named no longer qualifies (e.g. an ssh
			// session exited, clearing the 🌐 marker). Hand it back to tmux
			// automatic naming; autoRenameWindow's manual-override guard keeps any
			// user rename intact.
			autoRenameWindow(windowID, "")
		}
		// Hide the outer pane-border title on ssh windows so it doesn't overlap the
		// nested remote tmux status line; restored when ssh exits.
		reconcileSSHPaneBorder(windowID)
		// Persist the ssh destination host so the daemon's remote-bell poller knows
		// which windows mirror a remote machine and where to read its tracker state.
		reconcileSSHHost(windowID)
		if aiPane != "" {
			// Reflow ai/git[/run] only when auto-resize is enabled and orientation changes.
			// Yield while a reflow-focus debounce is in flight, so the periodic sync
			// doesn't reflow a mid-resize layout the debounced winner will redo.
			if !reflowDebouncePending(windowID) {
				reconcileWindowOrientation(windowID)
			}
			// Backfill @agent_dir for windows created before this feature, and
			// actively migrate windows whose stored path points at a worktree
			// (basename differs from the main repo's). Compare-and-set keeps the
			// rewrite idempotent for steady state.
			if out, err := runTmuxOutput("display-message", "-p", "-t", aiPane, "#{pane_current_path}"); err == nil {
				if panePath := strings.TrimSpace(out); panePath != "" {
					target := panePath
					if main := mainRepoPath(panePath); main != "" {
						target = main
					}
					if dir := abbrevPath(target); dir != "" &&
						tmuxWindowOption(windowID, "@agent_dir") != dir {
						setWindowOption(windowID, "@agent_dir", dir)
					}
				}
			}
			if live != nil {
				// Persist the adapter title as the transient-detection/pending-window
				// fallback used by agentWindowName. Live Window Nav rows consume the
				// centrally resolved @agent_resolved_display_title written above.
				// PersistTitle is adapter-gated: Claude only exposes a user-set name
				// here (the auto ai-title must not overwrite a typed `prefix ]`
				// title), Codex/Grok persist their auto titles.
				if t := agentTitleForWindow(live.PersistTitle); t != "" {
					setWindowOption(windowID, "@agent_title", t)
				}
				// Reuse the limited probe agentWindowName stamped above so the
				// daemon sees "limited" (asking-like) instead of idle→completed.
				status := live.Status
				if _, limited := windowQuotaLimitedUntil(windowID); limited &&
					!strings.EqualFold(status, "busy") {
					status = "limited"
				}
				// unknown must not finish_task (design: no false completion 🔔)
				if status != agentclient.StatusUnknown && status != "" {
					reconcileTaskStatus(sessionID, windowID, aiPane, agentTitleForWindow(live.Title), status, taskByPane[aiPane])
				}
				// Auto-retry a turn that stopped on a recoverable API error, using
				// the @agent_error_* stamp agentWindowName wrote this pass.
				reconcileErrorRetry(windowID, aiPane, live)
			}
		}
	}
	return nil
}

// reconcileAction 是 reconcileActions 决定要发给 daemon 的单条命令。抽成纯数据让
// 状态→命令的映射可单测，reconcileTask 只负责按它拼 Envelope 并发送。
type reconcileAction struct {
	command   string
	asking    bool // 仅 command=="mark_asking" 有意义
	attention string
}

// reconcileActions 把（已规整的）Claude 会话 status + daemon 当前任务状态映射成要发送
// 的命令序列（纯函数、可单测）。语义：
//   - busy：未在跑→start_task；在跑→mark_asking{false}（从 asking 回来时清标志）。
//   - asking/waiting/paused/limited/error：未在跑先 start_task，再 mark_asking{true}（保序）。
//     attention 区分 asking/limited/error，状态切换时可重新提醒；它们都保持任务 in_progress。
//   - idle：在跑才 finish_task（交由 daemon 宽限去抖判定是否真完成）；否则 no-op。
//   - shell：Claude 结束 turn 但有后台任务/subagent 在跑的活动态。在跑时发
//     mark_asking{false}——既清 asking、又（在 daemon 侧）作废 turn 边界瞬态 idle 留下的
//     待发完成；非在跑则 no-op，不凭空造任务（Claude 在等待而非主动工作）。
//   - 其它未知 status：no-op。
func reconcileActions(metaStatus, daemonStatus string) []reconcileAction {
	inProgress := daemonStatus == "in_progress"
	switch metaStatus {
	case "busy":
		if !inProgress {
			return []reconcileAction{{command: "start_task"}}
		}
		return []reconcileAction{{command: "mark_asking", asking: false}}
	case "asking", "waiting", "paused":
		if !inProgress {
			return []reconcileAction{{command: "start_task"}, {command: "mark_asking", asking: true, attention: "asking"}}
		}
		return []reconcileAction{{command: "mark_asking", asking: true, attention: "asking"}}
	case "limited", "error":
		if !inProgress {
			return []reconcileAction{{command: "start_task"}, {command: "mark_asking", asking: true, attention: metaStatus}}
		}
		return []reconcileAction{{command: "mark_asking", asking: true, attention: metaStatus}}
	case "idle", "shell":
		// "shell" = turn ended, background job still running. The agent accepts
		// input, and nothing in the session file distinguishes a productive
		// background job from a parked long-runner (dev server/watch), so it is
		// deliberately treated as idle: the window shows [I] and the completion
		// 🔔 fires — the user can always tell the turn has stopped. The 2s
		// completion grace still absorbs fast background blips.
		if inProgress {
			return []reconcileAction{{command: "finish_task"}}
		}
		return nil
	default:
		return nil
	}
}

// reconcileTaskStatus drives the daemon's task state from the live agent
// status so the status bar's 🔔 (completed-unread) reflects "finished while you
// were away". busy → ensure a task exists (in_progress); busy→idle → finish it
// (completed → 🔔 until focus acknowledges, with a grace debounce in the daemon).
func reconcileTaskStatus(sessionID, windowID, pane, title, status, daemonStatus string) {
	for _, act := range reconcileActions(strings.ToLower(strings.TrimSpace(status)), daemonStatus) {
		env := &ipc.Envelope{SessionID: sessionID, WindowID: windowID, Pane: pane}
		switch act.command {
		case "start_task":
			summary := title
			if summary == "" {
				summary = "working"
			}
			env.Summary = summary
		case "mark_asking":
			env.Asking = act.asking
			env.Attention = act.attention
		}
		_ = sendTrackerCommand(act.command, env)
	}
}
