package main

import (
	"sync"
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

// ---- B1: 锁外探测 + TOCTOU 复检回归测试 ----

// blockingWindowWatched 用一个可控 channel 阻塞 isWindowWatched，模拟锁外探测
// 期间的慢调用。started 在探测进入时关闭（通知并发 goroutine 可以动手了），
// release 由测试关闭以放行探测返回 watched。返回恢复函数。
func blockingWindowWatched(watched bool, started chan<- struct{}, release <-chan struct{}) func() {
	prev := isWindowWatched
	var once sync.Once
	isWindowWatched = func(string) bool {
		once.Do(func() { close(started) })
		<-release
		return watched
	}
	return func() { isWindowWatched = prev }
}

// committedGraceTarget 建一个已过宽限、即将在下次 finishTask 提交 completed 的 in_progress 任务。
func committedGraceTarget(s *server) tmuxTarget {
	target := testTarget()
	task := inProgressTask(s, target)
	past := time.Now().Add(-completionGraceWindow - time.Second)
	task.PendingCompleteAt = &past
	return target
}

// ① finishTask 在锁外探测期间不得持有 s.mu——否则 tmux 变慢会冻结整个 daemon。
func TestFinishTaskDoesNotHoldLockDuringWatch(t *testing.T) {
	s := newTestServer()
	target := committedGraceTarget(s)
	started := make(chan struct{})
	release := make(chan struct{})
	defer blockingWindowWatched(false, started, release)()

	go s.finishTask(target, "")
	<-started // 探测已进入（finishTask 应已解锁）

	got := make(chan struct{})
	go func() {
		s.mu.Lock()
		s.mu.Unlock()
		close(got)
	}()
	select {
	case <-got: // 拿到锁 → finishTask 没持锁探测 ✓
	case <-time.After(2 * time.Second): // 宽裕窗口——<-started 已确定性等到探测进入（解锁之后），
		// 拿锁 goroutine 应几乎立即成功；放宽仅为吸收繁忙 CI 上的调度抖动（code review 提示）。
		close(release)
		t.Fatal("finishTask 在锁外探测期间仍持有 s.mu：其它操作被冻结")
	}
	close(release)
}

// ② 探测期间 startTask 复位同 key → finishTask 复检 Status 应放弃写回。
func TestFinishTaskTOCTOURecheckStartTask(t *testing.T) {
	s := newTestServer()
	target := committedGraceTarget(s)
	started := make(chan struct{})
	release := make(chan struct{})
	defer blockingWindowWatched(false, started, release)()

	var notify bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); notify, _ = s.finishTask(target, "") }()
	<-started
	s.startTask(target, "restarted") // 复位为 in_progress
	close(release)
	wg.Wait()

	key := taskKey(target.SessionID, target.WindowID, target.PaneID)
	if notify {
		t.Fatal("startTask 复位后 finishTask 不应通知")
	}
	if s.tasks[key].Status != statusInProgress {
		t.Fatalf("startTask 复位后应 in_progress，实为 %q", s.tasks[key].Status)
	}
}

// ③ 探测期间 acknowledgeTask 标已读 → finishTask 复检 !Acknowledged 应放弃写回，不重弹通知。
func TestFinishTaskTOCTOURecheckAcknowledge(t *testing.T) {
	s := newTestServer()
	target := committedGraceTarget(s)
	started := make(chan struct{})
	release := make(chan struct{})
	defer blockingWindowWatched(false, started, release)() // watched=false：若无复检会把 ack 改回未读

	var notify bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); notify, _ = s.finishTask(target, "") }()
	<-started
	s.acknowledgeTask(target.SessionID, target.WindowID, target.PaneID) // 用户在探测期间点了窗口，消 🔔
	close(release)
	wg.Wait()

	key := taskKey(target.SessionID, target.WindowID, target.PaneID)
	if notify {
		t.Fatal("并发 acknowledge 后 finishTask 不应返回需通知")
	}
	if !s.tasks[key].Acknowledged {
		t.Fatal("并发 acknowledge 后 finishTask 不应把任务改回未读")
	}
}

// ④ R4 初值陷阱回归：无并发、未观看，任务从默认 Acknowledged:true 进入完成，仍应通知。
func TestFinishTaskNormalUnwatchedStillNotifies(t *testing.T) {
	defer withWindowWatched(false)() // 未观看
	s := newTestServer()
	target := testTarget()
	task := inProgressTask(s, target) // Acknowledged:true（默认）
	past := time.Now().Add(-completionGraceWindow - time.Second)
	task.PendingCompleteAt = &past

	notify, _ := s.finishTask(target, "")
	if !notify {
		t.Fatal("正常未观看完成应通知（不得被默认 Acknowledged:true 吞掉）")
	}
	if task.Acknowledged {
		t.Fatal("未观看完成的 Acknowledged 应落为 false")
	}
}

// ---- B1: exec 超时包装 ----

func TestRunTmuxOutputCtxTimeout(t *testing.T) {
	start := time.Now()
	_, err := runCommandOutputCtx(1*time.Second, "sleep", "5")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("超时命令应返回错误")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("超时应在 ~1s 触发，实际耗时 %v（未加 context 超时）", elapsed)
	}
}
