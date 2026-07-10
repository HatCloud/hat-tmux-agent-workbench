#!/usr/bin/env bash
set -euo pipefail

# 在 tmux 内（display-popup）用 fzf 选历史快照并恢复。
# 终端原生入口见 tmux_resume.sh。

scripts_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
restore_script="${scripts_dir}/restore_workspace.sh"

manifest="$("${scripts_dir}/workspace_snapshot_menu.sh" 2>/dev/null)" || {
  tmux display-message "fzf 不可用或没有可恢复的快照"
  exit 1
}
[[ -z "$manifest" ]] && exit 0
exec "$restore_script" "$manifest"
