package agentclient

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Index is a one-shot process-tree snapshot shared by all adapters in one sync pass.
type Index struct {
	Children map[int][]int  // ppid -> child pids
	Commands map[int]string // pid -> command line
	// SideCar holds per-adapter opaque caches (sessions maps, rollouts, …).
	// Adapters may type-assert their own keys; consumers must not.
	SideCar map[string]any
}

// BuildIndex runs a single `ps` snapshot. Adapters load their own sidecars after.
func BuildIndex() *Index {
	idx := &Index{
		Children: map[int][]int{},
		Commands: map[int]string{},
		SideCar:  map[string]any{},
	}
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,command=").CombinedOutput()
	if err != nil {
		return idx
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil {
			continue
		}
		idx.Children[ppid] = append(idx.Children[ppid], pid)
		if len(f) > 2 {
			idx.Commands[pid] = strings.Join(f[2:], " ")
		}
	}
	return idx
}

// WalkSubtree yields panePID and all descendants (BFS).
func (idx *Index) WalkSubtree(panePID int) []int {
	if idx == nil || panePID <= 0 {
		return nil
	}
	var out []int
	seen := map[int]bool{}
	stack := []int{panePID}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
		for _, c := range idx.Children[n] {
			if !seen[c] {
				stack = append(stack, c)
			}
		}
	}
	return out
}

// CommandFor returns the command line for pid, or empty.
func (idx *Index) CommandFor(pid int) string {
	if idx == nil {
		return ""
	}
	return idx.Commands[pid]
}

// Memo caches an adapter sidecar for the lifetime of this Index (one sync
// pass), so per-pass loads (session dirs, lsof batches) run once, not once per
// window. Not goroutine-safe: a sync pass is single-threaded by design.
func (idx *Index) Memo(key string, load func() any) any {
	if idx == nil {
		return load()
	}
	if idx.SideCar == nil {
		idx.SideCar = map[string]any{}
	}
	if v, ok := idx.SideCar[key]; ok {
		return v
	}
	v := load()
	idx.SideCar[key] = v
	return v
}

// RunOutput executes a command with a deadline and returns stdout. Adapters use
// it for sqlite3/lsof/ps probes so a hung subprocess can't stall a sync pass.
func RunOutput(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

// BuildIndexTimeout is available for callers that need a deadline wrapper later.
const BuildIndexTimeout = 3 * time.Second
