#!/usr/bin/env bash
set -euo pipefail

current_session_id="${1:-}"
current_session_name="${2:-}"
term_width="${3:-}"
status_bg="${4:-}"

[[ -z "$status_bg" || "$status_bg" == "default" ]] && status_bg=black
[[ ! "$term_width" =~ ^[0-9]+$ ]] && term_width=100

inactive_bg="#373b41"
inactive_fg="#c5c8c6"
active_bg="${TMUX_THEME_COLOR:-#b294bb}"
active_fg="#1d1f21"
ICON_SET=$(jq -r '.icon_set // "nerd"' "$HOME/.config/agent-tracker/agent-config.json" 2>/dev/null || echo nerd)
case "$ICON_SET" in
  emoji) separator="▐" ;;
  ascii) separator=" " ;;
  *)     separator="" ;;
esac
left_cap="█"
max_width=18

left_narrow_width=${TMUX_LEFT_NARROW_WIDTH:-80}
is_narrow=0
[[ "$term_width" =~ ^[0-9]+$ ]] && (( term_width < left_narrow_width )) && is_narrow=1

normalize_session_id() {
  local value="$1"
  value="${value#\$}"
  printf '%s' "$value"
}

trim_label() {
  local value="$1"
  if [[ "$value" =~ ^[0-9]+-(.*)$ ]]; then
    printf '%s' "${BASH_REMATCH[1]}"
  else
    printf '%s' "$value"
  fi
}

extract_index() {
  local value="$1"
  if [[ "$value" =~ ^([0-9]+)-.*$ ]]; then
    printf '%s' "${BASH_REMATCH[1]}"
  else
    printf ''
  fi
}




sessions=$(tmux list-sessions -F '#{session_id}::#{session_name}' 2>/dev/null || true)
if [[ -z "$sessions" ]]; then
  exit 0
fi

"$HOME/.hat-config/tmux/tmux-status/tracker_cache.sh" 2>/dev/null || true

CACHE_FILE="$HOME/.hat-config/state/agent-tracker/tmux-tracker-cache.json"
tracker_state=""
if [[ -f "$CACHE_FILE" ]]; then
  tracker_state=$(cat "$CACHE_FILE" 2>/dev/null || true)
fi

question_state=$(tmux list-panes -a -F '#{session_id}::#{@op_question_pending}' 2>/dev/null || true)

# Session 图标 = 该 session 下「当前存在的」window 图标的聚合，与窗口级
# window_task_icon.sh 同源：bell=未读注意力（asking 或 completed 未 ack），
# watch(⏳)=有窗口实时 busy（名字 [B] 前缀）。**不**用 tracker 的 in_progress
# 直接当 ⏳——daemon cache 会残留已关闭窗口的 stale in_progress task，否则全 idle
# 仍误亮 ⏳；只统计当前 window 也避免 stale task 把 bell/watch 串到别的 session。
get_session_icon() {
  local sid="$1"
  local has_question=0 has_bell=0 has_watch=0 has_fail=0

  local question_pane
  question_pane=$(grep -F -m1 -x "${sid}::1" <<< "$question_state" || true)
  [[ -n "$question_pane" ]] && has_question=1

  local -a wids=()
  local line wid unread wfail watching rbell wname
  while IFS='|' read -r wid unread wfail watching rbell wname; do
    [[ -z "$wid" ]] && continue
    wids+=("$wid")
    [[ "$unread" == "1" ]] && has_bell=1
    [[ "$unread" == "1" && "$wfail" == "1" ]] && has_fail=1
    [[ "$rbell" == "1" ]] && has_bell=1
    [[ "$watching" == "1" ]] && has_watch=1
    # 实时 busy：窗口名 [B] 前缀（sync-names 每 3 秒写），全 idle 时自然消失
    [[ "$wname" == '[B]'* ]] && has_watch=1
  done < <(tmux list-windows -t "$sid" \
    -F '#{window_id}|#{@unread}|#{@watch_failed}|#{@watching}|#{@agent_remote_bell}|#{window_name}' 2>/dev/null || true)

  if [[ -n "$tracker_state" && ${#wids[@]} -gt 0 ]]; then
    local wids_json bell
    wids_json=$(printf '%s\n' "${wids[@]}" | jq -R . | jq -s . 2>/dev/null || echo '[]')
    bell=$(echo "$tracker_state" | jq -r --argjson w "$wids_json" '
      .tasks // [] | .[]
      | select(.window_id as $x | $w | index($x))
      | select((.acknowledged != true) and (.asking == true or .status == "completed"))
      | "bell"
    ' 2>/dev/null | head -1 || true)
    [[ -n "$bell" ]] && has_bell=1
  fi

  if (( has_question )); then
    printf '❓'
  elif (( has_fail )); then
    printf '❌'
  elif (( has_bell )); then
    printf '🔔'
  elif (( has_watch )); then
    printf '⏳'
  fi
}

rendered=""
prev_bg=""
current_session_id_norm=$(normalize_session_id "$current_session_id")
while IFS= read -r entry; do
  [[ -z "$entry" ]] && continue
  session_id="${entry%%::*}"
  name="${entry#*::}"
  [[ -z "$session_id" ]] && continue

  session_id_norm=$(normalize_session_id "$session_id")
  segment_bg="$inactive_bg"
  segment_fg="$inactive_fg"
  trimmed_name=$(trim_label "$name")
  is_current=0
  if [[ "$session_id" == "$current_session_id" || "$session_id_norm" == "$current_session_id_norm" ]]; then
    is_current=1
    segment_bg="$active_bg"
    segment_fg="$active_fg"
  fi

  if (( is_narrow == 1 )); then
    if (( is_current == 1 )); then
      label="$trimmed_name"
    else
      idx=$(extract_index "$name")
      if [[ -n "$idx" ]]; then
        label="$idx"
      else
        label="$trimmed_name"
      fi
    fi
  else
    label="$trimmed_name"
  fi
  if (( ${#label} > max_width )); then
    label="${label:0:max_width-1}…"
  fi

  task_icon=$(get_session_icon "$session_id")

  # 用 range=user 把整段标成可点击区域，值带 SESS: 前缀 + session_id，
  # 供 tmux.conf 的 MouseDown1Status 绑定识别并 switch-client。
  # range 值只放 session_id 的数字部分（去掉 $），避免 $ 进 range；
  # 绑定侧再补回 $ 还原成 $N 目标。
  rendered+="#[range=user|SESS${session_id#\$}]"
  if [[ -z "$prev_bg" ]]; then
    rendered+="#[fg=${segment_bg},bg=${status_bg}]${left_cap}"
  else
    rendered+="#[fg=${prev_bg},bg=${segment_bg}]${separator}"
  fi
  rendered+="#[fg=${segment_fg},bg=${segment_bg}] ${label}${task_icon} #[norange]"
  prev_bg="$segment_bg"
done <<< "$sessions"

if [[ -n "$prev_bg" ]]; then
  rendered+="#[fg=${prev_bg},bg=${status_bg}]${separator}"
fi

printf '%s' "$rendered"
