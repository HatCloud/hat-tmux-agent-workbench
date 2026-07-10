package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/david/agent-tracker/internal/ipc"
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
}

type codexThreadMeta struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	CWD         string `json:"cwd"`
	Model       string `json:"model"`
	RolloutPath string `json:"rollout_path"`
	Status      string
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
}

func buildClaudeIndex() claudeIndex {
	ci := claudeIndex{
		children:      map[int][]int{},
		commands:      map[int]string{},
		byPID:         map[int]claudeSessionMeta{},
		providers:     loadProviderMap(),
		codexRollouts: map[int][]string{},
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
			ci.byPID[pid] = meta
		}
	}
	return ci
}

func codexStateDB() string {
	return filepath.Join(homeDir(), ".codex", "state_5.sqlite")
}

func shellSQLString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func agentTitleForWindow(title string) string {
	title = strings.Join(strings.Fields(strings.TrimSpace(title)), " ")
	return truncateDisplayCells(title, 20)
}

func truncateDisplayCells(text string, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	if displayCells(text) <= maxCells {
		return text
	}
	target := maxCells - 1
	var b strings.Builder
	width := 0
	r := []rune(text)
	for _, ch := range r {
		cw := runeDisplayCells(ch)
		if width+cw > target {
			break
		}
		b.WriteRune(ch)
		width += cw
	}
	return b.String() + "…"
}

func displayCells(text string) int {
	width := 0
	for _, r := range text {
		width += runeDisplayCells(r)
	}
	return width
}

func runeDisplayCells(r rune) int {
	if r == '\t' {
		return 4
	}
	if r < 0x80 {
		return 1
	}
	return 2
}

func latestCodexThreadForCWD(cwd string) (codexThreadMeta, bool) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return codexThreadMeta{}, false
	}
	query := `select id, title, cwd, coalesce(model, '') as model, rollout_path ` +
		`from threads where cwd = ` + shellSQLString(cwd) +
		` and source = 'cli' order by updated_at_ms desc limit 1;`
	out, err := runCommandOutput(3*time.Second, "sqlite3", "-json", codexStateDB(), query)
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return codexThreadMeta{}, false
	}
	var rows []codexThreadMeta
	if json.Unmarshal(out, &rows) != nil || len(rows) == 0 {
		return codexThreadMeta{}, false
	}
	rows[0].Title = agentTitleForWindow(rows[0].Title)
	rows[0].Status = codexStatusFromRollout(rows[0].RolloutPath)
	return rows[0], true
}

func codexThreadForRollouts(paths []string) (codexThreadMeta, bool) {
	var quoted []string
	seen := map[string]bool{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		quoted = append(quoted, shellSQLString(path))
	}
	if len(quoted) == 0 {
		return codexThreadMeta{}, false
	}
	query := `select id, title, cwd, coalesce(model, '') as model, rollout_path ` +
		`from threads where rollout_path in (` + strings.Join(quoted, ",") +
		`) and source = 'cli' order by updated_at_ms desc limit 1;`
	out, err := runCommandOutput(3*time.Second, "sqlite3", "-json", codexStateDB(), query)
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return codexThreadMeta{}, false
	}
	var rows []codexThreadMeta
	if json.Unmarshal(out, &rows) != nil || len(rows) == 0 {
		return codexThreadMeta{}, false
	}
	rows[0].Title = agentTitleForWindow(rows[0].Title)
	rows[0].Status = codexStatusFromRollout(rows[0].RolloutPath)
	return rows[0], true
}

const codexToolCallQuietWindow = 5 * time.Second

const codexStatusLineBuffer = 1 << 20

func codexStatusFromRollout(path string) string {
	return codexStatusFromRolloutAt(path, time.Now())
}

func codexStatusFromRolloutAt(path string, now time.Time) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	status := ""
	lastMeaningful := ""
	autoReviewApprovals := false
	var lastToolCallAt time.Time
	var entry struct {
		Timestamp string          `json:"timestamp"`
		Type      string          `json:"type"`
		Payload   json.RawMessage `json:"payload"`
	}
	reader := bufio.NewReaderSize(f, codexStatusLineBuffer)
	for {
		line, err := reader.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// Tool outputs can make a single JSONL record several megabytes. None
			// of the status-driving records need their large payload, so discard
			// the rest of this record and continue instead of letting Scanner stop
			// before a later task_complete event.
			for err == bufio.ErrBufferFull {
				_, err = reader.ReadSlice('\n')
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
			continue
		}
		if len(line) == 0 && err != nil {
			break
		}
		entry = struct {
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}{}
		if json.Unmarshal(line, &entry) != nil {
			if err != nil {
				break
			}
			continue
		}
		var payload struct {
			Type              string `json:"type"`
			Role              string `json:"role"`
			Name              string `json:"name"`
			ApprovalsReviewer string `json:"approvals_reviewer"`
		}
		if json.Unmarshal(entry.Payload, &payload) != nil {
			continue
		}
		switch entry.Type {
		case "turn_context":
			autoReviewApprovals = payload.ApprovalsReviewer == "auto_review"
		case "event_msg":
			switch payload.Type {
			case "task_started":
				status = "busy"
				lastMeaningful = "task_started"
			case "task_complete":
				status = "idle"
				lastMeaningful = "task_complete"
			case "turn_aborted", "thread_rolled_back":
				// Codex can terminate a turn without task_complete when an editor
				// action aborts or rolls it back. Both events leave the thread idle.
				status = "idle"
				lastMeaningful = payload.Type
			case "agent_message":
				lastMeaningful = "agent_message"
			case "user_message":
				if status == "" {
					status = "busy"
				}
				lastMeaningful = "user_message"
			}
		case "response_item":
			switch payload.Type {
			case "function_call", "custom_tool_call":
				if payload.Name == "request_user_input" {
					return "asking"
				}
				status = "busy"
				lastMeaningful = "tool_call"
				if ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
					lastToolCallAt = ts
				}
			case "function_call_output", "custom_tool_call_output":
				if status == "" {
					status = "busy"
				}
				lastMeaningful = "tool_output"
			case "message":
				if payload.Role == "assistant" {
					lastMeaningful = "agent_message"
				}
			}
		}
		if err != nil {
			break
		}
	}
	if status == "busy" && !autoReviewApprovals && lastMeaningful == "tool_call" && !lastToolCallAt.IsZero() && now.Sub(lastToolCallAt) >= codexToolCallQuietWindow {
		return "asking"
	}
	return status
}

