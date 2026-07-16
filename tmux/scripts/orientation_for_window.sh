#!/usr/bin/env bash
set -euo pipefail

# 按窗口宽高比输出该用 landscape 还是 portrait（无滞回硬判定）。
# 终端 cell 高 ≈ 宽的 2 倍，故视觉正方形 ≈ window_width == 2*window_height。
# 阈值取 2.0：width*10 >= height*20 视为横向。
# Go 侧 claude_session.go 的 desiredOrientation 共享这条物理假设，但阈值
# 刻意不同（运行时 22/18 滞回带防抖 vs 这里建窗一次性硬判）——改假设两处
# 都改，别把数值统一（见 docs/audit 2026-07-15 I-7）。
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
