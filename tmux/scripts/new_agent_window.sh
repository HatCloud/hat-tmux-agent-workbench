#!/usr/bin/env bash
set -euo pipefail

path="${1:-$PWD}"
name="${2:-agent}"
mode="${3:-}"  # 空 → build_agent_layout 取 General 设置的默认布局

if [[ -z "$path" ]]; then
  path="$PWD"
fi

if [[ -f "$path" ]]; then
  path="$(dirname "$path")"
fi

if [[ ! -d "$path" ]]; then
  path="$PWD"
fi

if root="$(git -C "$path" rev-parse --show-toplevel 2>/dev/null)"; then
  project_root="$root"
else
  project_root="$path"
fi

if [[ -z "$name" ]]; then
  name="agent"
fi

agent_pane="$(tmux new-window -P -F '#{pane_id}' -n "$name" -c "$project_root")"
window_id="$(tmux display-message -p -t "$agent_pane" '#{window_id}')"

"$(dirname "${BASH_SOURCE[0]}")/build_agent_layout.sh" "$window_id" "$project_root" "$mode"
