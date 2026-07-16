#!/usr/bin/env bash
set -euo pipefail

# prefix u：抓当前 pane 可见区 + 近 200 行回滚里的 URL。两段式：
#   extract（本入口，经 run-shell 调用、无弹窗）——没 URL 只 display-message
#   提示、不弹窗；有 URL 落临时文件后再开 display-popup 进 pick 段。
#   pick（--pick <file>，在弹窗内跑）——fzf 选择：Enter 打开 / Ctrl-y 复制。
# 机制同 tmux-fzf-url（capture-pane + 正则 + fzf + open），自管实现免第三方
# 依赖；鼠标路径走 Ghostty 的 Shift+Cmd+Click（tmux mouse on 下的终端惯例）。
# 注意：TUI pane（Claude Code 等 alternate screen）没有 tmux 历史行，只能抓到
# 当前可视区；滚出 TUI 视口的 URL 抓不到，这是 alternate screen 的边界。
# 用法: open_urls.sh <pane_id> | open_urls.sh --pick <urls_file>

script_path="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"

if [[ "${1:-}" == "--pick" ]]; then
  urls_file="${2:?urls file required}"
  trap 'rm -f "$urls_file"' EXIT
  # awk 反转：最近出现的 URL 排最前（capture 自上而下，越靠下越新）。
  # 不用 tail -r（BSD-only）：PATH 里若是 GNU tail 会报 invalid option，
  # 给 fzf 喂空输入、弹窗一闪而过。
  selected="$(awk '{a[NR]=$0} END{for(i=NR;i>=1;i--) print a[i]}' "$urls_file" | fzf --no-multi --reverse \
    --prompt='open url> ' \
    --header='Enter 打开 · Ctrl-y 复制 · Esc 取消' \
    --expect=ctrl-y)" || exit 0
  key="$(head -1 <<< "$selected")"
  url="$(sed -n '2p' <<< "$selected")"
  [[ -z "$url" ]] && exit 0
  [[ "$url" == www.* ]] && url="https://$url"
  if [[ "$key" == "ctrl-y" ]]; then
    printf '%s' "$url" | pbcopy
    tmux display-message "已复制: $url"
  else
    open "$url"
  fi
  exit 0
fi

pane_id="${1:?pane_id required}"

# -S -200：多抓 200 行回滚，覆盖刚滚出屏幕的链接；-J 合并被折行的长 URL。
# 尾部 || true 必须有：无 URL 时 grep rc=1，set -e + pipefail 会把脚本在
# 走到下方提示前直接杀死。
urls="$(tmux capture-pane -p -J -S -200 -t "$pane_id" 2>/dev/null |
  grep -oE '(https?|ftp|file)://[^ <>"()\[\]'"'"']+|www\.[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}[^ <>"()\[\]'"'"']*' |
  sed -E 's/[.,;:!?]+$//' |
  awk '!seen[$0]++' || true)"

if [[ -z "$urls" ]]; then
  tmux display-message "当前 pane 没有找到 URL"
  exit 0
fi

urls_file="$(mktemp "${TMPDIR:-/tmp}/agent_urls.XXXXXX")"
printf '%s\n' "$urls" > "$urls_file"
exec tmux display-popup -E -w 90% -h 60% "$script_path --pick $urls_file"
