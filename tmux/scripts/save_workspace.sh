#!/usr/bin/env bash
set -euo pipefail

# 存档当前所有 tmux workspace 的骨架清单（session/window/repo/layout）。
# 只记录结构、不记录运行进程；崩溃后由 restore_workspace.sh 重建。
#
# 用法：
#   save_workspace.sh           手动存档，弹 tmux 提示
#   save_workspace.sh --auto    供 launchd 定时器调用：静默、去重、保留最新 N 个
#   save_workspace.sh --stdout  只把当前 live manifest 打到 stdout，不落盘（供比对用）

auto=0
stdout=0
case "${1:-}" in
  --auto)   auto=1 ;;
  --stdout) stdout=1 ;;
esac

workspace_dir="${HOME}/.hat-config/state/workspaces"
snapshot_dir="${workspace_dir}/snapshots"
last_file="${workspace_dir}/last"
keep_recent=3

# --auto：tmux server 不在/无 window 时静默退出
if ! tmux list-windows -a >/dev/null 2>&1; then
  [[ "$auto" == "1" || "$stdout" == "1" ]] && exit 0
  tmux display-message "No tmux server to save" 2>/dev/null || true
  exit 0
fi

mkdir -p "$snapshot_dir"
tmp="$(mktemp "${snapshot_dir}/.save.XXXXXX")"
trap 'rm -f "$tmp"' EXIT

saved_count=0
skipped_single=0
skipped_non_git=0

# 注意：tmux 在 launchd 等上下文里会把 -F 格式中的 TAB sanitize 成 '_'，导致
# 制表符分隔的多字段解析整行错位。因此一律避免「单行 TAB 多字段」：要么每字段
# 各占一行（换行不会被 sanitize），要么只用无空格的安全字段配空格分隔 + 按
# pane_id 单独查询。

# 取 window 内最低 index 的 pane 的 pane_id（'#{pane_index} #{pane_id}' 两者均无空格）
lowest_pane() {
  tmux list-panes -t "$1" -F '#{pane_index} #{pane_id}' | sort -n | awk 'NR==1{print $2}'
}

# 探测三格朝向：第二个 pane 在 ai pane 右侧→landscape，下方→portrait
detect_layout() {
  local win="$1" ai_pane other_pane ai_left ai_top o_left o_top
  ai_pane="$(lowest_pane "$win")"
  other_pane="$(tmux list-panes -t "$win" -F '#{pane_index} #{pane_id}' | sort -n | awk 'NR==2{print $2}')"
  [[ -z "$other_pane" ]] && { printf 'landscape\n'; return; }
  ai_left="$(tmux display-message -p -t "$ai_pane" '#{pane_left}')"
  ai_top="$(tmux display-message -p -t "$ai_pane" '#{pane_top}')"
  o_left="$(tmux display-message -p -t "$other_pane" '#{pane_left}')"
  o_top="$(tmux display-message -p -t "$other_pane" '#{pane_top}')"
  if [[ -n "$o_left" && "$o_left" -gt "$ai_left" ]]; then
    printf 'landscape\n'
  elif [[ -n "$o_top" && "$o_top" -gt "$ai_top" ]]; then
    printf 'portrait\n'
  else
    printf 'landscape\n'
  fi
}

# 只枚举单字段 window_id（每行一个，记录尾换行存活），其余字段逐个 display-message 查询。
# 不在 -F 里放任何分隔符，彻底规避 launchd 下的控制字符 sanitize。
while IFS= read -r window_id; do
  [[ -z "$window_id" ]] && continue

  pane_count="$(tmux display-message -p -t "$window_id" '#{window_panes}' 2>/dev/null || true)"
  if [[ "$pane_count" == "1" ]]; then
    skipped_single=$((skipped_single + 1))
    continue
  fi

  ai_pane="$(lowest_pane "$window_id")"
  first_pane_path="$(tmux display-message -p -t "$ai_pane" '#{pane_current_path}' 2>/dev/null || true)"

  repo_root="$(git -C "$first_pane_path" rev-parse --show-toplevel 2>/dev/null || true)"
  if [[ -z "$repo_root" ]]; then
    skipped_non_git=$((skipped_non_git + 1))
    continue
  fi

  session_name="$(tmux display-message -p -t "$window_id" '#{session_name}')"
  window_index="$(tmux display-message -p -t "$window_id" '#{window_index}')"
  window_name="$(tmux display-message -p -t "$window_id" '#{window_name}')"
  # 剥掉开头的 [B]/[I]/[?] 等瞬时状态前缀，存干净基础名：
  # 否则状态每秒变动会让快照内容反复变化、--auto 去重失效，且恢复后残留旧状态。
  window_name="${window_name#\[*\] }"

  # 若该 ai pane 跑的是 Claude Code，取 claude_report.sh 落盘的 session id（第 6 列）。
  # cwd 守卫：映射里记的 cwd 与当前 ai pane cwd 不符则视为 pane id 被回收，丢弃。
  # map 文件是单行 `sid<TAB>cwd`，必须按 TAB 拆字段；早先误用 sed 行号会把整行
  # （含 cwd）当成 sid，污染第 6 列。
  claude_sid=""
  map_file="${workspace_dir}/claude-sessions/${ai_pane//[^A-Za-z0-9]/_}"
  if [[ -f "$map_file" ]]; then
    map_cwd=""
    IFS=$'\t' read -r claude_sid map_cwd < "$map_file" || true
    [[ -n "$map_cwd" && "$map_cwd" != "$first_pane_path" ]] && claude_sid=""
  fi

  printf '%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$session_name" \
    "$window_index" \
    "$window_name" \
    "$repo_root" \
    "$(detect_layout "$window_id")" \
    "$claude_sid" >> "$tmp"
  saved_count=$((saved_count + 1))
done < <(tmux list-windows -a -F '#{window_id}')

# 0 个合格 window：绝不写空快照（空文件若成为 last，恢复时什么都恢复不出）。
# 多发生在 launchd 环境连错 tmux socket、或当前确实没有 git 多 pane 窗口时。
if [[ "$saved_count" == "0" ]]; then
  if [[ "$auto" != "1" && "$stdout" != "1" ]]; then
    tmux display-message "No qualifying windows to save (${skipped_single} single-pane, ${skipped_non_git} non-git)"
  fi
  exit 0
fi

# --stdout：只输出当前 manifest，不落盘/不去重/不剪裁
if [[ "$stdout" == "1" ]]; then
  cat "$tmp"
  exit 0
fi

# --auto：内容与上次快照完全相同则跳过写入，避免无变化时堆积
if [[ "$auto" == "1" && -f "$last_file" ]]; then
  prev="$(cat "$last_file")"
  if [[ -f "$prev" ]] && cmp -s "$tmp" "$prev"; then
    exit 0
  fi
fi

timestamp="$(date '+%Y%m%d-%H%M%S')"
manifest="${snapshot_dir}/${timestamp}.tsv"
mv "$tmp" "$manifest"
trap - EXIT
printf '%s\n' "$manifest" > "$last_file"

# 保留最新 keep_recent 个快照
ls -1t "${snapshot_dir}"/*.tsv 2>/dev/null | tail -n +$((keep_recent + 1)) | while IFS= read -r old; do
  rm -f "$old"
done

if [[ "$auto" != "1" ]]; then
  tmux display-message "Saved ${saved_count} workspace windows to $(basename "$manifest") (${skipped_single} single-pane, ${skipped_non_git} non-git skipped)"
fi
