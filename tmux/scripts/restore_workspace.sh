#!/usr/bin/env bash
set -euo pipefail

# 按清单重建 tmux workspace 结构（session/window + 三格布局）。
# 不重启 Claude agent —— agent 由用户在 ai 格手动 `agent -r` 续。
#
# 用法：restore_workspace.sh [manifest]
#   manifest 缺省读 state/workspaces/last 指向的快照。
# 只在干净 tmux（1 session/1 window/1 pane）上恢复；要在已有环境上重建，
# 用 `tmux-resume -f`（它会先 kill-server 再回到本脚本的干净路径）。

workspace_dir="${HOME}/.hat-config/state/workspaces"
layout_script="$(dirname "${BASH_SOURCE[0]}")/build_agent_layout.sh"

manifest="${1:-}"
if [[ -z "$manifest" ]]; then
  last_file="${workspace_dir}/last"
  if [[ -f "$last_file" ]]; then
    manifest="$(cat "$last_file")"
  else
    tmux display-message "No workspace snapshot to restore"
    exit 1
  fi
fi

if [[ ! -f "$manifest" ]]; then
  tmux display-message "No workspace manifest: ${manifest}"
  exit 1
fi

if [[ ! -x "$layout_script" ]]; then
  tmux display-message "Missing layout script: ${layout_script}"
  exit 1
fi

# 守卫：仅在干净 tmux（1 session/1 window/1 pane）上恢复，避免污染现有环境。
session_count="$(tmux list-sessions -F '#{session_id}' | wc -l | tr -d ' ')"
window_count="$(tmux list-windows -a -F '#{window_id}' | wc -l | tr -d ' ')"
pane_count="$(tmux list-panes -a -F '#{pane_id}' | wc -l | tr -d ' ')"
if [[ "$session_count" != "1" || "$window_count" != "1" || "$pane_count" != "1" ]]; then
  tmux display-message "Restore requires exactly 1 session, 1 window, and 1 pane (用 tmux-resume -f 强制重建)"
  exit 0
fi

# 把当前唯一 session 改名 scratch 占位，恢复完再杀掉。
current_session_id="$(tmux display-message -p '#{session_id}')"
scratch_session_name="tmux-agent-restore-$$"
tmux rename-session -t "$current_session_id" "$scratch_session_name"

# 用真实 client 尺寸建新 session（缺省回退 200x50）。否则 detached session 默认
# 80x24，三格在小尺寸切好后一旦 attach 被 resize，tmux 把多出来的列全塞给 ai pane，
# 比例失衡触发 daemon 反复 reflow（可见闪烁）。建对尺寸 → 无纠正性 reflow。
client_w="$(tmux display-message -p '#{client_width}' 2>/dev/null || true)"
client_h="$(tmux display-message -p '#{client_height}' 2>/dev/null || true)"
[[ "$client_w" =~ ^[0-9]+$ && "$client_w" -gt 0 ]] || client_w=200
[[ "$client_h" =~ ^[0-9]+$ && "$client_h" -gt 0 ]] || client_h=50

restored_count=0
skipped_duplicate=0
skipped_missing_repo=0
first_restored_target=""

while IFS=$'\t' read -r session_name window_index window_name repo_root layout claude_sid; do
  [[ -z "${session_name}${window_index}${window_name}${repo_root}" ]] && continue
  layout="${layout:-landscape}"

  if [[ ! -d "$repo_root" ]]; then
    skipped_missing_repo=$((skipped_missing_repo + 1))
    continue
  fi

  if tmux has-session -t "$session_name" 2>/dev/null; then
    if tmux list-windows -t "$session_name" -F '#{window_index}' | grep -qx -- "$window_index"; then
      skipped_duplicate=$((skipped_duplicate + 1))
      continue
    fi
    window_id="$(tmux new-window -P -F '#{window_id}' -t "${session_name}:${window_index}" -n "$window_name" -c "$repo_root")"
  else
    # base-index 与环境相关（本仓库为 0），不能假设首个 window 是 index 1：
    # 取新建 window 的真实 id/index，与目标不符再 move-window 摆正。
    window_id="$(tmux new-session -d -P -F '#{window_id}' -x "$client_w" -y "$client_h" -s "$session_name" -n "$window_name" -c "$repo_root")"
    cur_index="$(tmux display-message -p -t "$window_id" '#{window_index}')"
    if [[ "$window_index" != "$cur_index" ]]; then
      tmux move-window -s "$window_id" -t "${session_name}:${window_index}" 2>/dev/null || true
    fi
  fi

  "$layout_script" "$window_id" "$repo_root" "$layout"
  tmux rename-window -t "$window_id" "$window_name"

  # Claude Code 窗口：往 ai 格预填 `claude --resume <id>`，但不回车（-l 字面发送）。
  # 用户回到窗口确认后自己按 Enter 续上崩溃前的对话。
  if [[ -n "${claude_sid:-}" ]]; then
    # 防御旧版（修复前）写坏的 manifest：第 6 列可能粘上了 cwd，截到首个空白/制表符为止。
    claude_sid="${claude_sid%%[[:space:]]*}"
    ai_pane="$(tmux list-panes -t "$window_id" -F '#{pane_index} #{pane_id}' | sort -n | awk 'NR==1{print $2}')"
    tmux send-keys -l -t "$ai_pane" "claude --resume $claude_sid"
  fi

  if [[ -z "$first_restored_target" ]]; then
    first_restored_target="${session_name}:${window_index}"
  fi
  restored_count=$((restored_count + 1))
done < "$manifest"

if [[ -n "$first_restored_target" ]]; then
  tmux switch-client -t "$first_restored_target" 2>/dev/null || true
  tmux kill-session -t "$current_session_id" 2>/dev/null || true
fi

tmux display-message "Restored ${restored_count} workspace windows (${skipped_duplicate} duplicates, ${skipped_missing_repo} missing repos skipped)"
