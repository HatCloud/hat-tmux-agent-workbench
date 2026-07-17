#!/usr/bin/env bash
set -euo pipefail

# prefix u：抓当前 pane 可见区 + 近 200 行回滚里的 URL / 文件 / 文件夹。两段式：
#   extract（本入口，经 run-shell 调用、无弹窗）——没命中只 display-message
#   提示、不弹窗；有命中落临时文件后再开 display-popup 进 pick 段。
#   pick（--pick <file>，在弹窗内跑）——fzf 选择，键位随类型分工：
#     Enter  默认打开：URL→浏览器 / 文件→VS Code(-g 跳行) / 文件夹→VS Code
#     Ctrl-o 其他方式：文件→系统 open / 文件夹→Finder（URL 无）
#     Ctrl-y 复制路径/URL（三类通用）
# 文件/文件夹检测刻意宽松（含 / 的 token、name.ext、裸词如 ls 输出的目录名，
# 均可带 :line[:col] 尾），相对路径以 pane 的 current_path 为基准解析，最后
# 只保留 test -e 磁盘上真实存在的项兜底误报；/dev /proc /sys 下的路径
# （/dev/null 这类命令行常客）虽存在但无打开价值，直接排除。
# 机制同 tmux-fzf-url（capture-pane + 正则 + fzf + open），自管实现免第三方
# 依赖；鼠标路径走 Ghostty 的 Shift+Cmd+Click（tmux mouse on 下的终端惯例）。
# 注意：TUI pane（Claude Code 等 alternate screen）没有 tmux 历史行，只能抓到
# 当前可视区；滚出 TUI 视口的内容抓不到，这是 alternate screen 的边界。
# 用法: open_urls.sh <pane_id>
#     | open_urls.sh --pick <items_file>
#     | open_urls.sh --extract <base_dir> < text   （测试入口，items 出 stdout）

# code CLI 与 open 在 tmux popup 里 PATH 可能缺 homebrew，补齐兜底。
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"

script_path="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"

# 每条记录四字段（TAB 分隔）：kind \t display \t payload \t root
#   kind    ∈ url / file / dir
#   display fzf 展示列：emoji + 解析后路径（base 之下显示相对 base 的路径，
#           之外显示绝对全称；URL 为原文）
#   payload url 原文；file 为 绝对路径[:line[:col]]；dir 为绝对路径
#   root    file 且位于解析根（pane cwd）之下时 = 该根目录，VS Code 用它作
#           工作区实现「在项目中打开」；其余（url/dir/根外文件）为空

open_vscode() {  # $1=path[:line:col] $2=root（可空）。带 root 时先开工作区再 -g 跳行
  local target="$1" root="${2:-}"
  if command -v code >/dev/null 2>&1; then
    if [[ -n "$root" ]]; then
      code -g "$root" "$target" >/dev/null 2>&1 || code "$root" "$target" >/dev/null 2>&1
    else
      code -g "$target" >/dev/null 2>&1 || code "$target" >/dev/null 2>&1
    fi
  else
    open -a "Visual Studio Code" "${target%%:*}"
  fi
}

if [[ "${1:-}" == "--pick" ]]; then
  items_file="${2:?items file required}"
  trap 'rm -f "$items_file"' EXIT
  # items 文件在 extract 段已按「最近出现在前」排好，直接喂 fzf。
  selected="$(fzf --no-multi --reverse --delimiter='\t' --with-nth=2 \
    --prompt='open> ' \
    --header='Enter 默认打开 · Ctrl-o 其他方式(open/Finder) · Ctrl-y 复制 · Esc 取消' \
    --expect=ctrl-y,ctrl-o < "$items_file")" || exit 0
  key="$(head -1 <<< "$selected")"
  row="$(sed -n '2p' <<< "$selected")"
  [[ -z "$row" ]] && exit 0
  IFS=$'\t' read -r kind _ payload root <<< "$row"
  [[ -z "$payload" ]] && exit 0

  # 复制/系统打开用的纯路径（去掉 :line:col 尾）
  plain="$payload"
  [[ "$kind" == "file" ]] && plain="${payload%%:*}"

  case "$key" in
    ctrl-y)
      printf '%s' "$plain" | pbcopy
      tmux display-message "已复制: $plain"
      ;;
    ctrl-o)
      case "$kind" in
        file) open "$plain" ;;
        dir)  open "$payload" ;;   # Finder
        url)  ;;                    # URL 无「其他方式」，忽略
      esac
      ;;
    *)  # Enter：默认打开
      case "$kind" in
        url)  target="$payload"; [[ "$target" == www.* ]] && target="https://$target"; open "$target" ;;
        file|dir) open_vscode "$payload" "$root" ;;
      esac
      ;;
  esac
  exit 0
fi

# URL 提取。bracket 表达式按 POSIX 写：排除的 ] 放 [^ 后第一位、[ 不加反斜杠——
# BSD /usr/bin/grep 把 \[\] 当字面反斜杠、类提前闭合，整条 URL 分支永不匹配。
url_re='(https?|ftp|file)://[^] <>"()['"'"']+|www\.[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}[^] <>"()['"'"']*'