func codexPromptFromRollout(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 4<<20)
	for scanner.Scan() {
		var raw struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &raw) != nil {
			continue
		}
		switch raw.Type {
		case "event_msg":
			var payload struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}
			if json.Unmarshal(raw.Payload, &payload) == nil &&
				payload.Type == "user_message" && strings.TrimSpace(payload.Message) != "" {
				return strings.TrimSpace(payload.Message)
			}
		case "response_item":
			if prompt := userPromptFromCodexPayload(raw.Payload); prompt != "" {
				return prompt
			}
		}
	}
	return ""
}

func userPromptFromCodexPayload(data json.RawMessage) string {
	var payload struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(data, &payload) != nil ||
		payload.Type != "message" || payload.Role != "user" {
		return ""
	}
	return strings.TrimSpace(textFromJSONContent(payload.Content))
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

func commandLooksLikeCodex(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" || strings.Contains(command, " app-server") {
		return false
	}
	fields := strings.Fields(command)
	for _, f := range fields {
		if filepath.Base(f) == "codex" {
			return true
		}
	}
	return false
}

func parseCodexRolloutsFromLsof(out string) map[int][]string {
	result := map[int][]string{}
	pid := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "p") {
			pid, _ = strconv.Atoi(strings.TrimPrefix(line, "p"))
			continue
		}
		if pid <= 0 || !strings.HasPrefix(line, "n") {
			continue
		}
		path := strings.TrimPrefix(line, "n")
		if strings.Contains(path, string(filepath.Separator)+".codex"+string(filepath.Separator)+"sessions"+string(filepath.Separator)) &&
			strings.HasPrefix(filepath.Base(path), "rollout-") &&
			strings.HasSuffix(path, ".jsonl") {
			result[pid] = append(result[pid], path)
		}
	}
	return result
}

func (ci claudeIndex) codexRolloutPathsForPanePID(panePID int) []string {
	if panePID <= 0 {
		return nil
	}
	seen := map[int]bool{panePID: true}
	stack := []int{panePID}
	var paths []string
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		paths = append(paths, ci.codexRollouts[n]...)
		for _, child := range ci.children[n] {
			if !seen[child] {
				seen[child] = true
				stack = append(stack, child)
			}
		}
	}
	return paths
}

func (ci claudeIndex) codexPIDForPanePID(panePID int) (int, bool) {
	if panePID <= 0 {
		return 0, false
	}
	seen := map[int]bool{panePID: true}
	stack := []int{panePID}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if commandLooksLikeCodex(ci.commands[n]) {
			return n, true
		}
		for _, c := range ci.children[n] {
			if !seen[c] {
				seen[c] = true
				stack = append(stack, c)
			}
		}
	}
	return 0, false
}

func codexThreadForPane(aiPane string, ci *claudeIndex) (codexThreadMeta, int, bool) {
	if ci == nil {
		built := buildClaudeIndex()
		ci = &built
	}
	codexPID, ok := ci.codexPIDForPanePID(panePID(aiPane))
	if !ok {
		return codexThreadMeta{}, 0, false
	}
	if meta, ok := codexThreadForRollouts(ci.codexRolloutPathsForPanePID(panePID(aiPane))); ok {
		return meta, codexPID, true
	}
	cwd := ""
	if out, err := runTmuxOutput("display-message", "-p", "-t", aiPane, "#{pane_current_path}"); err == nil {
		cwd = strings.TrimSpace(out)
	}
	meta, _ := latestCodexThreadForCWD(cwd)
	return meta, codexPID, true
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

// statusTag maps a Claude session status to the window-name prefix.
func statusTag(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "busy":
		return "[B] "
	case "idle":
		return "[I] "
	case "asking", "waiting", "paused":
		return "[?] "
	case "limited":
		return "[L] "
	default:
		return ""
	}
}

