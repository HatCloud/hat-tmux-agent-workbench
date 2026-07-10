#!/usr/bin/env bash
set -euo pipefail

window_id="${1-}"
path_value="${2-}"
session_name="${3-}"

_cw=$(tmux display-message -p '#{client_width}' 2>/dev/null || echo 200)
_raw=$(( _cw * 78 / 100 ))
popup_w=$(( _raw > 130 ? 130 : _raw ))

_ch=$(tmux display-message -p '#{client_height}' 2>/dev/null || echo 50)
_rawh=$(( _ch * 80 / 100 ))
popup_h=$(( _rawh > 45 ? 45 : _rawh ))

exec tmux display-popup -E -w "$popup_w" -h "$popup_h" -T " Timers " \
  ~/.hat-config/agent-tracker/bin/agent window-timer \
  --window="$window_id" \
  --path="$path_value" \
  --session-name="$session_name"
