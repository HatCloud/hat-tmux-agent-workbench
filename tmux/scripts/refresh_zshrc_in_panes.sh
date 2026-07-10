#!/usr/bin/env bash
# 给当前 tmux server 下所有"可重载"的 pane 重读 shell 配置。
# 改完 .zshrc 后跑一下（手动、`prefix R`、或 `refresh-zshrc` alias），
# 不用逐个 pane 切回去重载。
#
# 按前台命令分派：
#   zsh|bash|fish|sh    → 直接 `source ~/.zshrc`（或 HAT_REFRESH_CMD）
#   claude*             → `! source ~/.zshrc`，靠 Claude Code TUI 的 `!`
#                         把它转给底层 shell（不会变成对 Claude 的 prompt）
#   其他 TUI（lazygit/codex/vim …）→ 跳过，send-keys 进去只会乱他们的 UI
set -euo pipefail

cmd_base="${HAT_REFRESH_CMD:-source ~/.zshrc}"

while IFS= read -r target; do
  current_cmd=$(tmux display -p -t "$target" '#{pane_current_command}')

  case "$current_cmd" in
    zsh|bash|fish|sh)
      tmux send-keys -t "$target" "$cmd_base" Enter
      ;;
    claude*)
      tmux send-keys -t "$target" "! $cmd_base" Enter
      ;;
    *)
      # TUI 进程，跳过
      ;;
  esac
done < <(tmux list-panes -a -F '#{session_name}:#{window_index}.#{pane_index}')
