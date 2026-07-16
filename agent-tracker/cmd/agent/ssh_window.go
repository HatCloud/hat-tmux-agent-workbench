package main

import (
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ssh 窗检测与远程透传抓手：pane 进程树找 ssh、destination 解析、
// @agent_ssh_host / pane-border 对账。从 claude_session.go 拆出。

// sshFlagsWithArg is the set of single-letter ssh options that consume the
// following token as their argument. Used by parseSSHHost to skip option
// arguments when locating the destination host.
var sshFlagsWithArg = map[byte]bool{
	'b': true, 'c': true, 'D': true, 'E': true, 'e': true, 'F': true,
	'I': true, 'i': true, 'J': true, 'L': true, 'l': true, 'm': true,
	'O': true, 'o': true, 'p': true, 'Q': true, 'R': true, 'S': true,
	'W': true, 'w': true,
}

// parseSSHHost extracts the destination host from an ssh command line. args is
// the full `ps -o args=` string, including the leading program name (e.g.
// "ssh user@host -p 2222"). It is flag-aware: options in sshFlagsWithArg consume
// the next token, "--" terminates option parsing, and the destination is the
// first remaining non-option token. The leading user@ and a trailing :port are
// stripped (bracketed or multi-colon IPv6 addresses are preserved). Returns ""
// when there is no destination, so callers never clear a window name on garbage.
func parseSSHHost(args string) string {
	fields := strings.Fields(args)
	if len(fields) < 2 {
		return ""
	}
	// fields[0] is the program name (ssh); start parsing at the first argument.
	i := 1
	for i < len(fields) {
		tok := fields[i]
		if tok == "--" {
			i++
			break
		}
		if len(tok) >= 2 && tok[0] == '-' {
			// Single-letter option; if it takes an argument, skip that too.
			if len(tok) == 2 && sshFlagsWithArg[tok[1]] {
				i += 2
				continue
			}
			i++
			continue
		}
		// First non-option token is the destination.
		return sshHostFromDestination(tok)
	}
	if i < len(fields) {
		return sshHostFromDestination(fields[i])
	}
	return ""
}

// sshHostFromDestination strips a leading user@ and a trailing :port from an ssh
// destination token. Bracketed IPv6 ([::1]:22 → ::1) is unwrapped; bare IPv6
// (multiple colons, unbracketed) is left untouched to avoid eating an address
// segment as a port.
func sshHostFromDestination(dest string) string {
	if at := strings.LastIndex(dest, "@"); at >= 0 {
		dest = dest[at+1:]
	}
	if strings.HasPrefix(dest, "[") {
		if end := strings.Index(dest, "]"); end >= 0 {
			return dest[1:end]
		}
	}
	if strings.Count(dest, ":") == 1 {
		if i := strings.LastIndex(dest, ":"); i >= 0 {
			if port := dest[i+1:]; port != "" && isAllDigits(port) {
				return dest[:i]
			}
		}
	}
	return dest
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// sshHostForPane returns the parsed destination host for the ssh client running
// in the pane rooted at panePID. tmux's pane_pid is the pane's shell, and `ssh
// mini` typed at a prompt runs as a child of that shell, so we search the process
// subtree for the ssh program rather than inspecting panePID alone (which also
// covers the case where the pane command itself is ssh). The process args are
// never logged or persisted (they may carry an -i key path).
func sshHostForPane(panePID int) string {
	if panePID <= 0 {
		return ""
	}
	if args := sshProcessArgs(panePID); args != "" {
		return parseSSHHost(args)
	}
	return ""
}

// sshPsSnapshot returns a `ps -ax` process snapshot, cached for sshPsCacheTTL so
// the ~1s name-sync poll forks ps at most once per window across all ssh windows
// (and at most once per TTL window overall), instead of once per ssh window. host
// resolution tolerates a slightly stale snapshot since the ssh destination is
// stable for the life of the connection.
var (
	sshPsCacheMu sync.Mutex
	sshPsCache   string
	sshPsCacheAt time.Time
)

const sshPsCacheTTL = 2 * time.Second

func sshPsSnapshot() string {
	sshPsCacheMu.Lock()
	defer sshPsCacheMu.Unlock()
	if sshPsCache != "" && time.Since(sshPsCacheAt) < sshPsCacheTTL {
		return sshPsCache
	}
	out, err := runCommandOutput(3*time.Second, "ps", "-ax", "-o", "pid=,ppid=,args=")
	if err != nil {
		return ""
	}
	sshPsCache = string(out)
	sshPsCacheAt = time.Now()
	return sshPsCache
}

// sshProcessArgs walks the process subtree rooted at rootPID and returns the full
// command line of the first ssh process found, or "" if none. A shared (cached)
// `ps -ax` snapshot backs the walk; the parsing + BFS is in
// sshProcessArgsFromSnapshot.
func sshProcessArgs(rootPID int) string {
	snap := sshPsSnapshot()
	if snap == "" {
		return ""
	}
	return sshProcessArgsFromSnapshot(snap, rootPID)
}

// sshProcessArgsFromSnapshot parses a `ps -ax -o pid=,ppid=,args=` snapshot and
// walks the process subtree rooted at rootPID breadth-first, returning the args
// of the first process whose program basename is ssh (rootPID itself included),
// or "" if none. Cycles are guarded by a seen set.
func sshProcessArgsFromSnapshot(psOutput string, rootPID int) string {
	type proc struct {
		args string
	}
	procs := map[int]proc{}
	children := map[int][]int{}
	for _, line := range strings.Split(psOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		procs[pid] = proc{args: strings.Join(fields[2:], " ")}
		children[ppid] = append(children[ppid], pid)
	}
	queue := []int{rootPID}
	seen := map[int]bool{rootPID: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if p, ok := procs[pid]; ok {
			if f := strings.Fields(p.args); len(f) > 0 && filepath.Base(f[0]) == "ssh" {
				return p.args
			}
		}
		for _, c := range children[pid] {
			if !seen[c] {
				seen[c] = true
				queue = append(queue, c)
			}
		}
	}
	return ""
}

// windowHasSSHPane reports whether any pane in windowID is currently running
// ssh, returning that pane's pid (for sshHostForPane). Checks every pane, not
// just the active one: ssh may run in a non-active pane of a multi-pane window.
// The returned pid is the pane's root shell pid, not the ssh process itself —
// when ssh is typed at a prompt it's a descendant. Pass it to sshHostForPane,
// which walks the subtree; do not `ps -p <pid>` it directly for the ssh args.
func windowHasSSHPane(windowID string) (panePID int, ok bool) {
	out, err := runTmuxOutput("list-panes", "-t", windowID, "-F", "#{pane_current_command} #{pane_pid}")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "ssh" {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		return pid, true
	}
	return 0, false
}

// sshWindowMarker returns the "🌐 host" name for an ssh window, or "" when the
// window has no ssh pane. Host parsing failures fall back to a bare "🌐" rather
// than an empty string, which would clear the window name and disrupt naming.
func sshWindowMarker(windowID string) string {
	pid, ok := windowHasSSHPane(windowID)
	if !ok {
		return ""
	}
	host := sanitizeWindowMarker(sshHostForPane(pid))
	if host == "" {
		return "🌐"
	}
	return "🌐 " + host
}

// reconcileSSHHost persists the ssh destination host of a window's ssh pane into
// @agent_ssh_host so the daemon's remote-bell poller can mirror that machine's 🔔.
// Cleared (option unset) when the ssh session exits. The host is sanitized (the
// same C0/C1 + '#' stripping the marker uses) before it is stored and later fed
// to ssh, so a hostile remote can't inject status-line or ssh-option payloads.
func reconcileSSHHost(windowID string) {
	host := ""
	if pid, ok := windowHasSSHPane(windowID); ok {
		host = sanitizeWindowMarker(sshHostForPane(pid))
		// Never store a destination that ssh would read as an option flag.
		if strings.HasPrefix(host, "-") {
			host = ""
		}
	}
	prev := tmuxWindowOption(windowID, "@agent_ssh_host")
	switch {
	case host == "" && prev != "":
		unsetWindowOption(windowID, "@agent_ssh_host")
	case host != "" && host != prev:
		setWindowOption(windowID, "@agent_ssh_host", host)
	}
}

// reconcileSSHPaneBorder hides the outer tmux pane-border-status on a window that
// hosts an ssh pane, so the outer pane's border title doesn't overlap the inner
// (nested) remote tmux's status line. It tracks its own change in
// @agent_ssh_border_off and restores only what it disabled (setw -u → fall back
// to the global value), leaving any manual pane-border-status override intact:
// once disabled+tracked it won't re-force off, so the user can set it back.
func reconcileSSHPaneBorder(windowID string) {
	soloSSH := windowIsSoloSSHPane(windowID)
	tracked := tmuxWindowOption(windowID, "@agent_ssh_border_off") == "1"
	switch {
	case soloSSH && !tracked:
		_ = runTmux("setw", "-t", windowID, "pane-border-status", "off")
		setWindowOption(windowID, "@agent_ssh_border_off", "1")
	case !soloSSH && tracked:
		_ = runTmux("setw", "-u", "-t", windowID, "pane-border-status")
		unsetWindowOption(windowID, "@agent_ssh_border_off")
	}
}

// windowIsSoloSSHPane reports whether windowID has exactly one pane and that pane
// is running ssh. Only a full-window ssh pane gets its border hidden (it would
// otherwise overlap the nested remote tmux status line); multi-pane windows keep
// their pane borders, which still help distinguish panes.
func windowIsSoloSSHPane(windowID string) bool {
	out, err := runTmuxOutput("list-panes", "-t", windowID, "-F", "#{pane_current_command}")
	if err != nil {
		return false
	}
	var cmds []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			cmds = append(cmds, l)
		}
	}
	return len(cmds) == 1 && cmds[0] == "ssh"
}