// loadProviderMap reads ~/.claude/providers/*.env into a map from the provider's
// ANTHROPIC_BASE_URL to its name (file basename). official unsets the URL and so
// is absent (treated as the empty-URL default).
func loadProviderMap() map[string]string {
	m := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(homeDir(), ".claude", "providers"))
	if err != nil {
		return m
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".env") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(homeDir(), ".claude", "providers", e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "unset") || !strings.Contains(line, "ANTHROPIC_BASE_URL=") {
				continue
			}
			v := line[strings.Index(line, "ANTHROPIC_BASE_URL=")+len("ANTHROPIC_BASE_URL="):]
			if v = strings.Trim(strings.TrimSpace(v), `"'`); v != "" {
				m[v] = strings.TrimSuffix(e.Name(), ".env")
			}
			break
		}
	}
	return m
}

// providerForPID reads the Claude process env's ANTHROPIC_BASE_URL and maps it to
// a provider name. Empty/unset → "official" (which unsets the URL).
func providerForPID(pid int, providers map[string]string) string {
	if pid <= 0 {
		return ""
	}
	out, err := runCommandCombinedOutput(3*time.Second, "ps", "eww", "-p", strconv.Itoa(pid))
	if err != nil {
		return ""
	}
	for _, tok := range strings.Fields(string(out)) {
		if strings.HasPrefix(tok, "ANTHROPIC_BASE_URL=") {
			url := strings.TrimPrefix(tok, "ANTHROPIC_BASE_URL=")
			if name, ok := providers[url]; ok {
				return name
			}
			return ""
		}
	}
	return "official"
}

// agentAIPane returns the window's primary AI pane (@agent_pane_role=ai),
// falling back to its active pane. Empty when the window has no panes.
func agentAIPane(windowID string, ci *claudeIndex) string {
	if strings.TrimSpace(windowID) == "" {
		return ""
	}
	out, err := runTmuxOutput("list-panes", "-t", windowID, "-F", "#{pane_id}::#{@agent_pane_role}::#{pane_active}")
	if err != nil {
		return ""
	}
	active := ""
	var paneIDs []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "::", 3)
		if len(parts) != 3 || parts[0] == "" {
			continue
		}
		if parts[1] == "ai" {
			return parts[0]
		}
		paneIDs = append(paneIDs, parts[0])
		if parts[2] == "1" && active == "" {
			active = parts[0]
		}
	}
	// No pane tagged role=ai (e.g. a window rebuilt by workspace-restore that
	// lost its @agent_pane_role). Prefer the pane whose process tree actually
	// hosts a Claude session over the active pane, which may be the git/run/zsh
	// pane when the user has focus there.
	if ci != nil {
		for _, p := range paneIDs {
			if _, _, ok := ci.sessionForPanePID(panePID(p)); ok {
				return p
			}
		}
	}
	return active
}

func panePID(paneID string) int {
	if strings.TrimSpace(paneID) == "" {
		return 0
	}
	out, err := runTmuxOutput("display-message", "-p", "-t", paneID, "#{pane_pid}")
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0
	}
	return pid
}

