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

# Session 图标 = 该 session 下 window 图标的聚合：🔔=任一窗口 @agent_icon 有铃
# （daemon reconcileWindowIcons 预计算，本地任务铃与远端透传铃同源收口在那），
# ⏳=任一窗口实时 busy（窗口名 [B] 前缀，sync-names 写；全 idle 自然消失）。
# 不再自行解析 tracker cache——图标真身归 daemon，一处计算多处引用。
get_session_icon() {
  local sid="$1"
  local has_bell=0 has_watch=0
  local icon wname
  while IFS='|' read -r icon wname; do
    [[ "$icon" == *🔔* ]] && has_bell=1
    [[ "$wname" == '[B]'* ]] && has_watch=1
  done < <(tmux list-windows -t "$sid" -F '#{@agent_icon}|#{window_name}' 2>/dev/null || true)

  if (( has_bell )); then
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
