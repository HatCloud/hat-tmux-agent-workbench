#!/bin/bash

LOCK="/tmp/tmux-new-session.lock"
current_session_id="${1:-}"
current_path="${2:-}"
label="${3:-}"

touch "$LOCK"
# 标志位（非互斥锁）：告诉 session_created.sh「本脚本正在手动建 session 并自行编号，
# 别插手自动编号」。用 trap 覆盖成功/失败/信号所有退出路径，避免进程被杀后标志位
# 永久残留、卡死此后所有新建 session 的自动编号（session_created.sh 侧另有陈旧超时兜底
# SIGKILL/断电这类 trap 兜不住的情形）。
trap 'rm -f "$LOCK"' EXIT INT TERM HUP
# label 为空：用临时名 'session' 走 session-created hook 自动编号
if [ -n "$label" ]; then
  tmux_args=(new-session -d -P -s "$label" -F '#{session_id}')
else
  tmux_args=(new-session -d -P -s 'session' -F '#{session_id}')
fi
if [ -n "$current_path" ]; then
  tmux_args+=( -c "$current_path" )
  printf -v start_cmd 'cd %q && exec ${SHELL:-/bin/zsh} -l' "$current_path"
  tmux_args+=( "$start_cmd" )
fi
session_id=$(tmux "${tmux_args[@]}" 2>/dev/null)

if [ -z "$session_id" ]; then
  exit 0
fi

if [ -n "$current_session_id" ]; then
  python3 "$HOME/.hat-config/tmux/scripts/session_manager.py" insert-right "$current_session_id" "$session_id"
else
  python3 "$HOME/.hat-config/tmux/scripts/session_manager.py" ensure
fi

tmux switch-client -t "$session_id"
