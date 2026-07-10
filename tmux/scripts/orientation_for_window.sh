#!/usr/bin/env bash
set -euo pipefail

# 按窗口宽高比输出该用 landscape 还是 portrait（无滞回硬判定）。
# 终端 cell 高 ≈ 宽的 2 倍，故视觉正方形 ≈ window_width == 2*window_height。
# 阈值取 2.0：width*10 >= height*20 视为横向。
# 用法: orientation_for_window.sh <window_id>

window_id="${1:?window_id required}"

dims="$(tmux display-message -p -t "$window_id" '#{window_width} #{window_height}')"
w="${dims% *}"
h="${dims#* }"

if [[ -z "$w" || -z "$h" || "$h" -le 0 ]]; then
  echo landscape
  exit 0
fi

if (( w * 10 >= h * 20 )); then
  echo landscape
else
  echo portrait
fi
