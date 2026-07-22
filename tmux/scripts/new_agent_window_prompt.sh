#!/usr/bin/env bash
set -euo pipefail

# prefix [ 入口（及 prefix ] 的 OFF 直建路径）：按设置在指定目录新建 2/3-pane agent 窗口。
#   mode=here：直接用当前目录建，无任何输入（prefix ]，New agent prompt=OFF）。
#   mode=ask ：在 display-popup 里用 fzf 从 z 的目录历史（~/.z）里选：
#              输入关键字即过滤（frecency 排序，常用目录在前），Enter/Tab 选中高亮项；
#              没有匹配时回车直接把输入当路径用；fzf 不可用时回退 read -e 手输。
# 两种 mode 名字都走默认（空），交给 agent-tracker 自动命名。
# prefix ] 的 ON 路径（先输标题）改由 tmux 原生 command-prompt 直接调 new_agent_window.sh。
#
# 用法：new_agent_window_prompt.sh <here|ask> <current_path>

mode="${1:?mode required}"
cur="${2:-$PWD}"
scripts_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# z (rupa/z) 历史：~/.z 每行 path|rank|timestamp，按 z.sh 同款 frecency 公式排序，
# 过滤已删除的目录；当前目录另行置顶，这里跳过避免重复。
z_candidates() {
  local zdata="${_Z_DATA:-$HOME/.z}"
  [[ -r "$zdata" ]] || return 0
  awk -F'|' -v now="$(date +%s)" '
    NF >= 3 && $2 > 0 {
      dx = now - $3
      printf "%d\t%s\n", 10000 * $2 * (3.75 / ((0.0001 * dx + 1) + 0.25)), $1
    }' "$zdata" \
    | sort -t "$(printf '\t')" -k1,1 -rn \
    | cut -f2- \
    | while IFS= read -r d; do
        if [[ "$d" != "$cur" && -d "$d" ]]; then
          printf '%s\n' "$d"
        fi
      done
  return 0
}

dir="$cur"
if [[ "$mode" == "ask" ]]; then
  if command -v fzf >/dev/null 2>&1; then
    # --print-query：正常选中(0)输出「输入行+选中行」取末行；无匹配回车(1)只剩输入行，
    # 把输入直接当路径；Esc/Ctrl-C(130) 取消（弹窗随 exit 0 关闭）。
    # --tiebreak=index 让同分匹配保持 frecency 序。
    # 注意：header 里 ${cur} 的花括号不能省——bash 3.2 对紧跟变量名的多字节字符
    # （全角括号）会并入变量名解析，set -u 下直接 unbound variable。
    # 候选管道整体静默 stderr：fzf 提前退出时残余 printf 的 Broken pipe 报错不进弹窗。
    rc=0
    out="$({ printf '%s\n' "$cur"; z_candidates; } 2>/dev/null | fzf \
      --prompt='目录 > ' \
      --header="输入关键字过滤 z 历史，Enter/Tab 选中；无匹配则回车用所输路径（当前: ${cur}）" \
      --reverse --cycle \
      --print-query \
      --tiebreak=index \
      --bind='tab:accept,esc:abort')" || rc=$?
    case "$rc" in
      0|1) dir="$(printf '%s\n' "$out" | tail -n 1)" ;;
      *) exit 0 ;;
    esac
  else
    read -e -p "Dir [$cur]: " dir || exit 0
  fi
  dir="${dir:-$cur}"
fi

# 展开开头的 ~，去掉首尾空白
dir="${dir/#\~/$HOME}"
dir="${dir#"${dir%%[![:space:]]*}"}"
dir="${dir%"${dir##*[![:space:]]}"}"
[[ -z "$dir" ]] && dir="$cur"

exec "$scripts_dir/new_agent_window.sh" "$dir" ""
