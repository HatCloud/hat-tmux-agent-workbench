package claude

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// Project-session JSONL reading: latest assistant model, latest ai-title and
// the first user prompt. The live model from the JSONL tail is authoritative —
// it tracks in-session /model switches and provider switches that the
// launch-time process args never see.

// jsonlTailBytes bounds the per-pass read for multi-MB sessions; the latest
// assistant turn and current ai-title are almost always near the end.
const jsonlTailBytes = 256 << 10

// tailScanner positions a Scanner ~tailBytes before EOF with the first (likely
// partial) line consumed.
func tailScanner(f *os.File, tailBytes int64) *bufio.Scanner {
	start := int64(0)
	if info, err := f.Stat(); err == nil && info.Size() > tailBytes {
		start = info.Size() - tailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		start = 0
		_, _ = f.Seek(0, io.SeekStart)
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 4<<20)
	if start > 0 {
		scanner.Scan()
	}
	return scanner
}

// scanModelAndTitle folds one pass over session JSONL lines, keeping the latest
// assistant model and ai-title. skipFirst drops the first (likely partial) line
// when the reader starts mid-file.
func scanModelAndNames(r io.Reader, skipFirst bool) (model, aiTitle, customTitle string) {
	var entry struct {
		Type        string `json:"type"`
		AITitle     string `json:"aiTitle"`
		CustomTitle string `json:"customTitle"`
		Message     struct {
			Model string `json:"model"`
		} `json:"message"`
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 4<<20)
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
		if entry.Type == "custom-title" && entry.CustomTitle != "" {
			customTitle = entry.CustomTitle
		}
	}
	return model, aiTitle, customTitle
}

func scanModelAndTitle(r io.Reader, skipFirst bool) (model, aiTitle string) {
	model, aiTitle, _ = scanModelAndNames(r, skipFirst)
	return model, aiTitle
}

// probeSessionJSONL reads the JSONL tail for the latest model and ai-title,
// falling back to a full scan only when the tail lacks what the caller needs
// (a huge pasted attachment can push the latest assistant turn or an older
// ai-title past the tail window).
func probeSessionJSONL(path string, needTitle bool) (model, aiTitle, customTitle string) {
	if path == "" {
		return "", "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", ""
	}
	defer f.Close()
	start := int64(0)
	if info, err := f.Stat(); err == nil && info.Size() > jsonlTailBytes {
		start = info.Size() - jsonlTailBytes
	}
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return "", "", ""
		}
	}
	model, aiTitle, customTitle = scanModelAndNames(f, start > 0)
	if start > 0 && (model == "" || (needTitle && aiTitle == "" && customTitle == "")) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return model, aiTitle, customTitle
		}
		fullModel, fullTitle, fullCustomTitle := scanModelAndNames(f, false)
		if model == "" {
			model = fullModel
		}
		if aiTitle == "" {
			aiTitle = fullTitle
		}
		if customTitle == "" {
			customTitle = fullCustomTitle
		}
	}
	return model, aiTitle, customTitle
}

func customTitleFromJSONL(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	_, _, title := scanModelAndNames(f, false)
	return strings.TrimSpace(title)
}

// firstPromptFromJSONL returns the first user message text in the session.
func firstPromptFromJSONL(path string) string {
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
	localCommand := false
	for scanner.Scan() {
		var raw struct {
			Type    string `json:"type"`
			IsMeta  bool   `json:"isMeta"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &raw) != nil ||
			raw.Type != "user" || raw.Message.Role != "user" {
			continue
		}
		prompt := strings.TrimSpace(textFromJSONContent(raw.Message.Content))
		if strings.HasPrefix(prompt, "<local-command-caveat") {
			localCommand = true
			continue
		}
		if raw.IsMeta || isClaudePromptNoise(prompt) ||
			(localCommand && strings.HasPrefix(prompt, "<command-")) {
			continue
		}
		if prompt != "" {
			return prompt
		}
	}
	return ""
}

func isClaudePromptNoise(prompt string) bool {
	prompt = strings.TrimSpace(prompt)
	for _, prefix := range []string{
		"<task-notification", "<local-command", "<bash-", "<function_results",
		"<tool_use_error", "<telem",
	} {
		if strings.HasPrefix(prompt, prefix) {
			return true
		}
	}
	return prompt == "Continue where you left off." ||
		prompt == "Continue from where you left off."
}

// textFromJSONContent flattens a message content field that is either a plain
// string or a list of typed text parts.
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
