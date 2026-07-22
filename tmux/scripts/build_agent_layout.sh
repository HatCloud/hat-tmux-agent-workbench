#!/usr/bin/env bash
set -euo pipefail

# 按 Window & Resize 设置把一个已存在的 window 切成 ai/git 两格，或
# ai/git/run 三格，并在 git 格启动 lazygit。
# 供 new_agent_window.sh（新建）与 restore_workspace.sh（恢复）共用，
# 等价于参考方案里缺失的 project_layout.sh。
#
# 用法：build_agent_layout.sh <window_id> <path> [mode]
#   mode: landscape（默认）| auto（按窗口尺寸定朝向）| portrait
# 同时把 @agent_orientation_mode（模式）和 @agent_orientation（实际朝向）写到 window，
# 供快照兼容与状态展示使用；运行时自动 reflow 由全局开关控制。

window_id="${1:?window_id required}"
path="${2:?path required}"
mode="${3:-}"
scripts_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 未指定 mode → 取 Window & Resize 设置的初始朝向 / auto-resize 开关。
if [[ -z "$mode" ]]; then
  mode="$("$HOME/.hat-config/agent-tracker/bin/agent" tmux layout-default 2>/dev/null || echo landscape)"
fi

agent_bin="$HOME/.hat-config/agent-tracker/bin/agent"
main_percent="$("$agent_bin" tmux layout-main-percent 2>/dev/null || echo 55)"
third_pane="$("$agent_bin" tmux layout-third-pane 2>/dev/null || echo false)"
side_top_percent="$("$agent_bin" tmux layout-side-top-percent 2>/dev/null || echo 75)"

[[ "$main_percent" =~ ^[0-9]+$ ]] || main_percent=55
(( main_percent >= 40 && main_percent <= 75 )) || main_percent=55
side_percent=$((100 - main_percent))
[[ "$side_top_percent" =~ ^[0-9]+$ ]] || side_top_percent=75
(( side_top_percent >= 50 && side_top_percent <= 90 )) || side_top_percent=75
side_bottom_percent=$((100 - side_top_percent))
[[ "$third_pane" == "true" ]] || third_pane=false

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
    git_pane="$(tmux split-window -P -F '#{pane_id}' -h -l "${side_percent}%" -c "$path" -t "$ai_pane")"
    ;;
  portrait)
    git_pane="$(tmux split-window -P -F '#{pane_id}' -v -l "${side_percent}%" -c "$path" -t "$ai_pane")"
    ;;
  *)
    tmux display-message "Unknown agent layout: $layout"
    exit 2
    ;;
esac

tmux select-pane -t "$git_pane" -T git
tmux set -p -t "$git_pane" @agent_pane_role git

if [[ "$third_pane" == "true" ]]; then
  run_pane="$(tmux split-window -P -F '#{pane_id}' -v -l "${side_bottom_percent}%" -c "$path" -t "$git_pane")"
  tmux select-pane -t "$run_pane" -T run
  tmux set -p -t "$run_pane" @agent_pane_role run
fi

tmux set -w -t "$window_id" @agent_orientation "$layout"
tmux set -w -t "$window_id" @agent_orientation_mode "$mode"

tmux send-keys -t "$git_pane" "if command -v lazygit >/dev/null 2>&1; then lazygit; else git status --short --branch; fi" Enter
tmux select-pane -t "$ai_pane"

# status line 跟随布局朝向（横→底部 / 竖→顶部）
"$scripts_dir/update_status_position.sh" 2>/dev/null || true
