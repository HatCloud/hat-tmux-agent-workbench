package codex

import (
	"bufio"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/david/agent-tracker/internal/agentclient"
)

// Rollout status scan + SQLite turn-error probe. A turn is busy between
// task_started and a terminal event; an unanswered request_user_input (or a
// tool call sitting quiet past the approval window) is "asking"; a "Turn
// error:" log at/after the last progress signal resolves to "error".

// toolCallQuietWindow: a pending tool call with no output for this long (and
// approvals not on auto) is treated as an approval prompt → asking.
const toolCallQuietWindow = 5 * time.Second

// statusLineBuffer bounds one JSONL line read; tool outputs can make a single
// record several megabytes.
const statusLineBuffer = 1 << 20

type statusSnapshot struct {
	Status            string
	LastTaskStartedAt time.Time
	LastProgressAt    time.Time
}

func statusSnapshotFromRolloutAt(path string, now time.Time) statusSnapshot {
	var snapshot statusSnapshot
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
	reader := bufio.NewReaderSize(f, statusLineBuffer)
	for {
		line, err := reader.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// None of the status-driving records need their large payload, so
			// discard the rest of this record and continue instead of stopping
			// before a later task_complete event.
			for err == bufio.ErrBufferFull {
				_, err = reader.ReadSlice('\n')
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
	if status == "busy" && !autoReviewApprovals && lastMeaningful == "tool_call" && !lastToolCallAt.IsZero() && now.Sub(lastToolCallAt) >= toolCallQuietWindow {
		snapshot.Status = "asking"
		return snapshot
	}
	snapshot.Status = status
	return snapshot
}

// resolveStatus overlays the SQLite turn-error signal: an error at/after the
// last progress signal means the turn stopped on it.
func resolveStatus(snapshot statusSnapshot, errorAt time.Time) string {
	if !errorAt.IsZero() && !errorAt.Before(snapshot.LastProgressAt) {
		return "error"
	}
	return snapshot.Status
}

// latestTurnErrorFromDB finds the newest "Turn error:" log record for the
// thread since the given instant.
func latestTurnErrorFromDB(dbPath, threadID string, since time.Time) (time.Time, bool) {
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
	out, err := agentclient.RunOutput(3*time.Second, "sqlite3", "-json", dbPath, query)
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

// promptFromRollout returns the first user message in a rollout JSONL.
func promptFromRollout(path string) string {
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
			if prompt := userPromptFromPayload(raw.Payload); prompt != "" {
				return prompt
			}
		}
	}
	return ""
}

func userPromptFromPayload(data json.RawMessage) string {
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


// textFromJSONContent flattens a string-or-parts content field (same shape as
// Claude transcripts use).
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
