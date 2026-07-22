#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
test_root="$(mktemp -d /tmp/hat-agent-layout.XXXXXX)"
export TMUX_TMPDIR="$test_root/tmux"
export HOME="$test_root/home"
export LAZYGIT_ARGS_FILE="$test_root/lazygit-args"
mkdir -p "$TMUX_TMPDIR" "$HOME/.hat-config/agent-tracker/bin" "$test_root/bin"
tmux_socket_dir="$TMUX_TMPDIR/tmux-$(id -u)"

# A test launched from inside tmux inherits the real server socket through
# TMUX. Clear it before the first tmux command so TMUX_TMPDIR is authoritative.
unset TMUX TMUX_PANE

cleanup() {
  if [[ -d "$tmux_socket_dir" ]]; then
    local socket
    for socket in "$tmux_socket_dir"/*; do
      [[ -S "$socket" ]] && tmux -S "$socket" kill-server >/dev/null 2>&1 || true
    done
  fi
  rm -rf "$test_root"
}
trap cleanup EXIT

cat >"$HOME/.hat-config/agent-tracker/bin/agent" <<'EOF'
#!/usr/bin/env bash
case "${2:-}" in
  layout-default) printf 'landscape\n' ;;
  layout-main-percent) printf '%s\n' "${LAYOUT_MAIN_PERCENT:-55}" ;;
  layout-third-pane) printf '%s\n' "${LAYOUT_THIRD_PANE:-false}" ;;
  layout-side-top-percent) printf '%s\n' "${LAYOUT_SIDE_TOP_PERCENT:-75}" ;;
  status-position) printf 'bottom\n' ;;
esac
EOF
chmod +x "$HOME/.hat-config/agent-tracker/bin/agent"

cat >"$test_root/bin/lazygit" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$#" >"$LAZYGIT_ARGS_FILE"
EOF
chmod +x "$test_root/bin/lazygit"
export PATH="$test_root/bin:$PATH"

window_id="$(tmux -f /dev/null new-session -d -P -F '#{window_id}' -x 200 -y 50 -c "$root")"
"$root/tmux/scripts/build_agent_layout.sh" "$window_id" "$root"

pane_count="$(tmux display-message -p -t "$window_id" '#{window_panes}')"
[[ "$pane_count" == "2" ]] || { echo "FAIL: expected 2 panes, got $pane_count"; exit 1; }

roles="$(tmux list-panes -t "$window_id" -F '#{@agent_pane_role}' | sort | tr '\n' ' ')"
[[ "$roles" == "ai git " ]] || { echo "FAIL: expected ai/git roles, got $roles"; exit 1; }
[[ "$(tmux show-options -vwq -t "$window_id" @agent_orientation_mode)" == "landscape" ]] || {
  echo "FAIL: expected default orientation mode to be landscape"
  exit 1
}

window_width="$(tmux display-message -p -t "$window_id" '#{window_width}')"
git_width="$(tmux list-panes -t "$window_id" -F '#{@agent_pane_role}|#{pane_width}' | awk -F'|' '$1 == "git" {print $2}')"
git_percent=$((git_width * 100 / window_width))
(( git_percent >= 43 && git_percent <= 47 )) || {
  echo "FAIL: expected git pane near 45%, got ${git_percent}%"
  exit 1
}

for _ in {1..20}; do
  [[ -f "$LAZYGIT_ARGS_FILE" ]] && break
  sleep 0.05
done
[[ "$(cat "$LAZYGIT_ARGS_FILE")" == "0" ]] || {
  echo "FAIL: expected lazygit without arguments"
  exit 1
}

"$root/tmux/scripts/reflow_agent_layout.sh" "$window_id" portrait
[[ "$(tmux display-message -p -t "$window_id" '#{window_panes}')" == "2" ]] || {
  echo "FAIL: portrait reflow changed pane count"
  exit 1
}
[[ "$(tmux show-options -vwq -t "$window_id" @agent_orientation)" == "portrait" ]] || {
  echo "FAIL: portrait reflow did not update orientation"
  exit 1
}

window_height="$(tmux display-message -p -t "$window_id" '#{window_height}')"
git_height="$(tmux list-panes -t "$window_id" -F '#{@agent_pane_role}|#{pane_height}' | awk -F'|' '$1 == "git" {print $2}')"
git_percent=$((git_height * 100 / window_height))
(( git_percent >= 43 && git_percent <= 47 )) || {
  echo "FAIL: expected portrait git pane near 45%, got ${git_percent}%"
  exit 1
}

# An opt-in third pane keeps the configured main split and stacks run below git.
export LAYOUT_MAIN_PERCENT=60
export LAYOUT_THIRD_PANE=true
export LAYOUT_SIDE_TOP_PERCENT=75
three_window_id="$(tmux new-window -d -P -F '#{window_id}' -t ':')"
"$root/tmux/scripts/build_agent_layout.sh" "$three_window_id" "$root"

[[ "$(tmux display-message -p -t "$three_window_id" '#{window_panes}')" == "3" ]] || {
  echo "FAIL: expected opt-in 3-pane layout"
  exit 1
}
three_roles="$(tmux list-panes -t "$three_window_id" -F '#{@agent_pane_role}' | sort | tr '\n' ' ')"
[[ "$three_roles" == "ai git run " ]] || {
  echo "FAIL: expected ai/git/run roles, got $three_roles"
  exit 1
}

three_window_width="$(tmux display-message -p -t "$three_window_id" '#{window_width}')"
three_git_width="$(tmux list-panes -t "$three_window_id" -F '#{@agent_pane_role}|#{pane_width}' | awk -F'|' '$1 == "git" {print $2}')"
three_git_percent=$((three_git_width * 100 / three_window_width))
(( three_git_percent >= 38 && three_git_percent <= 42 )) || {
  echo "FAIL: expected configured side near 40%, got ${three_git_percent}%"
  exit 1
}

git_height="$(tmux list-panes -t "$three_window_id" -F '#{@agent_pane_role}|#{pane_height}' | awk -F'|' '$1 == "git" {print $2}')"
run_height="$(tmux list-panes -t "$three_window_id" -F '#{@agent_pane_role}|#{pane_height}' | awk -F'|' '$1 == "run" {print $2}')"
side_height=$((git_height + run_height + 1))
git_percent=$((git_height * 100 / side_height))
(( git_percent >= 72 && git_percent <= 78 )) || {
  echo "FAIL: expected git above run near 75%, got ${git_percent}%"
  exit 1
}

"$root/tmux/scripts/reflow_agent_layout.sh" "$three_window_id" portrait
[[ "$(tmux display-message -p -t "$three_window_id" '#{window_panes}')" == "3" ]] || {
  echo "FAIL: portrait reflow changed 3-pane count"
  exit 1
}
[[ "$(tmux show-options -vwq -t "$three_window_id" @agent_orientation)" == "portrait" ]] || {
  echo "FAIL: 3-pane portrait reflow did not update orientation"
  exit 1
}

echo "PASS agent_layout_test"
