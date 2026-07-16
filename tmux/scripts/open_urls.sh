#!/usr/bin/env bash
set -euo pipefail

# prefix u：抓当前 pane 可见区 + 近 200 行回滚里的 URL，fzf 选择后用系统默认
# 浏览器打开（Enter 打开、Ctrl-y 仅复制到剪贴板、Esc 取消）。
# 机制同 tmux-fzf-url（capture-pane + 正则 + fzf + open），自管实现免第三方
# 依赖；鼠标路径走 Ghostty 的 Shift+Cmd+Click（tmux mouse on 下的终端惯例），
# 本脚本是键盘补充。调用方（tmux.conf）经 display-popup -E 弹窗执行。
# 用法: open_urls.sh <pane_id>

pane_id="${1:?pane_id required}"

# -S -200：多抓 200 行回滚，覆盖刚滚出屏幕的链接；-J 合并被折行的长 URL。
urls="$(tmux capture-pane -p -J -S -200 -t "$pane_id" 2>/dev/null |
  grep -oE '(https?|ftp|file)://[^ <>"()\[\]'"'"']+|www\.[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}[^ <>"()\[\]'"'"']*' |
  sed -E 's/[.,;:!?]+$//' |
  awk '!seen[$0]++')"

if [[ -z "$urls" ]]; then
  tmux display-message "当前 pane 没有找到 URL"
  exit 0
fi

# tac：最近出现的 URL 排最前（capture 自上而下，越靠下越新）。
selected="$(tail -r <<< "$urls" | fzf --no-multi --reverse \
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