// agentWindowName builds the agent window name in the format:
//
//	[B] project/name [model]
//
// tmux status bar already prepends the window index, so we don't add a session
// index prefix here (that would produce double numbers like "1:1:[I] name").
// Status [B]/[I] comes from the live Claude session.
// project/name respects the show_path config toggle.
// [model] is appended when show_model is on and a model is detected.
// Empty for non-agent windows. ci is reused across windows; pass nil for a one-shot.
func agentWindowName(windowID, sessionID, aiPane string, ci *claudeIndex) string {
	client := tmuxWindowOption(windowID, "@agent_client")
	model := tmuxWindowOption(windowID, "@agent_model")

	idx := ci
	if idx == nil {
		built := buildClaudeIndex()
		idx = &built
	}
	meta, claudePID, hasClaude := idx.sessionForPanePID(panePID(aiPane))
	codexMeta, _, hasCodex := codexThreadForPane(aiPane, idx)
	liveTitle := ""
	liveStatus := ""

	if hasClaude {
		client = "claude"
		// Live model from the JSONL tail (latest assistant turn) is authoritative:
		// it tracks in-session /model switches and provider switches that the
		// launch-time process args miss. Fall back to the process --model arg only
		// when no assistant message has been written yet.
		if m := liveModelFromSession(meta); m != "" {
			model = m
		} else if m := modelForPID(claudePID); m != "" {
			model = m
		}
		// Read full JSONL only for the title fallback (latest AI-generated title).
		var aiTitle string
		if meta.Name == "" {
			_, aiTitle = readSessionJSONL(meta)
		}
		// Detect the live provider from the Claude process env (ANTHROPIC_BASE_URL
		// → providers/*.env). This is the single authoritative source; the
		// @agent_provider option is only a cache written once at window creation
		// and goes stale (or is missing) when the provider is switched or the
		// window is rebuilt by workspace-restore. Persist it so Window Nav and
		// the status bar read the correct value.
		provider := providerForPID(claudePID, idx.providers)
		if provider != "" && tmuxWindowOption(windowID, "@agent_provider") != provider {
			_ = runTmux("set", "-w", "-t", windowID, "@agent_provider", provider)
		}
		// Fallback: read ANTHROPIC_MODEL from provider env file (e.g. minimax).
		if model == "" {
			model = modelFromProviderEnv(provider)
		}
		// Persist raw model name so Window Nav and other consumers can read it.
		if model != "" && tmuxWindowOption(windowID, "@agent_model") != model {
			_ = runTmux("set", "-w", "-t", windowID, "@agent_model", model)
		}
		if tmuxWindowOption(windowID, "@agent_client") == "" {
			_ = runTmux("set", "-w", "-t", windowID, "@agent_client", "claude")
		}
		// Use AI-generated title as default name when user hasn't set one.
		if meta.Name == "" && aiTitle != "" {
			meta.Name = aiTitle
		}
		liveTitle = agentTitleForWindow(meta.Name)
		liveStatus = meta.Status
		// A session whose latest turn died on a usage-limit 429 is its own
		// "limited" status ([L]): the dialog blocks input and no timer/idle
		// semantics apply. The reset instant is stamped on the window so the
		// same sync pass (task reconcile) and other consumers reuse the probe.
		if !strings.EqualFold(liveStatus, "busy") {
			if resetAt, ok := claudeLimitResetFromSession(meta, time.Now()); ok {
				liveStatus = "limited"
				stamp := strconv.FormatInt(resetAt.Unix(), 10)
				if tmuxWindowOption(windowID, "@agent_limit_reset_at") != stamp {
					_ = runTmux("set", "-w", "-t", windowID, "@agent_limit_reset_at", stamp)
				}
			} else if tmuxWindowOption(windowID, "@agent_limit_reset_at") != "" {
				_ = runTmux("set", "-wu", "-t", windowID, "@agent_limit_reset_at")
			}
		}
	} else if hasCodex {
		client = "codex"
		if codexMeta.Model != "" {
			model = codexMeta.Model
		}
		liveTitle = codexMeta.Title
		liveStatus = codexMeta.Status
		if tmuxWindowOption(windowID, "@agent_client") == "" {
			_ = runTmux("set", "-w", "-t", windowID, "@agent_client", "codex")
		}
		if model != "" && tmuxWindowOption(windowID, "@agent_model") != model {
			_ = runTmux("set", "-w", "-t", windowID, "@agent_model", model)
		}
	} else {
		// No live claude/codex in this window.
		if client != "" {
			// Stale agent tags: either the agent exited or the launcher tagged the
			// window before its process came up. Drop the live-detected
			// provider/model so Window Nav shows no phantom provider (e.g. a
			// lingering "official") for a window with no running agent. Keep
			// @agent_client as the window's structural identity; it is refilled
			// when a claude/codex process appears.
			if tmuxWindowOption(windowID, "@agent_provider") != "" {
				_ = runTmux("set", "-wu", "-t", windowID, "@agent_provider")
			}
			if tmuxWindowOption(windowID, "@agent_model") != "" {
				_ = runTmux("set", "-wu", "-t", windowID, "@agent_model")
			}
		}
		// If it's running ssh, mark it "🌐 host" (no [B]/[I] status prefix).
		// autoRenameWindow keeps manual renames.
		if marker := sshWindowMarker(windowID); marker != "" {
			return marker
		}
		if client == "" {
			return ""
		}
	}

	cfg := loadAppConfig()

	// Use @agent_dir if available (set at window creation); fall back to live pane path.
	agentDir := tmuxWindowOption(windowID, "@agent_dir")
	var project string
	if agentDir != "" {
		project = filepath.Base(agentDir)
	} else {
		project = tmuxProjectName(aiPane)
	}
	sessionName := tmuxSessionName(sessionID)
	_, sessionLabel := splitSessionLabel(sessionName)

	// Name part priority: live session title > persisted @agent_title > session
	// label > project dir.
	sessionTitle := liveTitle
	if sessionTitle == "" {
		// @agent_title may be a user-typed title (prefix ] prompt). Strip C0/C1
		// control chars and '#' so a pasted title can't corrupt the status line —
		// same hygiene as the ssh host marker.
		sessionTitle = sanitizeWindowMarker(tmuxWindowOption(windowID, "@agent_title"))
	}

	// When enabled (default), strip a leading YYYY-MM-DD- from the title/label
	// segment so task dirs like "2026-07-09-open-source-refactor" render as
	// "open-source-refactor" in the tab, @agent_notify_name, and Window Nav Name.
	// (The project/dir segment already strips it via abbrevProject.)
	stripDate := stripDatePrefixSetting(cfg)
	titleSeg := maybeStripDatePrefix(sessionTitle, stripDate)
	labelSeg := maybeStripDatePrefix(sessionLabel, stripDate)

	// assemble builds "[status]project/name (model)", each part gated by a flag.
	// tmux already shows the window index before the name, so no idx prefix here.
	assemble := func(showStatus, showPath, showModel bool) string {
		var namePart string
		switch {
		case titleSeg != "":
			if showPath && project != "" {
				namePart = abbrevProject(project) + "/" + titleSeg
			} else {
				namePart = titleSeg
			}
		case labelSeg != "":
			if showPath && project != "" {
				namePart = abbrevProject(project) + "/" + labelSeg
			} else {
				namePart = labelSeg
			}
		default:
			namePart = abbrevProject(project)
		}
		if namePart == "" {
			return ""
		}
		name := namePart
		if showStatus {
			name = statusTag(liveStatus) + namePart
		}
		if showModel && strings.TrimSpace(model) != "" {
			name += " (" + normalizeModelNameLong(model) + ")"
		}
		return name
	}

	// Persist the notification title: always the full project/name (model) form
	// with no status prefix, independent of the window-tab display toggles, so
	// notifications stay self-descriptive even when the tab name is compact. The
	// daemon reads @agent_notify_name when building notification titles.
	if notify := assemble(false, true, true); notify != "" &&
		tmuxWindowOption(windowID, "@agent_notify_name") != notify {
		_ = runTmux("set", "-w", "-t", windowID, "@agent_notify_name", notify)
	}

	// Stamp last-busy timestamp every tick the agent is actively working so
	// window nav can display "idle since" even after the panel is reopened.
	if liveStatus == "busy" {
		_ = runTmux("set", "-w", "-t", windowID, "@agent_last_busy_at",
			strconv.FormatInt(time.Now().Unix(), 10))
	}

	return assemble(windowNameShowStatus(cfg), windowNameShowPath(cfg), windowNameShowModel(cfg))
}

