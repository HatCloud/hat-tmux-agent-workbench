#!/usr/bin/env bash
set -euo pipefail

# 终端原生的 workspace 恢复入口（崩溃后裸终端场景）。
# 由 `tmux-resume` 别名调用：fzf 直接在终端选快照 → 建干净 session → 恢复 → attach。
#
# 用法：
#   tmux-resume            崩溃后裸终端恢复（restore 仅在干净 1/1/1 环境执行）
#   tmux-resume -f|--force 先 kill-server 再按快照干净重建。kill 前若已有 server，
#                          比对当前 workspace 与所选快照，不一致则列差异并询问是否继续。

scripts_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
restore_script="${scripts_dir}/restore_workspace.sh"
save_script="${scripts_dir}/save_workspace.sh"

force=0
case "${1:-}" in
  -f|--force) force=1 ;;
esac

# kill-server 会杀掉脚本所在 tmux pane → -f 必须在 tmux 外的终端跑（或先 prefix d 脱离）。
if [[ "$force" == "1" && -n "${TMUX:-}" ]]; then
  echo "tmux-resume -f 会 kill-server，请在 tmux 外的终端运行（或先按 prefix d 脱离再跑）。" >&2
  exit 1
fi

manifest="$("${scripts_dir}/workspace_snapshot_menu.sh")"
[[ -z "$manifest" ]] && exit 0

if [[ "$force" == "1" ]]; then
  # 已有 server：比对当前 live 结构与所选快照（忽略易变的状态前缀/朝向/session id，
  # 只看 session+窗口号+名字+repo，即第 1-4 列），不一致就列差异并询问。
  if tmux has-session 2>/dev/null; then
    current="$("$save_script" --stdout 2>/dev/null | cut -f1-4 | sort || true)"
    snap="$(cut -f1-4 "$manifest" | sort)"
    if [[ "$current" != "$snap" ]]; then
      echo "⚠️  当前 workspace 与所选快照不一致："
      lost="$(comm -23 <(printf '%s\n' "$current") <(printf '%s\n' "$snap") | sed '/^$/d')"
      add="$(comm -13 <(printf '%s\n' "$current") <(printf '%s\n' "$snap") | sed '/^$/d')"
      [[ -n "$lost" ]] && { echo "  仅在当前（kill 后丢失，快照里没有）："; printf '%s\n' "$lost" | sed 's/\t/  /g; s/^/    - /'; }
      [[ -n "$add"  ]] && { echo "  仅在快照（将重建）：";            printf '%s\n' "$add"  | sed 's/\t/  /g; s/^/    + /'; }
      printf '仍要 kill-server 并按快照重建？[y/N] '
      read -r ans
      [[ "$ans" =~ ^[Yy]$ ]] || { echo "已取消。"; exit 0; }
    fi
    tmux kill-server 2>/dev/null || true
  fi
fi

# 无 tmux server 时先建一个干净 session（满足 restore 的 1/1/1 守卫）。
# -f 走到这里 server 已被 kill；普通模式则是裸终端崩溃后场景。
if ! tmux has-session 2>/dev/null; then
  tmux new-session -d -s resume
fi

"$restore_script" "$manifest"

# restore 会把第一个恢复的 window 设为 active 并杀掉 scratch；attach 接上去
if [[ -z "${TMUX:-}" ]]; then
  exec tmux attach
fi
