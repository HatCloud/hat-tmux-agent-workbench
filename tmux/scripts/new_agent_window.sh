#!/usr/bin/env bash
set -euo pipefail

path="${1:-$PWD}"
title="${2:-}"  # 可选窗口标题；空 → 完全交给 agent-tracker 自动命名
mode="${3:-}"   # 空 → build_agent_layout 取 Window & Resize 设置

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

# 始终用 placeholder "agent" 建窗，让自动命名保持接管；用户给的标题写进
# @agent_title，由 agentWindowName 当 name 段拼进「[status] project/title (model)」
# 并走 date-strip，而不是把原始标题钉成手动窗名。
agent_pane="$(tmux new-window -P -F '#{pane_id}' -n "agent" -c "$project_root")"
window_id="$(tmux display-message -p -t "$agent_pane" '#{window_id}')"

# 把 placeholder 名字登记为「上次自动写入的名字」，让 autoRenameWindow 认出这是
# 自动管理窗（current == @agent_window_name_auto）而非用户手动命名，从而在 agent
# 进程起来后正常用 @agent_title 接管命名，而不是把 "agent" 当手动名冻结自动命名。
tmux set -w -t "$window_id" @agent_window_name_auto "agent"

if [[ -n "$title" ]]; then
  tmux set -w -t "$window_id" @agent_title "$title"
fi

"$(dirname "${BASH_SOURCE[0]}")/build_agent_layout.sh" "$window_id" "$project_root" "$mode"
