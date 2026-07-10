#!/usr/bin/env bash
# Self-heal watchdog for the tracker-server LaunchAgent.
#
# launchd 的 KeepAlive 只能重启「崩溃退出」的进程，无法恢复「整个 job 被 bootout」
# 的情况（deploy 的 bootout→bootstrap 之间被打断、或某次 bootstrap 静默失败，job 就
# 从 launchd 彻底消失，KeepAlive 随之失效，再没人拉起 → 🔔/通知全哑）。
#
# 本脚本由每秒心跳（tracker_cache.sh 在 agent tracker state 失败时）后台触发，节流
# 后判断 job 是否还在 launchd 里：不在则 bootstrap 重新装回，在但不工作则 kickstart。
set -euo pipefail

LABEL="app.hat-tmux-workbench.agent-tracker"
PLIST="$HOME/Library/LaunchAgents/${LABEL}.plist"
THROTTLE="/tmp/agent-tracker-heal-$(id -u)"
UID_NUM=$(id -u)

# 节流：最多每 30s 尝试一次，避免 daemon 持续起不来时每秒刷 launchctl。
now=$(date +%s)
if [[ -f "$THROTTLE" ]]; then
  last=$(stat -c %Y "$THROTTLE" 2>/dev/null || stat -f %m "$THROTTLE" 2>/dev/null || echo 0)
  (( now - last < 30 )) && exit 0
fi
touch "$THROTTLE" 2>/dev/null || true

# plist 不存在说明根本没 deploy 过，不强行修复。
[[ -f "$PLIST" ]] || exit 0

if launchctl print "gui/${UID_NUM}/${LABEL}" >/dev/null 2>&1; then
  # job 仍在 launchd 注册 —— 多半 KeepAlive 已在重启，保险起见踢一脚。
  launchctl kickstart -k "gui/${UID_NUM}/${LABEL}" >/dev/null 2>&1 || true
else
  # job 已被 bootout，KeepAlive 失效 —— 重新装回。
  launchctl bootstrap "gui/${UID_NUM}" "$PLIST" >/dev/null 2>&1 || true
fi
