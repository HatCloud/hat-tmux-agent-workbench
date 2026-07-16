package main

import (
	"testing"
	"time"
)

func addTaskForWindow(s *server, windowID, status string) *taskRecord {
	target := tmuxTarget{SessionID: "$1", WindowID: windowID, PaneID: "%" + windowID}
	task := &taskRecord{
		SessionID: target.SessionID,
		WindowID:  target.WindowID,
		Pane:      target.PaneID,
		Summary:   "task-" + windowID,
		Status:    status,
		StartedAt: time.Now(),
	}
	s.tasks[taskKey(target.SessionID, target.WindowID, target.PaneID)] = task
	return task
}

// 窗口已消失的任务应被清除；存活窗口的任务保留。
func TestDropOrphanTasksRemovesClosedWindows(t *testing.T) {
	s := newTestServer()
	addTaskForWindow(s, "@1", statusInProgress)
	addTaskForWindow(s, "@2", statusCompleted)
	ghost := addTaskForWindow(s, "@9", statusInProgress)
	_ = ghost

	removed := s.dropOrphanTasks(map[string]bool{"@1": true, "@2": true})
	if !removed {
		t.Fatalf("存在孤儿任务时应报告 removed=true")
	}
	if len(s.tasks) != 2 {
		t.Fatalf("应剩 2 条任务，实为 %d", len(s.tasks))
	}
	for _, task := range s.tasks {
		if task.WindowID == "@9" {
			t.Fatalf("@9 的孤儿任务应被清除")
		}
	}
}

// 全部窗口存活时不删任何任务、报告 removed=false。
func TestDropOrphanTasksNoopWhenAllLive(t *testing.T) {
	s := newTestServer()
	addTaskForWindow(s, "@1", statusInProgress)

	if s.dropOrphanTasks(map[string]bool{"@1": true}) {
		t.Fatalf("窗口全部存活时不应报告 removed")
	}
	if len(s.tasks) != 1 {
		t.Fatalf("任务不应被删除")
	}
}

// 空存活集（tmux 瞬态不可达/无输出）必须视为不可信，跳过清理。
func TestDropOrphanTasksSkipsEmptyLiveSet(t *testing.T) {
	s := newTestServer()
	addTaskForWindow(s, "@1", statusInProgress)

	if s.dropOrphanTasks(map[string]bool{}) {
		t.Fatalf("空存活集不应删除任何任务")
	}
	if len(s.tasks) != 1 {
		t.Fatalf("空存活集下任务应原样保留")
	}
}

// icon 判定：任一 bell 输入为真即 🔔，否则空。
func TestDesiredWindowIcon(t *testing.T) {
	if desiredWindowIcon(false, false) != "" {
		t.Fatalf("无 bell 应为空")
	}
	if desiredWindowIcon(true, false) != "🔔 " {
		t.Fatalf("本地 bell 应出 🔔")
	}
	if desiredWindowIcon(false, true) != "🔔 " {
		t.Fatalf("远端 bell 应出 🔔")
	}
}