// autoRenameWindow applies an auto-computed name to windowID while respecting
// manual renames. We track the last auto-set name in @agent_window_name_auto:
//   - First call (option unset): always renames and records the name.
//   - Subsequent calls where current name == @agent_window_name_auto: renames on change.
//
// extractStatusPrefix returns the leading [B]/[I]/[?]/[L] prefix from name (with trailing space).
func extractStatusPrefix(name string) string {
	for _, p := range []string{"[B] ", "[I] ", "[?] ", "[L] "} {
		if strings.HasPrefix(name, p) {
			return p
		}
	}
	return ""
}

// stripStatusPrefix removes a leading [B]/[I]/[?]/[L] prefix if present.
func stripStatusPrefix(name string) string {
	name = strings.TrimPrefix(name, "[B] ")
	name = strings.TrimPrefix(name, "[I] ")
	name = strings.TrimPrefix(name, "[?] ")
	name = strings.TrimPrefix(name, "[L] ")
	return name
}

//   - Current name is empty/blank: user cleared it — resume auto-naming.
//   - Current name differs from @agent_window_name_auto (non-empty): user renamed it
//     manually — still update [B]/[I] status prefix, but keep user's base name.
//
// placeholderWindowName is the literal name new_agent_window.sh gives a
// freshly created window (tmux new-window -n "agent") before an agent
// process has started. A window still showing exactly this name was never
// actually renamed by anyone; treating it as a manual rename would freeze
// auto-naming on it forever the moment @agent_window_name_auto happens to
// hold a stale value (e.g. after workspace restore recreates the window, or
// a transient poll failure) — see the manual-override guard below.
const placeholderWindowName = "agent"

func autoRenameWindow(windowID, name string) {
	cur, _ := runTmuxOutput("display-message", "-p", "-t", windowID, "#{window_name}")
	cur = strings.TrimSpace(cur)
	lastAuto := strings.TrimSpace(tmuxWindowOption(windowID, "@agent_window_name_auto"))

	// Manual-override: user renamed the window — keep their base name but still
	// update the [B]/[I] status prefix so busy/idle is always current. The
	// placeholder exception keeps a window stuck on "agent" from being
	// mistaken for a deliberate rename (see placeholderWindowName above).
	if lastAuto != "" && cur != "" && cur != lastAuto && stripStatusPrefix(cur) != placeholderWindowName {
		// Clear call (name==""): the source that earned the auto-name is gone
		// (e.g. ssh exited) but the user has their own name. Keep their name, but
		// drop the tracking option so the poll stops re-entering this path.
		if name == "" {
			_ = runTmux("set-option", "-w", "-u", "-t", windowID, "@agent_window_name_auto")
			return
		}
		newStatus := extractStatusPrefix(name)
		userBase := stripStatusPrefix(cur)
		newName := newStatus + userBase
		if newName != cur {
			_ = runTmux("rename-window", "-t", windowID, newName)
		}
		return
	}

	// Clearing our auto-name (e.g. an ssh window after the session exited): tmux
	// leaves automatic-rename off once a window has been renamed, so we re-enable it
	// to hand the window back to tmux (which relabels from the pane command on the
	// next tick). We deliberately do NOT rename-window "" here — an explicit rename
	// turns automatic-rename back off. Drop the tracking option so we don't re-enter.
	if name == "" {
		_ = runTmux("set-option", "-w", "-t", windowID, "automatic-rename", "on")
		if lastAuto != "" {
			_ = runTmux("set-option", "-w", "-u", "-t", windowID, "@agent_window_name_auto")
		}
		return
	}

	if cur != name {
		_ = runTmux("rename-window", "-t", windowID, name)
	}
	if lastAuto != name {
		_ = runTmux("set-option", "-w", "-t", windowID, "@agent_window_name_auto", name)
	}
}

const syncNamesMaxRun = 20 * time.Second

func syncNamesLockPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("agent-sync-names-%d.lock", os.Getuid()))
}

func syncNamesLastStartPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("agent-sync-names-%d.last", os.Getuid()))
}

