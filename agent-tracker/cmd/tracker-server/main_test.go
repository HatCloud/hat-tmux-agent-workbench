package main

import (
	"testing"
	"time"
)

// 用不存在的 window id，使 isActiveWindow→false → windowIsBeingWatched→false 确定性成立，
// 不依赖测试环境的 tmux active window。
const testWindowID = "@nonexistent-test-515"

func newTestServer() *server {
	return &server{tasks: map[string]*taskRecord{}}
}

func testTarget() tmuxTarget {
	return tmuxTarget{SessionID: "$test", WindowID: testWindowID, PaneID: "%test"}
}

func inProgressTask(s *server, target tmuxTarget) *taskRecord {
	t := &taskRecord{
		SessionID:    target.SessionID,
		WindowID:     target.WindowID,
		Pane:         target.PaneID,
		StartedAt:    time.Now(),
		Status:       statusInProgress,
		Summary:      "working",
		Acknowledged: true, // 与 startTask 实际初始化一致（in_progress 任务默认已 ack）
	}
	s.tasks[taskKey(target.SessionID, target.WindowID, target.PaneID)] = t
	return t
}

// withWindowWatched 临时替换 isWindowWatched 替身，避免测试 shell 出去跑 tmux，
// 使完成通知路径确定性可验证。返回恢复函数。
func withWindowWatched(watched bool) func() {
	prev := isWindowWatched
	isWindowWatched = func(string) bool { return watched }
	return func() { isWindowWatched = prev }
}

func TestGraceElapsed(t *testing.T) {
	now := time.Now()
	if graceElapsed(now, now) {
		t.Fatalf("刚置 pending、零间隔不应视为已过宽限")
	}
	if graceElapsed(now.Add(-completionGraceWindow/2), now) {
		t.Fatalf("不足宽限不应视为已过")
	}
	if !graceElapsed(now.Add(-completionGraceWindow), now) {
		t.Fatalf("恰好等于宽限边界应视为已过（实现为 now >= pendingAt+grace）")
	}
	if !graceElapsed(now.Add(-completionGraceWindow-time.Second), now) {
		t.Fatalf("超过宽限应视为已过")
	}
}

// startTask 重置任务为 in_progress 时应清除残留 PendingCompleteAt
// （pending 期间会话重新 busy → 作废上轮瞬态 idle 的待发完成）。
func TestStartTaskClearsPending(t *testing.T) {
	s := newTestServer()
	target := testTarget()
	task := inProgressTask(s, target)
	task.Status = statusCompleted // 模拟非 in_progress、留有残留 pending
	now := time.Now()
	task.PendingCompleteAt = &now

	if err := s.startTask(target, "working"); err != nil {
		t.Fatalf("startTask 出错: %v", err)
	}
	if task.Status != statusInProgress {
		t.Fatalf("startTask 后应 in_progress，实为 %q", task.Status)
	}
	if task.PendingCompleteAt != nil {
		t.Fatalf("startTask 应清除残留 PendingCompleteAt")
	}
}

// 首次 idle：开始宽限，不通知、不改 Status。
func TestFinishTaskGraceDebounce(t *testing.T) {
	s := newTestServer()
	target := testTarget()
	task := inProgressTask(s, target)

	notify, err := s.finishTask(target, "")
	if err != nil {
		t.Fatalf("finishTask 出错: %v", err)
	}
	if notify {
		t.Fatalf("首次 idle 不应触发通知")
	}
	if task.Status != statusInProgress {
		t.Fatalf("首次 idle 后 Status 应仍为 in_progress，实为 %q", task.Status)
	}
	if task.PendingCompleteAt == nil {
		t.Fatalf("首次 idle 应置 PendingCompleteAt")
	}
}

// 持续 idle 满宽限：提交 completed 并通知（测试环境 windowIsBeingWatched=false）。
func TestFinishTaskCommitsAfterGrace(t *testing.T) {
	defer withWindowWatched(false)() // 未被观看 → 应通知；不依赖 tmux 外部环境
	s := newTestServer()
	target := testTarget()
	task := inProgressTask(s, target)

	past := time.Now().Add(-completionGraceWindow - time.Second)
	task.PendingCompleteAt = &past

	notify, err := s.finishTask(target, "")
	if err != nil {
		t.Fatalf("finishTask 出错: %v", err)
	}
	if task.Status != statusCompleted {
		t.Fatalf("满宽限后应 completed，实为 %q", task.Status)
	}
	if task.PendingCompleteAt != nil {
		t.Fatalf("提交完成后应清 PendingCompleteAt")
	}
	if !notify {
		t.Fatalf("满宽限提交且未被观看应通知")
	}
}

