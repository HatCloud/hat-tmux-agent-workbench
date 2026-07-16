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

// Codex session 解析：rollout/SQLite 状态探测、thread 定位、prompt 提取。
// 从 claude_session.go 按职责拆出（原六簇职责堆叠成 2100+ 行热点文件）。

type codexThreadMeta struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	CWD         string `json:"cwd"`
	Model       string `json:"model"`
	RolloutPath string `json:"rollout_path"`
	Status      string
}

type codexStatusCache map[string]string

func (c codexStatusCache) status(meta codexThreadMeta, load func(codexThreadMeta) string) string {
	key := meta.ID + "\x00" + meta.RolloutPath
	if status, ok := c[key]; ok {
		return status
	}
	status := load(meta)
	c[key] = status
	return status
}

func codexStateDB() string {
	return filepath.Join(homeDir(), ".codex", "state_5.sqlite")
}

func codexLogsDB() string {
	return filepath.Join(homeDir(), ".codex", "logs_2.sqlite")
}

func shellSQLString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
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
	return rows[0], true
}

const codexToolCallQuietWindow = 5 * time.Second

const codexStatusLineBuffer = 1 << 20

func codexStatusFromRollout(path string) string {
	return codexStatusFromRolloutAt(path, time.Now())
}

func codexStatusFromRolloutAt(path string, now time.Time) string {
	return codexStatusSnapshotFromRolloutAt(path, now).Status
}

type codexStatusSnapshot struct {
	Status            string
	LastTaskStartedAt time.Time
	LastProgressAt    time.Time
}

func codexStatusSnapshotFromRolloutAt(path string, now time.Time) codexStatusSnapshot {
	var snapshot codexStatusSnapshot
	path = strings.TrimSpace(path)
	if path == "" {
		return snapshot
	}
	f, err := os.Open(path)
	if err != nil {
		return snapshot
	}
	defer f.Close()
	status := ""
	lastMeaningful := ""
	autoReviewApprovals := false
	pendingUserInputCallIDs := map[string]struct{}{}
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
			CallID            string `json:"call_id"`
			ApprovalsReviewer string `json:"approvals_reviewer"`
		}
		if json.Unmarshal(entry.Payload, &payload) != nil {
			continue
		}
		entryAt, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
		markProgress := func() {
			if entryAt.After(snapshot.LastProgressAt) {
				snapshot.LastProgressAt = entryAt
			}
		}
		switch entry.Type {
		case "turn_context":
			autoReviewApprovals = payload.ApprovalsReviewer == "auto_review"
		case "event_msg":
			switch payload.Type {
			case "task_started":
				status = "busy"
				lastMeaningful = "task_started"
				snapshot.LastTaskStartedAt = entryAt
				markProgress()
				clear(pendingUserInputCallIDs)
			case "task_complete":
				status = "idle"
				lastMeaningful = "task_complete"
				clear(pendingUserInputCallIDs)
			case "turn_aborted", "thread_rolled_back":
				// Codex can terminate a turn without task_complete when an editor
				// action aborts or rolls it back. Both events leave the thread idle.
				status = "idle"
				lastMeaningful = payload.Type
				clear(pendingUserInputCallIDs)
			case "agent_message":
				lastMeaningful = "agent_message"
				markProgress()
			case "user_message":
				if status == "" {
					status = "busy"
				}
				lastMeaningful = "user_message"
				markProgress()
			}
		case "response_item":
			switch payload.Type {
			case "reasoning":
				markProgress()
			case "function_call", "custom_tool_call":
				if payload.Name == "request_user_input" {
					// Rollouts retain earlier turns, so keep explicit questions pending
					// until their matching output or a terminal event arrives.
					pendingUserInputCallIDs[payload.CallID] = struct{}{}
				}
				status = "busy"
				lastMeaningful = "tool_call"
				markProgress()
				if ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
					lastToolCallAt = ts
				}
			case "function_call_output", "custom_tool_call_output":
				if payload.CallID != "" {
					delete(pendingUserInputCallIDs, payload.CallID)
				}
				if status == "" {
					status = "busy"
				}
				lastMeaningful = "tool_output"
			case "message":
				if payload.Role == "assistant" {
					lastMeaningful = "agent_message"
					markProgress()
				}
			}
		}
		if err != nil {
			break
		}
	}
	if status == "busy" && len(pendingUserInputCallIDs) > 0 {
		snapshot.Status = "asking"
		return snapshot
	}
	if status == "busy" && !autoReviewApprovals && lastMeaningful == "tool_call" && !lastToolCallAt.IsZero() && now.Sub(lastToolCallAt) >= codexToolCallQuietWindow {
		snapshot.Status = "asking"
		return snapshot
	}
	snapshot.Status = status
	return snapshot
}

func resolveCodexStatus(snapshot codexStatusSnapshot, errorAt time.Time) string {
	if !errorAt.IsZero() && !errorAt.Before(snapshot.LastProgressAt) {
		return "error"
	}
	return snapshot.Status
}

func latestCodexTurnErrorFromDB(dbPath, threadID string, since time.Time) (time.Time, bool) {
	dbPath = strings.TrimSpace(dbPath)
	threadID = strings.TrimSpace(threadID)
	if dbPath == "" || threadID == "" {
		return time.Time{}, false
	}
	if _, err := os.Stat(dbPath); err != nil {
		return time.Time{}, false
	}
	query := `select ts, ts_nanos from logs where thread_id = ` + shellSQLString(threadID) +
		` and ts >= ` + strconv.FormatInt(since.Unix(), 10) +
		` and level = 'INFO' and target = 'codex_core::session::turn'` +
		` and instr(feedback_log_body, 'Turn error:') > 0` +
		` order by ts desc, ts_nanos desc, id desc limit 1;`
	out, err := runCommandOutput(3*time.Second, "sqlite3", "-json", dbPath, query)
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return time.Time{}, false
	}
	var rows []struct {
		Seconds     int64 `json:"ts"`
		Nanoseconds int64 `json:"ts_nanos"`
	}
	if json.Unmarshal(out, &rows) != nil || len(rows) == 0 {
		return time.Time{}, false
	}
	return time.Unix(rows[0].Seconds, rows[0].Nanoseconds), true
}

func codexStatusForThread(meta codexThreadMeta) string {
	snapshot := codexStatusSnapshotFromRolloutAt(meta.RolloutPath, time.Now())
	errorAt, _ := latestCodexTurnErrorFromDB(codexLogsDB(), meta.ID, snapshot.LastTaskStartedAt)
	return resolveCodexStatus(snapshot, errorAt)
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

func commandLooksLikeCodex(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" || strings.Contains(command, " app-server") {
		return false
	}
	fields := strings.Fields(command)
	for i, f := range fields {
		if filepath.Base(f) != "codex" {
			continue
		}
		// Headless `codex exec` runs (agent-hl workers etc.) must not drive
		// window naming/status — mirrors the Claude sdk-cli entrypoint filter.
		if i+1 < len(fields) && fields[i+1] == "exec" {
			return false
		}
		return true
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
		meta.Status = ci.codexStatuses.status(meta, codexStatusForThread)
		return meta, codexPID, true
	}
	cwd := ""
	if out, err := runTmuxOutput("display-message", "-p", "-t", aiPane, "#{pane_current_path}"); err == nil {
		cwd = strings.TrimSpace(out)
	}
	meta, _ := latestCodexThreadForCWD(cwd)
	meta.Status = ci.codexStatuses.status(meta, codexStatusForThread)
	return meta, codexPID, true
}
