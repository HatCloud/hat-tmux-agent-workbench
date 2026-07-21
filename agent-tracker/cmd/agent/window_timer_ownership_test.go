package main

import (
	"testing"
	"time"
)

func timerFor(window string) *windowTimer {
	return &windowTimer{
		ID:          newTimerID(),
		WindowID:    window,
		Content:     "c",
		TriggerMode: windowTimerTriggerDelay,
		Enabled:     true,
		NextFireAt:  time.Now().Add(time.Hour),
	}
}

// tmux server 换代（pid 变化）＝窗口 id 空间重置：旧 server 的定时器必须整体作废，
// 否则新窗口复用旧 @N 会继承（并被注入）别人的定时器——这是「新建窗口看到旧窗口
// 定时器」串扰的根因。
func TestReconcileTimerOwnershipWipesOnServerChange(t *testing.T) {
	store := &windowTimerStore{
		ServerPID: 111,
		Timers:    []*windowTimer{timerFor("@1"), timerFor("@2")},
	}
	changed := reconcileTimerOwnership(store, 222, map[string]bool{"@1": true, "@2": true})
	if !changed {
		t.Fatalf("server pid 变化应报告 changed")
	}
	if len(store.Timers) != 0 {
		t.Fatalf("旧 server 的定时器应全部作废，实剩 %d", len(store.Timers))
	}
	if store.ServerPID != 222 {
		t.Fatalf("应更新 ServerPID 到 222，实为 %d", store.ServerPID)
	}
}

// 旧格式（ServerPID==0）迁移：无法判断归属，只补 stamp、不清定时器。
func TestReconcileTimerOwnershipMigratesLegacyStore(t *testing.T) {
	store := &windowTimerStore{Timers: []*windowTimer{timerFor("@1")}}
	changed := reconcileTimerOwnership(store, 222, map[string]bool{"@1": true})
	if !changed {
		t.Fatalf("补 stamp 应报告 changed")
	}
	if len(store.Timers) != 1 || store.ServerPID != 222 {
		t.Fatalf("旧格式只 stamp 不清：timers=%d pid=%d", len(store.Timers), store.ServerPID)
	}
}

// 同代 server 内：窗口已关闭的定时器清除，存活窗口的保留。
func TestReconcileTimerOwnershipDropsClosedWindows(t *testing.T) {
	store := &windowTimerStore{
		ServerPID: 111,
		Timers:    []*windowTimer{timerFor("@1"), timerFor("@9")},
	}
	changed := reconcileTimerOwnership(store, 111, map[string]bool{"@1": true})
	if !changed {
		t.Fatalf("存在孤儿定时器应报告 changed")
	}
	if len(store.Timers) != 1 || store.Timers[0].WindowID != "@1" {
		t.Fatalf("应只剩 @1 的定时器")
	}
}

// 空存活集（tmux 瞬态不可达）不可信：不清、不 stamp 变更之外的内容。
func TestReconcileTimerOwnershipSkipsEmptyLiveSet(t *testing.T) {
	store := &windowTimerStore{
		ServerPID: 111,
		Timers:    []*windowTimer{timerFor("@1")},
	}
	if reconcileTimerOwnership(store, 111, map[string]bool{}) {
		t.Fatalf("空存活集同代下不应有变更")
	}
	if len(store.Timers) != 1 {
		t.Fatalf("空存活集不应清定时器")
	}
}

// pid 未知（tmux 查询失败，传 0）：不 wipe、不 stamp，只做（有存活集时的）孤儿清理。
func TestReconcileTimerOwnershipUnknownPID(t *testing.T) {
	store := &windowTimerStore{
		ServerPID: 111,
		Timers:    []*windowTimer{timerFor("@1"), timerFor("@9")},
	}
	changed := reconcileTimerOwnership(store, 0, map[string]bool{"@1": true})
	if !changed || len(store.Timers) != 1 {
		t.Fatalf("pid 未知仍应做孤儿清理")
	}
	if store.ServerPID != 111 {
		t.Fatalf("pid 未知不应改写 ServerPID")
	}
}

// interval loop 补发风暴：睡眠/停机吞掉多个周期后，下一次触发必须重锚到
// now+interval，而不是留在过去导致每 tick 连环补发。
func TestComputeNextLoopIntervalReanchorsAfterDowntime(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, loc)
	timer := &windowTimer{
		LoopMode:        windowTimerLoopInterval,
		LoopIntervalSec: 300, // 5m
	}
	// 正常情形：prevFire 刚过，next = prevFire+5m（仍在未来）
	prev := now.Add(-time.Minute)
	if got, want := computeNextLoopFireAtFrom(timer, prev, now, loc), prev.Add(5*time.Minute); !got.Equal(want) {
		t.Fatalf("正常情形 next=%v, want %v", got, want)
	}
	// 停机 3 小时：prevFire+5m 已在过去 → 重锚 now+5m
	prev = now.Add(-3 * time.Hour)
	if got, want := computeNextLoopFireAtFrom(timer, prev, now, loc), now.Add(5*time.Minute); !got.Equal(want) {
		t.Fatalf("停机后 next=%v, want %v（不应留在过去）", got, want)
	}
}

