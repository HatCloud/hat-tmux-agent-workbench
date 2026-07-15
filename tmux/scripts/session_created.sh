#!/bin/bash

LOCK="/tmp/tmux-new-session.lock"
# 标志位存在「且新鲜」才让位——new_session.sh 正在手动建 session 并自行编号。
# 陈旧标志位（>10s，或 stat 读不到 mtime）视为 new_session.sh 异常退出（SIGKILL/断电
# 等 trap 兜不住的情形）的残留：忽略它、继续自动编号，而非永久跳过。10s 远大于
# new_session.sh 正常路径的亚秒耗时（含慢速 python3 调用余量）。stat 失败当陈旧，是
# 安全方向：宁可偶尔并发编号，也不重新引入「标志位永久残留卡死自动编号」这个正在修的 bug。
if [ -f "$LOCK" ]; then
  # mtime 秒。GNU coreutils（本机 homebrew stat）用 -c %Y；BSD（/usr/bin/stat）用 -f %m。
  # 不能用 `stat -f %m ... || stat -c %Y`——GNU 的 -f 是 --file-system，会把文件系统 dump
  # 写到 stdout 且 exit 0，使 || 兜底不触发、变量被垃圾污染。故显式各自尝试、取纯数字。
  lock_mtime="$(stat -c %Y "$LOCK" 2>/dev/null || /usr/bin/stat -f %m "$LOCK" 2>/dev/null)"
  if [[ "$lock_mtime" =~ ^[0-9]+$ ]] && (( $(date +%s) - lock_mtime < 10 )); then
    exit 0
  fi
fi

python3 "$HOME/.hat-config/tmux/scripts/session_manager.py" created