// acquireSyncNamesLock uses a kernel flock instead of a mkdir/PID lock. The
// kernel releases it automatically if the process exits or is killed, so a crash
// cannot leave sync-names permanently disabled. An overlapping trigger is
// coalesced rather than queued, bounding the worker count at one.
func acquireSyncNamesLock(path string) (release func(), acquired bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
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

// runTmuxSyncNames re-syncs every window's name from its AI pane's Claude session
// (or launcher @agent_client). The status bar invokes --periodic every second,
// which is rate-limited to one full pass per 5s; navigation hooks remain immediate.
// All callers share a non-blocking kernel lock, so slow passes are coalesced and
// can never accumulate into a process storm.
func runTmuxSyncNames(args []string) error {
	release, acquired, err := acquireSyncNamesLock(syncNamesLockPath())
	if err != nil || !acquired {
		return nil
	}
	defer release()

	periodic := len(args) > 0 && args[0] == "--periodic"
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

	out, err := runTmuxOutput("list-windows", "-a", "-F", "#{session_id}::#{window_id}")
	if err != nil {
		return nil
	}
	ci := buildClaudeIndex()
	// Daemon task status per pane, to drive the 🔔 (completed-unread) icon from
	// the live Claude busy/idle status.
	taskByPane := map[string]string{}
	if st, err := trackerLoadState(""); err == nil && st != nil {
		for _, tk := range st.Tasks {
			taskByPane[tk.Pane] = tk.Status
		}
	}
	checkAndFireTimers(&ci)

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if time.Now().After(deadline) {
			break
		}
		parts := strings.SplitN(strings.TrimSpace(line), "::", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		sessionID, windowID := parts[0], parts[1]
		aiPane := agentAIPane(windowID, &ci)
		if name := agentWindowName(windowID, sessionID, aiPane, &ci); name != "" {
			autoRenameWindow(windowID, name)
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
			// Keep the ai/git/run layout matching its configured orientation.
			// Yield while a reflow-focus debounce is in flight, so the 1s poll
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
						_ = runTmux("set", "-w", "-t", windowID, "@agent_dir", dir)
					}
				}
			}
			if meta, _, ok := ci.sessionForPanePID(panePID(aiPane)); ok {
				// Persist session title so Window Nav can display it without re-parsing.
				if meta.Name != "" {
					_ = runTmux("set", "-w", "-t", windowID, "@agent_title", agentTitleForWindow(meta.Name))
				}
				// Reuse the limited probe agentWindowName stamped above so the
				// daemon sees "limited" (asking-like) instead of idle→completed.
				if _, limited := windowQuotaLimitedUntil(windowID); limited &&
					!strings.EqualFold(meta.Status, "busy") {
					meta.Status = "limited"
				}
				reconcileTask(sessionID, windowID, aiPane, meta, taskByPane[aiPane])
			} else if meta, _, ok := codexThreadForPane(aiPane, &ci); ok {
				if meta.Title != "" {
					_ = runTmux("set", "-w", "-t", windowID, "@agent_title", meta.Title)
				}
				reconcileTaskStatus(sessionID, windowID, aiPane, meta.Title, meta.Status, taskByPane[aiPane])
			}
		}
	}
	return nil
}

// reconcileWindowOrientation makes a window's actual layout match its configured
// mode: a pinned landscape/portrait window is forced to that orientation, while an
// "auto" window follows its current dimensions. No-op unless it's a standard
// 3-pane ai/git/run layout and the orientation actually differs.
// Called from the ~1s name-sync poll and on focus/resize (agent tmux reflow-focus),
// so switching the terminal between portrait/landscape reflows every window the
// moment it's selected.
func reconcileWindowOrientation(windowID string) {
	mode := tmuxWindowOption(windowID, "@agent_orientation_mode")
	if mode == "" {
		mode = "auto"
	}
	// Skip while the window is zoomed or any pane is in a tmux mode (copy-mode,
	// choose-tree, etc.). list-panes reports the zoomed pane at full window size,
	// so the layout looks "wrong" and triggers a reflow; doing break-pane/join-pane
	// on a pane that's currently hosting an interactive picker (e.g. `prefix W`'s
	// `choose-tree -Zw`) destabilizes the tmux state and can kill the server.
	if z, err := runTmuxOutput("display-message", "-p", "-t", windowID, "#{window_zoomed_flag}"); err == nil && strings.TrimSpace(z) == "1" {
		return
	}
	if m, err := runTmuxOutput("list-panes", "-t", windowID, "-F", "#{pane_in_mode}"); err == nil {
		for _, f := range strings.Fields(m) {
			if f == "1" {
				return
			}
		}
	}
	// Only ever touch a standard 3-pane ai/git/run layout.
	out, err := runTmuxOutput("list-panes", "-t", windowID, "-F", "#{@agent_pane_role}")
	if err != nil {
		return
	}
	roles := map[string]bool{}
	n := 0
	for _, r := range strings.Fields(out) {
		roles[r] = true
		n++
	}
	if n != 3 || !roles["ai"] || !roles["git"] || !roles["run"] {
		return
	}
	current := tmuxWindowOption(windowID, "@agent_orientation")
	var desired string
	switch mode {
	case "landscape", "portrait":
		desired = mode // pinned: enforce the configured orientation
	default: // auto: follow the window's current dimensions
		dim, err := runTmuxOutput("display-message", "-p", "-t", windowID, "#{window_width} #{window_height}")
		if err != nil {
			return
		}
		var w, h int
		if _, err := fmt.Sscanf(strings.TrimSpace(dim), "%d %d", &w, &h); err != nil || w <= 0 || h <= 0 {
			return
		}
		desired = desiredOrientation(w, h, current)
	}
	if desired == "" {
		return
	}
	// Reflow when the orientation is wrong, OR when it's right but the ai pane's
	// proportions drifted (e.g. a restored / mid-resize layout with a too-small main
	// pane) — orientation alone isn't enough to call a layout correct.
	if desired == current && layoutProportionsOK(windowID, desired) {
		return
	}
	script := filepath.Join(homeDir(), ".hat-config", "tmux", "scripts", "reflow_agent_layout.sh")
	_, _ = runCommandOutput(10*time.Second, script, windowID, desired)
}

