#!/usr/bin/env bash
set -euo pipefail

client_tty="${1-}"
window_id="${2-}"
agent_id="${3-}"
path_value="${4-}"
session_name="${5-}"
window_name="${6-}"
open_mode="${7-}"  # optional: windows, activity, settings, general, window-title, status, snippets, todos

# Width: 78% but capped at 130.
_cw=$(tmux display-message -p '#{client_width}' 2>/dev/null || echo 200)
_raw=$(( _cw * 78 / 100 ))
popup_w=$(( _raw > 130 ? 130 : _raw ))

if [[ -n "$open_mode" ]]; then
  exec tmux display-popup -E -c "$client_tty" -d "$path_value" -w "$popup_w" -h 80% -T agent \
    ~/.hat-config/agent-tracker/bin/agent palette \
    --window="$window_id" \
    --agent-id="$agent_id" \
    --path="$path_value" \
    --session-name="$session_name" \
    --window-name="$window_name" \
    --open="$open_mode"
else
  exec tmux display-popup -E -c "$client_tty" -d "$path_value" -w "$popup_w" -h 80% -T agent \
    ~/.hat-config/agent-tracker/bin/agent palette \
    --window="$window_id" \
    --agent-id="$agent_id" \
    --path="$path_value" \
    --session-name="$session_name" \
    --window-name="$window_name"
fi
