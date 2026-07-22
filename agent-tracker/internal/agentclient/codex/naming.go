package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/david/agent-tracker/internal/agentclient"
)

type sessionIndexSnapshot struct {
	Names     map[string]sessionIndexName
	Available bool
	Reliable  bool
}

type sessionIndexName struct {
	Latest  string
	Changed bool
}

func loadSessionIndexSnapshot(path string) sessionIndexSnapshot {
	f, err := os.Open(path)
	if err != nil {
		return sessionIndexSnapshot{Available: !os.IsNotExist(err)}
	}
	defer f.Close()
	snapshot := sessionIndexSnapshot{Names: map[string]sessionIndexName{}, Available: true, Reliable: true}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			snapshot.Reliable = false
			return snapshot
		}
		if entry.ID != "" {
			name := strings.TrimSpace(entry.ThreadName)
			prior, seen := snapshot.Names[entry.ID]
			if seen && prior.Latest != name {
				prior.Changed = true
			}
			prior.Latest = name
			snapshot.Names[entry.ID] = prior
		}
	}
	if scanner.Err() != nil {
		snapshot.Reliable = false
	}
	return snapshot
}

func (a *Adapter) sessionIndexSnapshot(idx *agentclient.Index) sessionIndexSnapshot {
	load := func() any {
		return loadSessionIndexSnapshot(filepath.Join(a.home(), ".codex", "session_index.jsonl"))
	}
	if idx == nil {
		return load().(sessionIndexSnapshot)
	}
	v := idx.Memo("codex.session-index", load)
	snapshot, _ := v.(sessionIndexSnapshot)
	return snapshot
}

func (a *Adapter) resolveThreadName(idx *agentclient.Index, threadID, dbName string, dbSupported bool) (string, bool) {
	snapshot := a.sessionIndexSnapshot(idx)
	if snapshot.Available {
		if !snapshot.Reliable {
			return "", false
		}
		if indexed, ok := snapshot.Names[threadID]; ok {
			// Codex writes one session-index record for its generated default
			// title. A later value change is the observable CLI /rename signal;
			// the SQLite name column remains NULL in Codex 0.145.0. An explicit
			// DB name (app-server thread/name/set) is authoritative even when the
			// index still contains only that initial default record.
			if indexed.Changed {
				return indexed.Latest, true
			}
			if dbSupported && strings.TrimSpace(dbName) != "" {
				return strings.TrimSpace(dbName), true
			}
			return "", true
		}
		if !dbSupported {
			// The index is valid but has not observed this thread yet, while the
			// legacy DB cannot prove whether a native name exists. Wait for a
			// later sync instead of guessing that the session is unnamed.
			return "", false
		}
	}
	return strings.TrimSpace(dbName), dbSupported
}

func sessionNameState(meta threadMeta) agentclient.SessionNameState {
	if !meta.NameSupported {
		return agentclient.SessionNameState{Source: agentclient.SessionNameUnknown}
	}
	state := agentclient.SessionNameState{
		Value:    strings.TrimSpace(meta.Name),
		Source:   agentclient.SessionNameNone,
		Writable: true,
	}
	if state.Value != "" {
		state.Source = agentclient.SessionNameUser
	}
	return state
}

func (a *Adapter) SessionName(s agentclient.LiveSession) (agentclient.SessionNameState, error) {
	threadID := strings.TrimSpace(s.SessionKey)
	if threadID == "" {
		return agentclient.SessionNameState{Source: agentclient.SessionNameUnknown}, nil
	}
	meta, ok := a.queryThreads(nil, `id = `+shellSQLString(threadID))
	if !ok {
		return agentclient.SessionNameState{Source: agentclient.SessionNameUnknown}, nil
	}
	return sessionNameState(meta), nil
}

func (a *Adapter) SetSessionName(ctx context.Context, s agentclient.LiveSession, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.TrimSpace(s.SessionKey) == "" {
		return fmt.Errorf("invalid Codex session name target")
	}
	state, err := a.SessionName(s)
	if err != nil {
		return err
	}
	if state.Source == agentclient.SessionNameUser && state.Value != "" {
		return agentclient.ErrSessionAlreadyNamed
	}
	if !state.Writable {
		return agentclient.ErrSessionNameUnsupported
	}
	if a.setName != nil {
		return a.setName(ctx, s.SessionKey, name)
	}
	return setThreadNameViaAppServer(ctx, s.SessionKey, name)
}

type appServerResponse struct {
	ID    json.RawMessage `json:"id"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func waitAppServerResponse(scanner *bufio.Scanner, wantID int) error {
	for scanner.Scan() {
		var response appServerResponse
		if json.Unmarshal(scanner.Bytes(), &response) != nil || len(response.ID) == 0 {
			continue
		}
		var id int
		if json.Unmarshal(response.ID, &id) != nil || id != wantID {
			continue
		}
		if response.Error != nil {
			return fmt.Errorf("codex app-server error %d: %s", response.Error.Code, response.Error.Message)
		}
		return nil
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func setThreadNameViaAppServer(ctx context.Context, threadID, name string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "codex", "app-server", "--listen", "stdio://")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	if err := encoder.Encode(map[string]any{
		"id": 1, "method": "initialize",
		"params": map[string]any{"clientInfo": map[string]string{
			"name": "agent-tracker", "version": "1",
		}},
	}); err != nil {
		return err
	}
	if err := waitAppServerResponse(scanner, 1); err != nil {
		return fmt.Errorf("initialize codex app-server: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	if err := encoder.Encode(map[string]any{"method": "initialized"}); err != nil {
		return err
	}
	if err := encoder.Encode(map[string]any{
		"id": 2, "method": "thread/name/set",
		"params": map[string]string{"threadId": threadID, "name": name},
	}); err != nil {
		return err
	}
	if err := waitAppServerResponse(scanner, 2); err != nil {
		return fmt.Errorf("set Codex thread name: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