// reflowDebounceDelay is the trailing-debounce wait. Dragging a terminal to
// fullscreen emits a burst of window-resized events, each spawning its own
// `agent tmux reflow-focus` process; without debouncing they reflow 4-5 times in
// sequence before the size settles. The window only needs the LAST one.
const reflowDebounceDelay = 450 * time.Millisecond

// reflowDebouncePath is the per-window file holding the latest reflow request's
// token (a UnixNano timestamp). Keyed by the window id's digits.
func reflowDebouncePath(windowID string) string {
	id := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, windowID)
	return filepath.Join(os.TempDir(), "agent_reflow_debounce_"+id)
}

// reflowDebounceClaim records this caller as the latest reflow request, waits the
// debounce delay, then reports whether it is still the latest (no newer request
// arrived) — i.e. the trailing-debounce winner that should perform the reflow.
// Returns true (proceed) if the debounce file is unusable, to never deadlock.
func reflowDebounceClaim(windowID string) bool {
	path := reflowDebouncePath(windowID)
	token := strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.WriteFile(path, []byte(token), 0o644); err != nil {
		return true
	}
	time.Sleep(reflowDebounceDelay)
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(data)) == token
}

// reflowDebouncePending reports whether a reflow-focus debounce is currently in
// flight (a request was registered within the last delay). The 1s poll checks
// this to yield to the debounced winner instead of reflowing a transient layout.
func reflowDebouncePending(windowID string) bool {
	data, err := os.ReadFile(reflowDebouncePath(windowID))
	if err != nil {
		return false
	}
	token, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(0, token)) < reflowDebounceDelay
}

// layoutProportionsOK reports whether the ai pane occupies roughly the expected
// ~66% of the window along the layout's main axis (width for landscape, height for
// portrait). Returns true (don't reflow) when it can't measure, to stay conservative.
func layoutProportionsOK(windowID, orientation string) bool {
	out, err := runTmuxOutput("list-panes", "-t", windowID, "-F", "#{@agent_pane_role}|#{pane_width}|#{pane_height}")
	if err != nil {
		return true
	}
	aiW, aiH := 0, 0
	found := false
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) == 3 && parts[0] == "ai" {
			aiW, _ = strconv.Atoi(parts[1])
			aiH, _ = strconv.Atoi(parts[2])
			found = true
			break
		}
	}
	if !found {
		return true
	}
	dim, err := runTmuxOutput("display-message", "-p", "-t", windowID, "#{window_width} #{window_height}")
	if err != nil {
		return true
	}
	var ww, wh int
	if _, err := fmt.Sscanf(strings.TrimSpace(dim), "%d %d", &ww, &wh); err != nil {
		return true
	}
	aiDim, winDim := aiH, wh
	if orientation == "landscape" {
		aiDim, winDim = aiW, ww
	}
	if winDim <= 0 {
		return true
	}
	pct := aiDim * 100 / winDim
	return pct >= 60 && pct <= 72 // expected ~66%, with tolerance
}

// desiredOrientation maps window dimensions to landscape/portrait with a hysteresis
// dead-band, so a window hovering near square doesn't flip-flop every poll. Terminal
// cells are ~2x taller than wide, so a visually square window has width == 2*height.
func desiredOrientation(w, h int, current string) string {
	switch {
	case w*10 >= h*22: // clearly wide
		return "landscape"
	case w*10 <= h*18: // clearly tall
		return "portrait"
	default:
		return current // dead-band: keep current orientation
	}
}

// modelForPID reads the raw model name from the Claude process args (--model <value>).
func modelForPID(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := runCommandOutput(3*time.Second, "ps", "-p", strconv.Itoa(pid), "-o", "args=")
	if err != nil {
		return ""
	}
	args := strings.Fields(string(out))
	for i, arg := range args {
		if arg == "--model" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, "--model=") {
			return strings.TrimPrefix(arg, "--model=")
		}
	}
	return ""
}

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

