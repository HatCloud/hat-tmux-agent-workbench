#!/usr/bin/env bash
set -euo pipefail

# 把一个已存在的 window 切成 ai/git/run 三格并在 git 格起 lazygit。
# 供 new_agent_window.sh（新建）与 restore_workspace.sh（恢复）共用，
# 等价于参考方案里缺失的 project_layout.sh。
#
# 用法：build_agent_layout.sh <window_id> <path> [mode]
#   mode: auto（默认，按窗口尺寸定朝向）| landscape | portrait
# 同时把 @agent_orientation_mode（模式）和 @agent_orientation（实际朝向）写到 window，
# 供 prefix [ 循环切换与 agent-tracker 自动 reflow 使用。

window_id="${1:?window_id required}"
path="${2:?path required}"
mode="${3:-}"
scripts_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 未指定 mode → 取 General 设置的「默认布局」（auto/landscape/portrait）
if [[ -z "$mode" ]]; then
  mode="$("$HOME/.hat-config/agent-tracker/bin/agent" tmux layout-default 2>/dev/null || echo auto)"
fi

case "$mode" in
  auto)
    layout="$("$scripts_dir/orientation_for_window.sh" "$window_id")"
    ;;
  landscape|portrait)
    layout="$mode"
    ;;
  *)
    tmux display-message "Unknown agent layout mode: $mode"
    exit 2
    ;;
esac

ai_pane="$(tmux list-panes -t "$window_id" -F '#{pane_id}' | head -n 1)"
tmux select-pane -t "$ai_pane" -T agent
tmux set -p -t "$ai_pane" @agent_pane_role ai

case "$layout" in
  landscape)
    git_pane="$(tmux split-window -P -F '#{pane_id}' -h -l 34% -c "$path" -t "$ai_pane")"
    run_pane="$(tmux split-window -P -F '#{pane_id}' -v -l 24% -c "$path" -t "$git_pane")"
    ;;
  portrait)
    git_pane="$(tmux split-window -P -F '#{pane_id}' -v -l 34% -c "$path" -t "$ai_pane")"
    run_pane="$(tmux split-window -P -F '#{pane_id}' -h -l 27% -c "$path" -t "$git_pane")"
    ;;
  *)
    tmux display-message "Unknown agent layout: $layout"
    exit 2
    ;;
esac

tmux select-pane -t "$git_pane" -T git
tmux set -p -t "$git_pane" @agent_pane_role git
tmux select-pane -t "$run_pane" -T run
tmux set -p -t "$run_pane" @agent_pane_role run

tmux set -w -t "$window_id" @agent_orientation "$layout"
tmux set -w -t "$window_id" @agent_orientation_mode "$mode"

tmux send-keys -t "$git_pane" "if command -v lazygit >/dev/null 2>&1; then lazygit --screen-mode half; else git status --short --branch; fi" Enter
tmux select-pane -t "$ai_pane"

# status line 跟随布局朝向（横→底部 / 竖→顶部）
"$scripts_dir/update_status_position.sh" 2>/dev/null || true