# 路径候选提取（宽松，靠后续 test -e 过滤误报）：
#   ① 有根路径（/ 开头，可带 ~ / . / .. 前缀）
#   ② 相对多段路径（word/word/…，无前缀；leftmost-longest 保证整条吃下，
#      不会被裸词分支拆成首段）
#   ③ 裸的带扩展名文件名（name.ext）
#   ④ 裸词（字母开头 ≥2 字符，接住 ls 输出里的目录名）——误报最多，全靠 -e 兜底
# 四支均可带 :line[:col] 尾。
path_re='(~|\.\.?)?/[A-Za-z0-9._~+/-]+(:[0-9]+){0,2}|[A-Za-z0-9._~+-]+(/[A-Za-z0-9._~+-]+)+(:[0-9]+){0,2}|[A-Za-z0-9._-]+\.[A-Za-z0-9]+(:[0-9]+){0,2}|[A-Za-z][A-Za-z0-9._-]+(:[0-9]+){0,2}'

# 从 $text/$base 提取 items（kind\tdisplay\tpayload\troot）写 stdout，最近出现在前。
# 文件夹条目默认不列（Settings → General → URL picker folders 可开）：裸词目录
# 误报多、prompt/命令行 token 常把文件夹顶到列表头部。OPEN_URLS_SHOW_DIRS 环境
# 变量可覆盖（on/off，测试用）。
extract_items() {
  local show_dirs="${OPEN_URLS_SHOW_DIRS:-}"
  if [[ -z "$show_dirs" ]]; then
    show_dirs="$("$HOME/.hat-config/agent-tracker/bin/agent" tmux url-picker-dirs 2>/dev/null || echo off)"
  fi
  # grep -n 保留行号，两类候选合并后按行号降序（最近在前）；-s 稳定排序保证
  # 同一行内保持从左到右原序。match 内的冒号属后续字段，-t: -k1 只看行号。
  {
    printf '%s\n' "$text" | grep -noE "$url_re" 2>/dev/null || true
    printf '%s\n' "$text" | grep -noE "$path_re" 2>/dev/null || true
  } | sort -s -t: -k1,1nr |
    # 先按 match 原文去重（保最近一次），避免同词反复 stat。
    awk '{k=substr($0, index($0,":")+1)} !seen[k]++' |
    while IFS= read -r line; do
      match="${line#*:}"
      case "$match" in
        http://*|https://*|ftp://*|file://*|www.*)
          url="$(sed -E 's/[.,;:!?]+$//' <<< "$match")"
          printf 'url\t🔗 %s\t%s\n' "$url" "$url"
          continue
          ;;
      esac

      # 路径候选：剥尾部 :line:col（先试 :l:c 再试 :l，贪婪 .+ 只会多吃不会少吃）、
      # 归一化 ./ 前缀、展开 ~、相对补 base，噪声目录排除后 test -e。
      core="$match"; linecol=""
      if [[ "$core" =~ ^(.+):([0-9]+):([0-9]+)$ ]]; then
        core="${BASH_REMATCH[1]}"; linecol="${BASH_REMATCH[2]}:${BASH_REMATCH[3]}"
      elif [[ "$core" =~ ^(.+):([0-9]+)$ ]]; then
        core="${BASH_REMATCH[1]}"; linecol="${BASH_REMATCH[2]}"
      fi
      core="${core#./}"
      case "$core" in
        "~"/*) abs="${HOME}/${core#\~/}" ;;
        /*)    abs="$core" ;;
        *)     abs="$base/$core" ;;
      esac
      case "$abs" in
        /dev|/dev/*|/proc|/proc/*|/sys|/sys/*) continue ;;
      esac
      [[ -e "$abs" ]] || continue
      # display：base 之下的显示成相对 base 的路径（项目内容一眼可读），
      # base 之外保持绝对全称。
      disp="$abs"
      case "$abs" in "$base"/*) disp="${abs#"$base"/}" ;; esac
      if [[ -d "$abs" ]]; then
        [[ "$show_dirs" == on ]] || continue
        printf 'dir\t📁 %s\t%s\t\n' "$disp" "$abs"
      else
        payload="$abs"; [[ -n "$linecol" ]] && { payload="$abs:$linecol"; disp="$disp:$linecol"; }
        root=""
        case "$abs" in "$base"/*) root="$base" ;; esac
        printf 'file\t📄 %s\t%s\t%s\n' "$disp" "$payload" "$root"
      fi
    done | awk -F'\t' '!seen[$3]++'  # 再按 payload 去重（./x 与 x 归一后同条）
}

if [[ "${1:-}" == "--extract" ]]; then
  base="${2:?base dir required}"
  text="$(cat)"
  extract_items
  exit 0
fi

pane_id="${1:?pane_id required}"
base="$(tmux display-message -p -t "$pane_id" '#{pane_current_path}' 2>/dev/null || true)"
[[ -z "$base" ]] && base="$PWD"

text="$(tmux capture-pane -p -J -S -200 -t "$pane_id" 2>/dev/null || true)"

items_file="$(mktemp "${TMPDIR:-/tmp}/agent_urls.XXXXXX")"
extract_items > "$items_file"

if [[ ! -s "$items_file" ]]; then
  rm -f "$items_file"
  tmux display-message "当前 pane 没有找到 URL / 文件 / 文件夹"
  exit 0
fi

exec tmux display-popup -E -w 90% -h 60% "$script_path --pick $items_file"
