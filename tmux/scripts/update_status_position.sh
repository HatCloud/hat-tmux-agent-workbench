#!/usr/bin/env bash
set -euo pipefail

# tmux status line 位置。Window & Resize 设置 status_position 决定策略：
#   top / bottom → 固定到该位置（显式设置，完全尊重——嵌套 ssh 错位靠 per-machine
#                  固定值实现：外层机固定 bottom、内层机固定 top，两层天然错开）。
#   auto（默认）  → 跟随当前 active window 的 agent 布局朝向：
#                   纵向(portrait) → 顶部；横向(landscape) → 底部；
#                   非 agent 窗口回退按 client 视觉宽高比（cell 高约宽 2 倍，
#                   width < height*2 视为竖屏）。
#                   auto 下额外：active window 跑 ssh 时强制 top（嵌套错位的
#                   自动兜底，仅当该机未固定显式值时生效）。
# 由布局变换（reflow/build）、切窗（after-select-window）、client-resized/attached
# 等时机调用，幂等（无变化不写）。

# _window_has_ssh: 该 window 任一 pane 的前台命令为 ssh（any-pane，非仅 active pane）。
_window_has_ssh() {
  tmux list-panes -t "$1" -F '#{pane_current_command}' 2>/dev/null | grep -qx ssh
}

setting="$("$HOME/.hat-config/agent-tracker/bin/agent" tmux status-position 2>/dev/null || echo auto)"

case "$setting" in
  top)    pos=top ;;
  bottom) pos=bottom ;;
  *)
    # auto：ssh 窗优先 top（嵌套错位兜底），否则跟随朝向。显式 top/bottom 不入此分支。
    active_win="$(tmux display-message -p '#{window_id}' 2>/dev/null || true)"
    if [[ -n "$active_win" ]] && _window_has_ssh "$active_win"; then
      pos=top
    else
      orient="$(tmux display-message -p '#{@agent_orientation}' 2>/dev/null || true)"
      case "$orient" in
        portrait)  pos=top ;;
        landscape) pos=bottom ;;
        *)
          dims="$(tmux display-message -p '#{client_width} #{client_height}' 2>/dev/null || true)"
          w="${dims%% *}"
          h="${dims##* }"
          if [[ -n "$w" && -n "$h" && "$h" -gt 0 ]] && (( w < h * 2 )); then
            pos=top
          else
            pos=bottom
          fi
          ;;
      esac
    fi
    ;;
esac

cur="$(tmux show -gqv status-position 2>/dev/null || true)"
[[ "$cur" == "$pos" ]] && exit 0
tmux set -g status-position "$pos"
