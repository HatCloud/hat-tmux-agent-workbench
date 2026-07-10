#!/usr/bin/env bash
set -euo pipefail

# 用 fzf 选一个 workspace 快照，把对应 manifest 的绝对路径打印到 stdout（无选择则空）。
# 供 tmux_resume.sh（终端原生）与 choose_workspace.sh（tmux popup）共用，
# 样式对齐 agent 启动器：中文 prompt/header + --reverse，并带 manifest 预览。
#
# 用法：manifest="$(workspace_snapshot_menu.sh)"

snapshot_dir="${HOME}/.hat-config/state/workspaces/snapshots"

if ! command -v fzf >/dev/null 2>&1; then
  echo "fzf is required to choose a workspace snapshot" >&2
  exit 2
fi
if [[ ! -d "$snapshot_dir" ]] || ! find "$snapshot_dir" -name '*.tsv' -print -quit | grep -q .; then
  echo "No workspace snapshots in ${snapshot_dir}" >&2
  exit 3
fi

# 把 20260620-001234.tsv → 2026-06-20 00:12
pretty_time() {
  local ts="${1%.tsv}" d="${1%-*}" t
  d="${ts%-*}"; t="${ts#*-}"
  printf '%s-%s-%s %s:%s' "${d:0:4}" "${d:4:2}" "${d:6:2}" "${t:0:2}" "${t:2:2}"
}

# 预览器：把 manifest 渲染成「窗口 / 名字 / 朝向 / repo」，↻ 标记可 --resume 的窗口。
# 作为子命令被 fzf 以 `{2}`（manifest 路径）调用。
if [[ "${1:-}" == "--preview" ]]; then
  awk -F '\t' '
    function shorten(s, max) { return (length(s) > max ? substr(s, 1, max - 1) "…" : s) }
    BEGIN { wins = 0; resumable = 0 }
    {
      wins++
      n = split($4, a, "/"); base = a[n]
      mark = ($6 == "" ? "·" : "↻")
      if ($6 != "") resumable++
      printf "%s  #%-2s  %s\n", mark, $2, shorten($3, 48)
      printf "        %s · %s\n", base, $5
    }
    END {
      print ""
      printf "%d 个窗口，%d 个可 --resume（↻）\n", wins, resumable
    }
  ' "${2:?manifest path required}"
  exit 0
fi

self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/workspace_snapshot_menu.sh"

selection="$(
  find "$snapshot_dir" -type f -name '*.tsv' -print \
    | sort -r \
    | while IFS= read -r file; do
        base="$(basename "$file")"
        count="$(grep -c . "$file" 2>/dev/null || echo 0)"
        printf '%s · %s 个窗口\t%s\n' "$(pretty_time "$base")" "$count" "$file"
      done \
    | fzf \
        --prompt='恢复哪个快照 > ' \
        --header='j/k 选择，回车恢复，Esc 取消（最上面是最新）' \
        --reverse --height=80% --ansi --cycle \
        --disabled --bind='j:down,k:up' \
        --delimiter='\t' --with-nth=1 \
        --preview="$self --preview {2}" \
        --preview-window='down,55%,border-top,wrap'
)"

[[ -z "$selection" ]] && exit 0
printf '%s\n' "$selection" | awk -F '\t' '{ print $2 }'
