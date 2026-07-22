package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/david/agent-tracker/internal/agentclient"
)

func (a *Adapter) SessionName(s agentclient.LiveSession) (agentclient.SessionNameState, error) {
	if s.PID > 0 {
		data, err := os.ReadFile(filepath.Join(a.sessionsDir(), fmt.Sprintf("%d.json", s.PID)))
		if err == nil {
			var meta sessionMeta
			if json.Unmarshal(data, &meta) == nil {
				if name := strings.TrimSpace(meta.Name); name != "" {
					return agentclient.SessionNameState{Value: name, Source: agentclient.SessionNameUser, Writable: true}, nil
				}
			}
		}
	}
	if name := customTitleFromJSONL(s.SourcePath); name != "" {
		return agentclient.SessionNameState{Value: name, Source: agentclient.SessionNameUser, Writable: true}, nil
	}
	return agentclient.SessionNameState{Source: agentclient.SessionNameNone, Writable: true}, nil
}

func (a *Adapter) SetSessionName(ctx context.Context, s agentclient.LiveSession, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.TrimSpace(s.SessionKey) == "" || strings.TrimSpace(s.SourcePath) == "" {
		return fmt.Errorf("invalid Claude session name target")
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	state, err := a.SessionName(s)
	if err != nil {
		return err
	}
	if state.Source == agentclient.SessionNameUser && state.Value != "" {
		return agentclient.ErrSessionAlreadyNamed
	}
	root := filepath.Join(a.home(), ".claude", "projects")
	rel, err := filepath.Rel(root, filepath.Clean(s.SourcePath))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Claude transcript is outside projects root")
	}
	record, err := json.Marshal(struct {
		Type        string `json:"type"`
		CustomTitle string `json:"customTitle"`
		SessionID   string `json:"sessionId"`
	}{Type: "custom-title", CustomTitle: name, SessionID: s.SessionKey})
	if err != nil {
		return err
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	f, err := os.OpenFile(s.SourcePath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	record = append(record, '\n')
	_, err = f.Write(record)
	return err
}
