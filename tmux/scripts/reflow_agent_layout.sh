#!/usr/bin/env bash
set -euo pipefail

# 无损把一个两 pane(ai/git) 或三 pane(ai/git/run) agent window 重排成指定朝向。
# 用 break-pane -d 把 git[/run] 摘成游离窗口（保留进程），再按配置比例接回，
# 不杀 lazygit / run pane 里的进程，并恢复重排前的 active pane。
# 是否需要重排由 reconcile（Go 侧）判断，本脚本被调用即执行一次重排；
# 并发/高频调用靠下方每窗口锁串行化，避免 break/join 互相打断造成闪烁。
# 用法: reflow_agent_layout.sh <window_id> <portrait|landscape>

window_id="${1:?window_id required}"
target="${2:?orientation required}"

case "$target" in
  landscape|portrait) ;;
  *) tmux display-message "reflow: unknown orientation: $target"; exit 2 ;;
esac

agent_bin="$HOME/.hat-config/agent-tracker/bin/agent"
main_percent="$("$agent_bin" tmux layout-main-percent 2>/dev/null || echo 55)"
side_top_percent="$("$agent_bin" tmux layout-side-top-percent 2>/dev/null || echo 75)"
[[ "$main_percent" =~ ^[0-9]+$ ]] || main_percent=55
(( main_percent >= 40 && main_percent <= 75 )) || main_percent=55
side_percent=$((100 - main_percent))
[[ "$side_top_percent" =~ ^[0-9]+$ ]] || side_top_percent=75
(( side_top_percent >= 50 && side_top_percent <= 90 )) || side_top_percent=75
side_bottom_percent=$((100 - side_top_percent))

# 串行化：同一 window 并发/高频 reflow 会让 break-pane/join-pane 互相打断而闪烁。
# 用原子 mkdir 做每窗口锁，后来者直接让位；>10s 的陈旧锁（异常退出残留）可抢占。
lock_id="${window_id//[^0-9]/}"
lock_dir="${TMPDIR:-/tmp}/agent_reflow_${lock_id}.lock"
if ! mkdir "$lock_dir" 2>/dev/null; then
  lock_mtime="$(stat -f %m "$lock_dir" 2>/dev/null || stat -c %Y "$lock_dir" 2>/dev/null)"
  [[ "$lock_mtime" =~ ^[0-9]+$ ]] || lock_mtime=0
  if (( $(date +%s) - lock_mtime < 10 )); then
    exit 0
  fi
  rmdir "$lock_dir" 2>/dev/null || true
  mkdir "$lock_dir" 2>/dev/null || exit 0
fi
trap 'rmdir "$lock_dir" 2>/dev/null' EXIT INT TERM HUP

ai_pane=""
git_pane=""
run_pane=""
pane_total=0
while IFS='|' read -r pid role; do
  [[ -z "$pid" ]] && continue
  pane_total=$((pane_total + 1))
  case "$role" in
    ai)  ai_pane="$pid" ;;
    git) git_pane="$pid" ;;
    run) run_pane="$pid" ;;
  esac
done < <(tmux list-panes -t "$window_id" -F '#{pane_id}|#{@agent_pane_role}')

# 只对标准两 pane(ai/git) / 三 pane(ai/git/run) 布局动手，其余一律不碰。
if [[ -z "$ai_pane" || -z "$git_pane" ]]; then
  exit 0
fi
if [[ "$pane_total" -ne 2 && "$pane_total" -ne 3 ]]; then
  exit 0
fi
if [[ "$pane_total" -eq 3 && -z "$run_pane" ]]; then
  exit 0
fi

active="$(tmux display-message -p -t "$window_id" '#{pane_id}')"

# 摘下 git[/run] 成游离窗口（-d 不切过去），ai 占满整窗。
tmux break-pane -d -s "$git_pane"
if [[ -n "$run_pane" ]]; then
  tmux break-pane -d -s "$run_pane"
fi

case "$target" in
  landscape)
    tmux join-pane -h -l "${side_percent}%" -s "$git_pane" -t "$ai_pane"
    ;;
  portrait)
    tmux join-pane -v -l "${side_percent}%" -s "$git_pane" -t "$ai_pane"
    ;;
esac

if [[ -n "$run_pane" ]]; then
  tmux join-pane -v -l "${side_bottom_percent}%" -s "$run_pane" -t "$git_pane"
fi

tmux set -w -t "$window_id" @agent_orientation "$target"
tmux select-pane -t "$active"

# status line 跟随布局朝向（横→底部 / 竖→顶部）
"$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/update_status_position.sh" 2>/dev/null || true
