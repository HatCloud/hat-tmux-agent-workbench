#!/usr/bin/env bash
# claude_report.sh — bridge a Claude Code Stop hook to the agent-tracker daemon.
# Derives the tmux coordinate from $TMUX_PANE and reports the matching tracker
# command, and captures the Claude session id for workspace --resume restore.
# A no-op outside tmux so the hook never blocks or errors when Claude runs in a
# plain terminal.
#
# Usage (wired by deploy.sh into ~/.claude/settings.json hooks):
#   claude_report.sh finish_task   # Stop: work done → 🔔 (+ capture session id)
#   claude_report.sh start_task [summary]
# (The Notification → notify hook was retired: notifications now come from the
#  daemon's sessions-json poll, see docs/ARCHITECTURE.md.)
set -euo pipefail

action="${1:-}"
[[ -z "$action" ]] && exit 0

# Only report from inside a tmux pane; $TMUX_PANE is set by tmux per pane.
pane="${TMUX_PANE:-}"
[[ -z "$pane" ]] && exit 0

agent_bin="$HOME/.hat-config/agent-tracker/bin/agent"
[[ -x "$agent_bin" ]] || exit 0

# Resolve the full coordinate (session_id::window_id::pane_id) from the pane.
coords="$(tmux display-message -t "$pane" -p '#{session_id}::#{window_id}::#{pane_id}' 2>/dev/null || true)"
[[ -z "$coords" ]] && exit 0
sid="${coords%%::*}"
rest="${coords#*::}"
wid="${rest%%::*}"
pid="${rest##*::}"
[[ -z "$sid" || -z "$wid" || -z "$pid" ]] && exit 0

# Capture Claude Code's own session id (from the hook's JSON stdin) keyed by the
# pane it runs in, so save_workspace.sh can record it and restore can prefill
# `claude --resume <id>`. Guarded on a non-tty stdin so manual CLI invocations
# don't block on cat. cwd is stored alongside to guard against tmux recycling a
# pane id to a different repo before the next save.
if [[ ! -t 0 ]]; then
  payload="$(cat 2>/dev/null || true)"
  if [[ -n "$payload" ]]; then
    claude_sid="$(printf '%s' "$payload" \
      | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
    if [[ -n "$claude_sid" ]]; then
      cwd="$(tmux display-message -t "$pane" -p '#{pane_current_path}' 2>/dev/null || true)"
      map_dir="$HOME/.hat-config/state/workspaces/claude-sessions"
      mkdir -p "$map_dir"
      printf '%s\t%s\n' "$claude_sid" "$cwd" > "${map_dir}/${pid//[^A-Za-z0-9]/_}"
    fi
  fi
fi

# NOTE: flags MUST precede the subcommand — `agent tracker command` parses
# leading flags and stops at the first non-flag arg (the subcommand). Putting
# the subcommand first would leave the coordinates unparsed and fold them into
# the summary.
case "$action" in
  start_task)
    "$agent_bin" tracker command \
      -session-id "$sid" -window-id "$wid" -pane "$pid" \
      -summary "${2:-working}" start_task >/dev/null 2>&1 || true
    ;;
  finish_task)
    "$agent_bin" tracker command \
      -session-id "$sid" -window-id "$wid" -pane "$pid" finish_task >/dev/null 2>&1 || true
    ;;
  *)
    exit 0
    ;;
esac