// sanitizeWindowMarker strips C0/C1 control characters (incl. DEL) plus the tmux
// format character '#' from s, so a malformed alias/hostname can't inject markup
// into the status line. Multi-byte printable runes (e.g. the 🌐 emoji) pass through.
func sanitizeWindowMarker(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == '#' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
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
		_ = runTmux("set", "-w", "-u", "-t", windowID, "@agent_ssh_host")
	case host != "" && host != prev:
		_ = runTmux("set", "-w", "-t", windowID, "@agent_ssh_host", host)
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
		_ = runTmux("set", "-w", "-t", windowID, "@agent_ssh_border_off", "1")
	case !soloSSH && tracked:
		_ = runTmux("setw", "-u", "-t", windowID, "pane-border-status")
		_ = runTmux("set", "-w", "-u", "-t", windowID, "@agent_ssh_border_off")
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
	var entry struct {
		Type    string `json:"type"`
		AITitle string `json:"aiTitle"`
		Message struct {
			Model string `json:"model"`
		} `json:"message"`
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
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

// modelFromSessionJSONL is a convenience wrapper used for model-only lookups.
func modelFromSessionJSONL(pid int) string {
	sessionFile := filepath.Join(claudeSessionsDir(), fmt.Sprintf("%d.json", pid))
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return ""
	}
	var meta claudeSessionMeta
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	model, _ := readSessionJSONL(meta)
	return model
}

// modelFromProviderEnv reads ANTHROPIC_MODEL from a provider .env file.
// Used for providers (e.g. minimax) that set the model via env var, not --model flag.
func modelFromProviderEnv(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(homeDir(), ".claude", "providers", provider+".env"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"export ANTHROPIC_MODEL=", "ANTHROPIC_MODEL="} {
			if after, ok := strings.CutPrefix(line, prefix); ok {
				return strings.Trim(strings.TrimSpace(after), "\"'")
			}
		}
	}
	return ""
}

// normalizeModelName maps raw model IDs to short family names for the status bar.
// e.g. "claude-sonnet-4-6" → "sonnet", "MiniMax-M3" → "MiniMax-M3"
func normalizeModelName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	lower := strings.ToLower(model)
	for _, family := range []string{"opus", "sonnet", "haiku", "fable"} {
		if strings.Contains(lower, family) {
			return family
		}
	}
	r := []rune(model)
	if len(r) > 12 {
		return string(r[:12]) + "…"
	}
	return model
}

// normalizeModelNameLong maps raw model IDs to a longer form for Window Nav.
// e.g. "claude-sonnet-4-6" → "sonnet4.6", "sonnet" → "sonnet4.6", "MiniMax-M3" → "MiniMax-M3"
func normalizeModelNameLong(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	// Short-name aliases: map to current versioned equivalents.
	shortNames := map[string]string{
		"opus":   "opus4.8",
		"sonnet": "sonnet4.6",
		"haiku":  "haiku4.5",
		"fable":  "fable5",
	}
	if v, ok := shortNames[strings.ToLower(model)]; ok {
		return v
	}
	s := model
	if lower := strings.ToLower(model); strings.HasPrefix(lower, "claude-") {
		s = model[7:]
	}
	parts := strings.Split(s, "-")
	for i, p := range parts {
		lp := strings.ToLower(p)
		for _, f := range []string{"opus", "sonnet", "haiku", "fable"} {
			if lp == f {
				var ver []string
				for _, vp := range parts[i+1:] {
					if _, err := strconv.Atoi(vp); err != nil {
						break
					}
					ver = append(ver, vp)
				}
				if len(ver) > 0 {
					return lp + strings.Join(ver, ".")
				}
				return lp
			}
		}
	}
	// Non-Claude model (minimax, etc.): return as-is, capped at 16 chars.
	r := []rune(model)
	if len(r) > 16 {
		return string(r[:16]) + "…"
	}
	return model
}

// reconcileAction 是 reconcileActions 决定要发给 daemon 的单条命令。抽成纯数据让
// 状态→命令的映射可单测，reconcileTask 只负责按它拼 Envelope 并发送。
type reconcileAction struct {
	command string
	asking  bool // 仅 command=="mark_asking" 有意义
}

// reconcileActions 把（已规整的）Claude 会话 status + daemon 当前任务状态映射成要发送
// 的命令序列（纯函数、可单测）。语义：
//   - busy：未在跑→start_task；在跑→mark_asking{false}（从 asking 回来时清标志）。
//   - asking/waiting/paused/limited：未在跑先 start_task，再 mark_asking{true}（保序）。
//     limited（额度满，429 后弹窗挡输入）视同 asking——需要用户注意，且不能被当 idle
//     误发完成。
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
	case "asking", "waiting", "paused", "limited":
		if !inProgress {
			return []reconcileAction{{command: "start_task"}, {command: "mark_asking", asking: true}}
		}
		return []reconcileAction{{command: "mark_asking", asking: true}}
	case "idle":
		if inProgress {
			return []reconcileAction{{command: "finish_task"}}
		}
		return nil
	case "shell":
		if inProgress {
			return []reconcileAction{{command: "mark_asking", asking: false}}
		}
		return nil
	default:
		return nil
	}
}

// reconcileTask drives the daemon's task state from the live Claude session
// status so the status bar's 🔔 (completed-unread) reflects "finished while you
// were away". busy → ensure a task exists (in_progress); busy→idle → finish it
// (completed → 🔔 until focus acknowledges, with a grace debounce in the daemon).
// "shell" (turn ended but background work pending) is treated as still-active so
// a transient idle around the turn boundary doesn't raise a premature completion.
func reconcileTask(sessionID, windowID, pane string, meta claudeSessionMeta, daemonStatus string) {
	reconcileTaskStatus(sessionID, windowID, pane, meta.Name, meta.Status, daemonStatus)
}

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
		}
		_ = sendTrackerCommand(act.command, env)
	}
}
