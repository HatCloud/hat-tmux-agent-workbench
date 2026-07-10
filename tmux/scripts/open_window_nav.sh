#!/usr/bin/env bash
set -euo pipefail

window_id="${1-}"
path_value="${2-}"
session_name="${3-}"

_cw=$(tmux display-message -p '#{client_width}' 2>/dev/null || echo 200)
_ch=$(tmux display-message -p '#{client_height}' 2>/dev/null || echo 50)

# Width tier from the General → Window nav size setting (standard|wide|full, default wide).
# wide is the default and is roomy enough that the footer hint row fits on one line
# and the Name column keeps ample width after the trailing columns.
size="$(~/.hat-config/agent-tracker/bin/agent tmux window-nav-size 2>/dev/null \
  || jq -r '.window_nav_size // "wide"' ~/.config/agent-tracker/agent-config.json 2>/dev/null \
  || echo wide)"
case "$size" in
  standard) _raw=$(( _cw * 78 / 100 )); _cap=140 ;;
  full)     _raw=$(( _cw * 96 / 100 )); _cap=400 ;;
  *)        _raw=$(( _cw * 88 / 100 )); _cap=180 ;; # wide (default)
esac
popup_w=$(( _raw > _cap ? _cap : _raw ))

# 高度上限：竖屏 client_height 很大，铺满整屏太多，按档位 cap 行数。
case "$size" in
  full) _rawh=$(( _ch * 92 / 100 )); _caph=60 ;;
  *)    _rawh=$(( _ch * 85 / 100 )); _caph=50 ;;
esac
popup_h=$(( _rawh > _caph ? _caph : _rawh ))

# Pass the popup's INNER dimensions (minus the 1-col border on each side) so the
# panel sizes its columns correctly even if the popup's WindowSizeMsg is late/zero.
inner_w=$(( popup_w - 2 > 20 ? popup_w - 2 : popup_w ))
inner_h=$(( popup_h - 2 > 5 ? popup_h - 2 : popup_h ))

exec tmux display-popup -E -w "$popup_w" -h "$popup_h" -T " Windows " \
  ~/.hat-config/agent-tracker/bin/agent windows \
  --window="$window_id" \
  --path="$path_value" \
  --session-name="$session_name" \
  --width="$inner_w" \
  --height="$inner_h"
