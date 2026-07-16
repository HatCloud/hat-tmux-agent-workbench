package main

import (
	"strings"
	"time"
)

// Orphan-task sweep: tasks live only in memory, and the daemon deletes them only
// on an explicit delete_task command. A window closed while its task was
// in_progress/asking leaves a permanent ghost record. That inflates the Active
// count, and — worse — poisons remote-bell aggregation on machines ssh'd into
// this one: aggregateRemoteState folds in every cached task, so one ghost [B]
// keeps the remote ssh window's status prefix busy forever. This sweep
// reconciles the task map against the live tmux window list and drops records
// whose window no longer exists.

const orphanSweepInterval = 15 * time.Second

func (s *server) sweepOrphanTasksLoop() {
	ticker := time.NewTicker(orphanSweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.sweepOrphanTasks()
	}
}

func (s *server) sweepOrphanTasks() {
	out, err := tmuxOutput("list-windows", "-a", "-F", "#{window_id}")
	if err != nil {
		// tmux unreachable (server restarting, call timed out): keep everything —
		// wiping on a transient failure would drop live tasks.
		return
	}
	live := make(map[string]bool)
	for _, id := range strings.Fields(out) {
		live[id] = true
	}
	if s.dropOrphanTasks(live) {
		// broadcastState also sweeps now-stale per-window notifications
		// (reconcileNotifications), keeping 🔔 and notification in lock-step.
		s.broadcastStateAsync()
		s.statusRefreshAsync()
	}
}

// dropOrphanTasks removes tasks whose window is not in the live set, reporting
// whether anything was removed. An empty live set is treated as untrustworthy
// (a tmux server with any session always has at least one window) and skipped.
func (s *server) dropOrphanTasks(live map[string]bool) bool {
	if len(live) == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := false
	for key, t := range s.tasks {
		if !live[t.WindowID] {
			delete(s.tasks, key)
			removed = true
		}
	}
	return removed
}
