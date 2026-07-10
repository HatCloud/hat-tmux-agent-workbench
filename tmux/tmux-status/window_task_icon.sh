#!/usr/bin/env bash
set -euo pipefail

window_id="${1:-}"
unread="${2:-0}"
watching="${3:-0}"
watch_failed="${4:-0}"
remote_bell="${5:-0}"
[[ -z "$window_id" ]] && exit 0

has_bell=0
has_watch=0
has_question=0
has_fail=0

[[ "$unread" == "1" ]] && has_bell=1
[[ "$unread" == "1" && "$watch_failed" == "1" ]] && has_fail=1
# Remote 🔔 mirrored from a machine this window is ssh'd into (set by the daemon).
[[ "$remote_bell" == "1" ]] && has_bell=1

[[ "$watching" == "1" ]] && has_watch=1

question_pane=$(tmux list-panes -t "$window_id" -F '#{@op_question_pending}' 2>/dev/null | grep -F -m1 -x '1' || true)
[[ -n "$question_pane" ]] && has_question=1

CACHE_FILE="$HOME/.hat-config/state/agent-tracker/tmux-tracker-cache.json"
if [[ -f "$CACHE_FILE" ]]; then
  state=$(cat "$CACHE_FILE" 2>/dev/null || true)
  if [[ -n "$state" ]]; then
    # bell = 未读注意力（完成或 asking 未读）→ 🔔。asking 本身由窗口名的 [?] 前缀表示，不再加 ❓。
    result=$(echo "$state" | jq -r --arg wid "$window_id" '
      .tasks // [] | .[] | select(.window_id == $wid) |
      if (.acknowledged != true) and (.asking == true or .status == "completed") then "bell"
      else empty end
    ' 2>/dev/null | head -1 || true)
    case "$result" in
      bell) has_bell=1 ;;
    esac
  fi
fi

if (( has_question )); then
  printf '❓ '
elif (( has_fail )); then
  printf '❌ '
elif (( has_bell )); then
  printf '🔔 '
elif (( has_watch )); then
  printf '⏳ '
fi
