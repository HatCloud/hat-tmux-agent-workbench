// Package paths is the single source of truth for agent-tracker's on-disk
// locations after the hat-config migration.
//
// Layout (see docs/ARCHITECTURE.md "路径四分类"):
//   - runtime state + socket → ~/.hat-config/state/agent-tracker/   (gitignored)
//   - vendored binaries      → ~/.hat-config/agent-tracker/bin/      (gitignored)
//   - user config            → ~/.config/agent-tracker/             (kept in place)
//
// The socket path in particular MUST be identical across tracker-server,
// tracker-mcp and the agent palette, otherwise daemon/MCP/palette cannot
// connect; centralising it here guarantees that.
package paths

import (
	"os"
	"path/filepath"
)

// home returns the user's home directory, falling back to $HOME.
func home() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.Getenv("HOME")
}

// hatConfigRoot returns ~/.hat-config.
func hatConfigRoot() string {
	return filepath.Join(home(), ".hat-config")
}

// StateDir returns the runtime-state root ~/.hat-config/state/agent-tracker.
// It replaces the reference repo's ~/.config/agent-tracker/run directory.
func StateDir() string {
	return filepath.Join(hatConfigRoot(), "state", "agent-tracker")
}

// SocketPath returns the unix socket path. Single source of truth for all
// three callers (tracker-server, tracker-mcp, agent palette).
func SocketPath() string {
	return filepath.Join(StateDir(), "agent-tracker.sock")
}

// SettingsStore returns the daemon settings.json runtime path.
func SettingsStore() string {
	return filepath.Join(StateDir(), "settings.json")
}

// AgentBin returns the vendored agent binary path.
func AgentBin() string {
	return filepath.Join(hatConfigRoot(), "agent-tracker", "bin", "agent")
}

// ConfigFile returns the user config path, kept at ~/.config/agent-tracker.
func ConfigFile() string {
	return filepath.Join(home(), ".config", "agent-tracker", "agent-config.json")
}

// TimersFile returns the path for persisted window timer definitions.
func TimersFile() string {
	return filepath.Join(home(), ".config", "agent-tracker", "window-timers.json")
}

// TimerHistoryFile returns the path for per-window timer content history.
// Co-located with TimersFile (same timer-data class, not StateDir): history
// survives timer deletion and is keyed by window.
func TimerHistoryFile() string {
	return filepath.Join(home(), ".config", "agent-tracker", "window-timer-history.json")
}