// 被观看（terminal 前台 + window 选中）时提交完成不应通知（自动 ack）。
func TestFinishTaskNoNotifyWhenWatched(t *testing.T) {
	defer withWindowWatched(true)()
	s := newTestServer()
	target := testTarget()
	task := inProgressTask(s, target)
	past := time.Now().Add(-completionGraceWindow - time.Second)
	task.PendingCompleteAt = &past

	notify, _ := s.finishTask(target, "")
	if notify {
		t.Fatalf("被观看时提交完成不应通知")
	}
	if !task.Acknowledged {
		t.Fatalf("被观看时完成应自动 ack")
	}
}

// 活动信号（mark_asking false，shell 路径）清 pending，且不被 Asking==asking early-return 跳过；
// 随后再 idle 重新进入宽限而非直接完成。
func TestMarkAskingClearsPendingThenRegrace(t *testing.T) {
	s := newTestServer()
	target := testTarget()
	task := inProgressTask(s, target)
	now := time.Now()
	task.Asking = false
	task.PendingCompleteAt = &now

	s.markTaskAsking(target, false) // Asking 未变（仍 false），但必须清 pending
	if task.PendingCompleteAt != nil {
		t.Fatalf("markTaskAsking 应清 PendingCompleteAt（不得被 Asking==asking early-return 跳过）")
	}

	notify, _ := s.finishTask(target, "")
	if notify || task.Status != statusInProgress || task.PendingCompleteAt == nil {
		t.Fatalf("清 pending 后再 idle 应重新进入宽限：notify=%v status=%q pending=%v", notify, task.Status, task.PendingCompleteAt)
	}
}

func TestMarkTaskAttentionErrorKeepsTaskInProgress(t *testing.T) {
	s := newTestServer()
	target := testTarget()
	task := inProgressTask(s, target)
	now := time.Now()
	task.PendingCompleteAt = &now

	if changed := s.markTaskAttention(target, "error"); !changed {
		t.Fatal("entering error attention should be a state change")
	}
	if task.Status != statusInProgress {
		t.Fatalf("error attention status = %q, want in_progress", task.Status)
	}
	if !task.Asking || task.Attention != "error" || task.Acknowledged {
		t.Fatalf("error attention task = %+v", task)
	}
	if task.PendingCompleteAt != nil {
		t.Fatal("error attention must cancel pending completion")
	}
	if changed := s.markTaskAttention(target, "error"); changed {
		t.Fatal("repeated error attention must not notify again")
	}
	task.Acknowledged = true
	if changed := s.markTaskAttention(target, "asking"); !changed {
		t.Fatal("error to asking must be a fresh attention transition")
	}
	if task.Attention != "asking" || task.Acknowledged {
		t.Fatalf("asking transition task = %+v", task)
	}
	if changed := s.markTaskAttention(target, ""); !changed || task.Asking || task.Attention != "" {
		t.Fatalf("clear attention changed=%v task=%+v", changed, task)
	}
}

func TestAttentionNotificationMessage(t *testing.T) {
	if got := attentionNotificationMessage("error"); got != "⚠️ Codex 执行出错，请查看窗口" {
		t.Fatalf("error notification = %q", got)
	}
	if got := attentionNotificationMessage("asking"); got != "❓ 有问题需要你回答" {
		t.Fatalf("asking notification = %q", got)
	}
}

func TestStripNotificationStatusPrefixError(t *testing.T) {
	if got := stripNotificationStatusPrefix("[E] project/title"); got != "project/title" {
		t.Fatalf("strip error prefix = %q", got)
	}
}

// 无 in_progress 任务（含已 completed）的 idle 应 no-op，不创建/不改 completed 任务。
func TestFinishTaskNoopWhenNoInProgress(t *testing.T) {
	s := newTestServer()
	target := testTarget()

	notify, err := s.finishTask(target, "")
	if err != nil {
		t.Fatalf("finishTask 出错: %v", err)
	}
	if notify {
		t.Fatalf("无任务时不应通知")
	}
	if _, ok := s.tasks[taskKey(target.SessionID, target.WindowID, target.PaneID)]; ok {
		t.Fatalf("无 in_progress 任务时 finishTask 不应凭空创建任务")
	}

	// 已 completed 的任务再收到 idle：幂等 no-op，不重复通知。
	done := inProgressTask(s, target)
	done.Status = statusCompleted
	notify, _ = s.finishTask(target, "")
	if notify {
		t.Fatalf("已 completed 任务不应再次通知")
	}
}
