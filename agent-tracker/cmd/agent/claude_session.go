package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Claude Code writes one <pid>.json per running session under ~/.claude/sessions,
// holding the user-set session title in "name" (null until named) and a live
// "status" (busy/idle). The window name anchors on this title for the AI pane's
// Claude session (not the tmux session); the status drives a [B]/[I] prefix.
// Windows not launched via the `agent` launcher still get named/tagged when a
// live Claude session is detected (client inferred as "claude").

func claudeSessionsDir() string {
	return filepath.Join(homeDir(), ".claude", "sessions")
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		home = os.Getenv("HOME")
	}
	return home
}

type claudeSessionMeta struct {
	PID       int    `json:"pid"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	// Entrypoint distinguishes the interactive TUI ("cli") from headless runs
	// ("sdk-cli" for `claude -p`); kind is "interactive" for BOTH, so this is
	// the only usable discriminator.
	Entrypoint string `json:"entrypoint"`
}

// isWindowAgentSession reports whether a session should drive window features
// (naming, status prefix, task tracking, auto-retry). Only the interactive TUI
// qualifies: headless `claude -p` children (e.g. agent-hl workers spawned under
// a pane) also write session files, and matching them via the pane process tree
// used to hijack the window's state — worst case the auto-retry engine pasted
// "Continue where you left off." into a pane that was running a headless
// worker. Empty entrypoint is accepted for older Claude versions that predate
// the field.
func isWindowAgentSession(meta claudeSessionMeta) bool {
	return meta.Entrypoint == "" || meta.Entrypoint == "cli"
}

// claudeIndex is a one-shot snapshot of the process tree, the Claude session
// files and the provider map, so a single sync pass over many windows costs one
// ps + one readdir.
type claudeIndex struct {
	children      map[int][]int             // ppid -> child pids
	commands      map[int]string            // pid -> command line
	byPID         map[int]claudeSessionMeta // claude session pid -> meta
	providers     map[string]string         // ANTHROPIC_BASE_URL -> provider name
	codexRollouts map[int][]string          // codex pid -> open root/subagent rollouts
	codexStatuses codexStatusCache          // root thread -> status, shared within one sync pass
}

func buildClaudeIndex() claudeIndex {
	ci := claudeIndex{
		children:      map[int][]int{},
		commands:      map[int]string{},
		byPID:         map[int]claudeSessionMeta{},
		providers:     loadProviderMap(),
		codexRollouts: map[int][]string{},
		codexStatuses: codexStatusCache{},
	}
	if out, err := runCommandOutput(3*time.Second, "ps", "-axo", "pid=,ppid=,command="); err == nil {
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
			ci.children[ppid] = append(ci.children[ppid], pid)
			if len(f) > 2 {
				ci.commands[pid] = strings.Join(f[2:], " ")
			}
		}
	}
	var codexPIDs []string
	for pid, command := range ci.commands {
		if commandLooksLikeCodex(command) {
			codexPIDs = append(codexPIDs, strconv.Itoa(pid))
		}
	}
	if len(codexPIDs) > 0 {
		if out, err := runCommandOutput(3*time.Second, "lsof", "-Fn", "-p", strings.Join(codexPIDs, ",")); err == nil {
			ci.codexRollouts = parseCodexRolloutsFromLsof(string(out))
		}
	}
	if entries, err := os.ReadDir(claudeSessionsDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			pid, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
			if err != nil {
				continue
			}
			data, err := os.ReadFile(filepath.Join(claudeSessionsDir(), e.Name()))
			if err != nil {
				continue
			}
			var meta claudeSessionMeta
			if json.Unmarshal(data, &meta) != nil {
				continue
			}
			meta.Name = strings.TrimSpace(meta.Name)
			if !isWindowAgentSession(meta) {
				continue // headless (sdk-cli) sessions must not drive window state
			}
			ci.byPID[pid] = meta
		}
	}
	return ci
}

func claudeSessionJSONLPath(meta claudeSessionMeta) string {
	if meta.SessionID == "" || meta.CWD == "" {
		return ""
	}
	slug := strings.NewReplacer("/", "-", "_", "-", ".", "-").Replace(meta.CWD)
	return filepath.Join(homeDir(), ".claude", "projects", slug, meta.SessionID+".jsonl")
}

func claudePromptFromSession(meta claudeSessionMeta) string {
	jsonlPath := claudeSessionJSONLPath(meta)
	if jsonlPath == "" {
		return ""
	}
	f, err := os.Open(jsonlPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 4<<20)
	for scanner.Scan() {
		var raw struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &raw) != nil ||
			raw.Type != "user" || raw.Message.Role != "user" {
			continue
		}
		if prompt := strings.TrimSpace(textFromJSONContent(raw.Message.Content)); prompt != "" {
			return prompt
		}
	}
	return ""
}

func textFromJSONContent(data json.RawMessage) string {
	if len(data) == 0 || string(data) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(data, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(data, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(p.Text)
	}
	return b.String()
}

// sessionForPanePID walks panePID's descendants and returns the Claude session
// meta + its pid when one of them owns a session.
func (ci claudeIndex) sessionForPanePID(panePID int) (claudeSessionMeta, int, bool) {
	if panePID <= 0 {
		return claudeSessionMeta{}, 0, false
	}
	seen := map[int]bool{panePID: true}
	stack := []int{panePID}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if meta, ok := ci.byPID[n]; ok {
			return meta, n, true
		}
		for _, c := range ci.children[n] {
			if !seen[c] {
				seen[c] = true
				stack = append(stack, c)
			}
		}
	}
	return claudeSessionMeta{}, 0, false
}

// readSessionJSONL reads both the model and the latest AI-generated title from
// the Claude session JSONL file in a single pass.
// Path: ~/.claude/projects/<cwd-slug>/<sessionId>.jsonl
func readSessionJSONL(meta claudeSessionMeta) (model, aiTitle string) {
	jsonlPath := claudeSessionJSONLPath(meta)
	if jsonlPath == "" {
		return "", ""
	}
	f, err := os.Open(jsonlPath)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	// Tail-first so the per-call cost stays bounded for multi-MB sessions (the
	// latest assistant model and the current ai-title are almost always near the
	// end). Only when the tail has no ai-title can an older one predate the tail
	// window — fall back to a full scan then. Callers reach this function only
	// while the window still lacks a title, so the expensive path is short-lived.
	const tailBytes = 256 << 10
	start := int64(0)
	if info, err := f.Stat(); err == nil && info.Size() > tailBytes {
		start = info.Size() - tailBytes
	}
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return "", ""
		}
	}
	model, aiTitle = scanSessionJSONL(f, start > 0)
	if aiTitle == "" && start > 0 {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return model, ""
		}
		fullModel, fullTitle := scanSessionJSONL(f, false)
		if model == "" {
			model = fullModel
		}
		aiTitle = fullTitle
	}
	return model, aiTitle
}

// scanSessionJSONL folds one pass over session JSONL lines, keeping the latest
// assistant model and ai-title. skipFirst drops the first (likely partial) line
// when the reader starts mid-file.
func scanSessionJSONL(r io.Reader, skipFirst bool) (model, aiTitle string) {
	var entry struct {
		Type    string `json:"type"`
		AITitle string `json:"aiTitle"`
		Message struct {
			Model string `json:"model"`
		} `json:"message"`
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			if skipFirst {
				continue
			}
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Type == "assistant" && entry.Message.Model != "" {
			model = entry.Message.Model // keep the latest, reflecting /model switches
		}
		if entry.Type == "ai-title" && entry.AITitle != "" {
			aiTitle = entry.AITitle // keep updating to get the latest
		}
	}
	return model, aiTitle
}

// liveModelFromSession returns the model of the most recent assistant message in
// the session JSONL, reflecting in-session /model switches and provider switches
// that the launch-time process args (modelForPID) never see. It reads only the
// tail of the file (the latest assistant turn is always near the end), so the
// per-tick cost stays bounded even for multi-MB sessions. Empty if no assistant
// model is found in the tail.
func liveModelFromSession(meta claudeSessionMeta) string {
	path := claudeSessionJSONLPath(meta)
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const tailBytes = 256 << 10
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	start := info.Size() - tailBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 4<<20)
	// Drop the first (likely partial) line when we seeked into the middle.
	if start > 0 {
		scanner.Scan()
	}
	var model string
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		if entry.Type == "assistant" && entry.Message.Model != "" {
			model = entry.Message.Model // keep the latest
		}
	}
	if model != "" {
		return model
	}
	// The tail held no assistant turn (e.g. the latest assistant message is
	// followed by huge non-assistant entries like a big pasted attachment that
	// pushed it past the tail window). Fall back to a full scan rather than to
	// the static launch-time --model arg, which never reflects /model switches.
	full, _ := readSessionJSONL(meta)
	return full
}
