#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMUX_CONF="${HOME}/.tmux.conf"
ACTION=""
ASSUME_YES=0
RELOAD_TMUX=1
REMOVE_STATE=0
KEEP_STATE=0
MINI_HOST="${HAT_CONFIG_MINI_HOST:-mini}"
SKIP_MINI_SYNC="${HAT_CONFIG_SKIP_MINI_SYNC:-0}"

# Per-step install/uninstall gates (default: do everything). Each maps to one
# side of the setup wizard's intrusion disclosure; uninstall honours them
# symmetrically.
SKIP_TMUX=0        # managed tmux block + reload
SKIP_DAEMON=0      # agent-tracker launchd daemon
SKIP_WS_TIMER=0    # workspace auto-save launchd timer
SKIP_STOP_HOOK=0   # Claude Stop hook (settings.json)
SKIP_STATUSLINE=0  # Claude statusLine registration (settings.json)
SKIP_ALIAS=0       # agent / tmux-resume shell aliases

START_MARKER="# >>> hat-config managed tmux"
END_MARKER="# <<< hat-config managed tmux"
SOURCE_LINE="source-file ${REPO_DIR}/tmux/tmux.conf"

# agent-tracker (Go daemon) layout — kept under ~/.hat-config to match
# internal/paths/paths.go (binary + runtime state both live there).
HAT_CONFIG_DIR="${HOME}/.hat-config"
AGENT_TRACKER_DIR="${HAT_CONFIG_DIR}/agent-tracker"
BIN_DIR="${AGENT_TRACKER_DIR}/bin"
STATE_DIR="${HAT_CONFIG_DIR}/state/agent-tracker"
PLIST_LABEL="app.hat-tmux-workbench.agent-tracker"
PLIST_TEMPLATE="${AGENT_TRACKER_DIR}/${PLIST_LABEL}.plist.tmpl"
PLIST_DEST="${HOME}/Library/LaunchAgents/${PLIST_LABEL}.plist"

# workspace 周期自动存档定时器（launchd StartInterval）
WS_STATE_DIR="${HAT_CONFIG_DIR}/state/workspaces"
WS_SAVE_SCRIPT="${REPO_DIR}/tmux/scripts/save_workspace.sh"
WS_PLIST_LABEL="app.hat-tmux-workbench.workspace-save"
WS_PLIST_TEMPLATE="${AGENT_TRACKER_DIR}/${WS_PLIST_LABEL}.plist.tmpl"
WS_PLIST_DEST="${HOME}/Library/LaunchAgents/${WS_PLIST_LABEL}.plist"

# Legacy labels — retained ONLY for the one-time label-neutralisation migration
# (bootout old → bootstrap new, with backup/rollback). Never used as the active
# label anywhere else.
OLD_PLIST_LABEL="me.hatcloud.agent-tracker"
OLD_PLIST_DEST="${HOME}/Library/LaunchAgents/${OLD_PLIST_LABEL}.plist"
OLD_WS_PLIST_LABEL="me.hatcloud.workspace-save"
OLD_WS_PLIST_DEST="${HOME}/Library/LaunchAgents/${OLD_WS_PLIST_LABEL}.plist"

# Backup dir for the pre-migration plists (under runtime state/).
BACKUP_DIR="${STATE_DIR}/backup"

# Claude Code integration: Stop hook + statusLine (settings.json). The
# tracker-mcp MCP server is no longer registered here (planned debt: state now
# comes from the daemon's sessions-json poll, MCP was redundant).
CLAUDE_SETTINGS="${HOME}/.claude/settings.json"
CLAUDE_REPORT_SH="${REPO_DIR}/tmux/scripts/claude_report.sh"
# Claude statusLine registration reads statusline_chain out of the daemon config.
AGENT_CONFIG_JSON="${HOME}/.config/agent-tracker/agent-config.json"
STATUSLINE_SCRIPT="${REPO_DIR}/tmux/scripts/claude_statusline.sh"

usage() {
  cat <<EOF
Usage:
  $(basename "$0") [install|update|uninstall|status] [options]

Options:
  --tmux-conf PATH   Target tmux config file. Default: ~/.tmux.conf
  --yes, -y          Do not prompt for confirmation
  --no-reload        Do not reload running tmux servers
  --remove-state     Remove ~/.hat-config/state during uninstall
  --keep-state       Keep ~/.hat-config/state during uninstall
  --skip-mini-sync   Do not sync/deploy to the SSH host "mini"
  --help, -h         Show this help

Per-step skip flags (apply to both install/update and uninstall, symmetric):
  --skip-tmux        Skip the managed tmux block + reload
  --skip-daemon      Skip the agent-tracker launchd daemon
  --skip-ws-timer    Skip the workspace auto-save launchd timer
  --skip-stop-hook   Skip the Claude Stop hook (settings.json)
  --skip-statusline  Skip the Claude statusLine registration (settings.json)
  --skip-alias       Skip the agent / tmux-resume shell aliases

With no action, the script runs in interactive mode.
Install and update use the same idempotent deployment path.
EOF
}

log() {
  printf '%s\n' "$*"
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

confirm() {
  local prompt="$1"
  if [[ "$ASSUME_YES" -eq 1 ]]; then
    return 0
  fi

  local answer
  read -r -p "${prompt} [y/N] " answer
  case "$answer" in
    y|Y|yes|YES) return 0 ;;
    *) return 1 ;;
  esac
}

choose_action() {
  cat <<EOF
Hat Config deploy

1) Install / update
2) Uninstall
3) Status
4) Quit
EOF

  local choice
  read -r -p "Choose an action: " choice
  case "$choice" in
    1|install|update) ACTION="install" ;;
    2|uninstall) ACTION="uninstall" ;;
    3|status) ACTION="status" ;;
    4|q|quit|exit) exit 0 ;;
    *) die "unknown choice: $choice" ;;
  esac
}

parse_args() {
  while [[ "$#" -gt 0 ]]; do
    case "$1" in
      install|update|uninstall|status)
        ACTION="$1"
        ;;
      --tmux-conf)
        shift
        [[ "$#" -gt 0 ]] || die "--tmux-conf requires a path"
        TMUX_CONF="$1"
        ;;
      --yes|-y)
        ASSUME_YES=1
        ;;
      --no-reload)
        RELOAD_TMUX=0
        ;;
      --remove-state)
        REMOVE_STATE=1
        KEEP_STATE=0
        ;;
      --keep-state)
        KEEP_STATE=1
        REMOVE_STATE=0
        ;;
      --skip-mini-sync)
        SKIP_MINI_SYNC=1
        ;;
      --skip-tmux)
        SKIP_TMUX=1
        ;;
      --skip-daemon)
        SKIP_DAEMON=1
        ;;
      --skip-ws-timer)
        SKIP_WS_TIMER=1
        ;;
      --skip-stop-hook)
        SKIP_STOP_HOOK=1
        ;;
      --skip-statusline)
        SKIP_STATUSLINE=1
        ;;
      --skip-alias)
        SKIP_ALIAS=1
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      *)
        die "unknown argument: $1"
        ;;
    esac
    shift
  done
}

ensure_runtime() {
  [[ -f "${REPO_DIR}/tmux/tmux.conf" ]] || die "missing tmux/tmux.conf"
  # Core scripts referenced by tmux.conf / the agent launcher — a missing one
  # would otherwise surface only as a silently-skipped tmux hook at runtime.
  local required=(
    tmux/scripts/open_vscode_project.sh
    tmux/scripts/new_agent_window.sh
    tmux/scripts/agent
    tmux/scripts/claude_report.sh
    tmux/scripts/session_manager.py
    tmux/scripts/build_agent_layout.sh
    tmux/scripts/save_workspace.sh
    tmux/scripts/restore_workspace.sh
    tmux/scripts/choose_workspace.sh
    tmux/scripts/tmux_resume.sh
    tmux/scripts/workspace_snapshot_menu.sh
    tmux/tmux-status/tracker_cache.sh
  )
  local f
  for f in "${required[@]}"; do
    [[ -e "${REPO_DIR}/${f}" ]] || die "missing ${f}"
  done

  # Make every deployed script executable (tmux hooks gate on `test -x`).
  chmod +x \
    "${REPO_DIR}"/tmux/scripts/*.sh "${REPO_DIR}"/tmux/scripts/agent \
    "${REPO_DIR}"/tmux/scripts/*.py \
    "${REPO_DIR}"/tmux/tmux-status/*.sh "${REPO_DIR}"/tmux/tmux-status/*.py \
    2>/dev/null || true
}

strip_managed_block() {
  local file="$1"
  if [[ ! -f "$file" ]]; then
    return 0
  fi

  awk -v start="$START_MARKER" -v end="$END_MARKER" '
    $0 == start { skip = 1; next }
    $0 == end { skip = 0; next }
    skip != 1 { print }
  ' "$file"
}

write_managed_block() {
  local file="$1"
  local dir
  dir="$(dirname "$file")"
  mkdir -p "$dir"

  local tmp
  tmp="$(mktemp)"

  strip_managed_block "$file" > "$tmp"
  if [[ -s "$tmp" ]] && [[ "$(tail -c 1 "$tmp")" != "" ]]; then
    printf '\n' >> "$tmp"
  fi
  cat >> "$tmp" <<EOF
${START_MARKER}
${SOURCE_LINE}
${END_MARKER}
EOF

  mv "$tmp" "$file"
}

remove_managed_block() {
  local file="$1"
  [[ -f "$file" ]] || return 0

  local tmp
  tmp="$(mktemp)"
  strip_managed_block "$file" > "$tmp"
  mv "$tmp" "$file"
}

reload_tmux() {
  [[ "$RELOAD_TMUX" -eq 1 ]] || return 0
  command -v tmux >/dev/null 2>&1 || return 0

  if tmux list-sessions >/dev/null 2>&1; then
    tmux source-file "$TMUX_CONF" >/dev/null 2>&1 || log "warning: failed to reload ${TMUX_CONF}"
  fi
}

cleanup_active_tmux_keys() {
  [[ "$RELOAD_TMUX" -eq 1 ]] || return 0
  command -v tmux >/dev/null 2>&1 || return 0

  if tmux list-sessions >/dev/null 2>&1; then
    for key in A S T X v u d h j k l C-a C-s C-t C-x C-v C-u C-d C-h C-j C-k C-l; do
      tmux unbind-key "$key" >/dev/null 2>&1 || true
    done
    for key in M-C M-u M-d M-h M-j M-k M-l ˙ ∆ ˚ ¬ ¨ ∂; do
      tmux unbind-key -n "$key" >/dev/null 2>&1 || true
    done
    tmux set-option -gu prefix2 >/dev/null 2>&1 || true
    tmux set-option -gu status-left >/dev/null 2>&1 || true
    tmux set-option -gu mouse >/dev/null 2>&1 || true
    tmux set-option -gu pane-border-status >/dev/null 2>&1 || true
    tmux set-option -gu pane-border-style >/dev/null 2>&1 || true
    tmux set-option -gu pane-active-border-style >/dev/null 2>&1 || true
    tmux set-option -gu pane-border-format >/dev/null 2>&1 || true
    tmux set-window-option -gu mode-keys >/dev/null 2>&1 || true
    tmux source-file "$TMUX_CONF" >/dev/null 2>&1 || true
  fi
}

preflight() {
  log "Preflight dependency check:"

  command -v go >/dev/null 2>&1 || die "missing dependency: go (install from https://go.dev/dl/)"
  log "  go:               detected $(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//') / required present"

  command -v fzf >/dev/null 2>&1 || die "missing dependency: fzf (install from https://github.com/junegunn/fzf)"
  log "  fzf:              detected $(fzf --version 2>/dev/null | awk '{print $1}') / required present"

  command -v jq >/dev/null 2>&1 || die "missing dependency: jq (install from https://jqlang.github.io/jq/)"
  log "  jq:               detected $(jq --version 2>/dev/null) / required present"

  command -v tmux >/dev/null 2>&1 || die "missing dependency: tmux (>= 3.3)"
  # tmux -V prints e.g. "tmux 3.3a" / "tmux 3.4" — strip a trailing letter suffix.
  local tmux_ver tmux_major tmux_minor tmux_rest
  tmux_ver="$(tmux -V 2>/dev/null | awk '{print $2}')"
  tmux_major="${tmux_ver%%.*}"
  tmux_rest="${tmux_ver#*.}"
  tmux_minor="${tmux_rest%%[!0-9]*}"
  log "  tmux:             detected ${tmux_ver:-unknown} / required >= 3.3"
  [[ "$tmux_major" =~ ^[0-9]+$ && "$tmux_minor" =~ ^[0-9]+$ ]] \
    || die "cannot parse tmux version: ${tmux_ver:-unknown} (require >= 3.3)"
  if (( tmux_major < 3 || (tmux_major == 3 && tmux_minor < 3) )); then
    die "tmux ${tmux_ver} is too old; require >= 3.3"
  fi

  # Optional (degraded, not fatal): warn but continue when absent.
  if command -v terminal-notifier >/dev/null 2>&1; then
    log "  terminal-notifier: detected / optional"
  else
    log "  warning: terminal-notifier not found — desktop notifications disabled (degraded)"
  fi
  if command -v z >/dev/null 2>&1; then
    log "  z:                detected / optional"
  else
    log "  warning: z not found — directory-history jump unavailable (degraded)"
  fi
}

build_binaries() {
  log "Building agent-tracker binaries..."
  (
    cd "$AGENT_TRACKER_DIR" \
      && go build -o bin/tracker-server ./cmd/tracker-server \
      && go build -o bin/tracker-mcp ./cmd/tracker-mcp \
      && go build -o bin/agent ./cmd/agent
  ) || die "go build failed in ${AGENT_TRACKER_DIR}"
  log "Built tracker-server / tracker-mcp / agent into ${BIN_DIR}"
}

# daemon_health_check: wait (up to ~6s) for the tracker daemon to reach the
# launchd "running" state, then — if the agent CLI is built — confirm the state
# socket actually answers. Returns 0 healthy, 1 otherwise. launchd happily loads
# a plist whose program crashes on launch, so "running" alone is necessary but
# a live socket probe is the real signal.
daemon_health_check() {
  local deadline dstate
  deadline=$(( $(date +%s) + 6 ))
  while :; do
    dstate="$(launchctl print "gui/${UID}/${PLIST_LABEL}" 2>/dev/null \
      | awk -F'= ' '/^[[:space:]]*state =/{print $2; exit}')"
    [[ "$dstate" == "running" ]] && break
    [[ "$(date +%s)" -ge "$deadline" ]] && return 1
    sleep 1
  done
  if [[ -x "${BIN_DIR}/agent" ]]; then
    local i=0
    while [[ "$i" -lt 4 ]]; do
      "${BIN_DIR}/agent" tracker state >/dev/null 2>&1 && return 0
      sleep 1
      i=$(( i + 1 ))
    done
    return 1
  fi
  return 0
}

# rollback_daemon: undo a failed migration — bootout the (possibly half-loaded)
# new-label job and bootstrap the backed-up old plist back. Never leaves both
# jobs gone when a backup exists.
rollback_daemon() {
  local backup="$1"
  launchctl bootout "gui/${UID}/${PLIST_LABEL}" >/dev/null 2>&1 || true
  if [[ -f "$backup" ]]; then
    launchctl bootstrap "gui/${UID}" "$backup" >/dev/null 2>&1 || true
  fi
}

install_daemon() {
  [[ -f "$PLIST_TEMPLATE" ]] || die "missing plist template: ${PLIST_TEMPLATE}"
  # State dir must exist before bootstrap: launchd does not create parent dirs
  # for StandardOutPath/StandardErrorPath and would silently fail otherwise.
  mkdir -p "$STATE_DIR"
  mkdir -p "$(dirname "$PLIST_DEST")"

  # launchd 的默认 PATH 只有 /usr/bin:/bin:/usr/sbin:/sbin，不含 Homebrew。
  # daemon 要裸调 tmux（解析窗口上下文）、ssh（远程 🔔 透传）、terminal-notifier
  # （通知），找不到 tmux 时 start_task 等命令会静默失败、整个 tracker 瘫掉。
  # 把部署时探测到的 tmux 目录拼进 plist 的 EnvironmentVariables PATH。
  local tracker_tmux_bin tracker_tmux_dir tracker_path
  tracker_tmux_bin="$(command -v tmux || echo /opt/homebrew/bin/tmux)"
  tracker_tmux_dir="$(dirname "$tracker_tmux_bin")"
  tracker_path="${tracker_tmux_dir}:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

  # Absolute paths only — ProgramArguments is not shell-expanded, so no ~/$HOME.
  sed -e "s#__BIN__#${BIN_DIR}/tracker-server#g" \
      -e "s#__STATE__#${STATE_DIR}#g" \
      -e "s#__PATH__#${tracker_path}#g" \
      "$PLIST_TEMPLATE" > "$PLIST_DEST"
  chmod 644 "$PLIST_DEST"

  # Validate the rendered plist BEFORE touching launchd — a malformed plist must
  # never cause us to bootout the working (old) job with no valid replacement.
  plutil -lint "$PLIST_DEST" >/dev/null 2>&1 \
    || die "rendered plist failed plutil -lint: ${PLIST_DEST}"

  # Is there an old me.hatcloud.* daemon (on disk or loaded) to migrate off of?
  local has_old=0
  if [[ -f "$OLD_PLIST_DEST" ]] || launchctl print "gui/${UID}/${OLD_PLIST_LABEL}" >/dev/null 2>&1; then
    has_old=1
  fi

  if [[ "$has_old" -eq 1 ]]; then
    # ── Label-neutralisation migration (transactional) ──
    # The tracker socket is exclusive, so old and new must never run together:
    # bootout old → bootstrap new → health-check → only then delete old plist.
    mkdir -p "$BACKUP_DIR"
    local backup="${BACKUP_DIR}/${OLD_PLIST_LABEL}.plist"
    [[ -f "$OLD_PLIST_DEST" ]] && cp "$OLD_PLIST_DEST" "$backup"

    launchctl bootout "gui/${UID}/${OLD_PLIST_LABEL}" >/dev/null 2>&1 || true
    # Defensive: on a re-entry (new job already healthy but old plist not yet
    # deleted) the new label may still be loaded — bootout so bootstrap can't hit
    # "already loaded" and wrongly trip the rollback that kills the healthy job.
    launchctl bootout "gui/${UID}/${PLIST_LABEL}" >/dev/null 2>&1 || true

    if ! launchctl bootstrap "gui/${UID}" "$PLIST_DEST" >/dev/null 2>&1; then
      rollback_daemon "$backup"
      die "daemon migration failed: could not bootstrap ${PLIST_LABEL}; rolled back to ${OLD_PLIST_LABEL}. Manual recovery: launchctl bootstrap gui/${UID} ${OLD_PLIST_DEST}"
    fi
    if ! daemon_health_check; then
      rollback_daemon "$backup"
      die "daemon migration failed: ${PLIST_LABEL} not healthy after bootstrap; rolled back to ${OLD_PLIST_LABEL}. See ${STATE_DIR}/daemon.err.log; manual recovery: launchctl bootstrap gui/${UID} ${OLD_PLIST_DEST}"
    fi

    # New job is healthy — retire the old plist.
    rm -f "$OLD_PLIST_DEST"
    log "Migrated launchd daemon ${OLD_PLIST_LABEL} → ${PLIST_LABEL} (running; old plist backed up at ${backup})"
  else
    # Plain (re)install of the new-label job.
    launchctl bootout "gui/${UID}/${PLIST_LABEL}" >/dev/null 2>&1 || true
    launchctl bootstrap "gui/${UID}" "$PLIST_DEST" \
      || die "launchctl bootstrap failed for ${PLIST_DEST}"
    if ! daemon_health_check; then
      local exitcode
      exitcode="$(launchctl print "gui/${UID}/${PLIST_LABEL}" 2>/dev/null \
        | awk -F'= ' '/^[[:space:]]*last exit code =/{print $2; exit}')"
      die "daemon ${PLIST_LABEL} not healthy after bootstrap (last exit = ${exitcode:-n/a}); see ${STATE_DIR}/daemon.err.log"
    fi
    log "Installed launchd daemon ${PLIST_LABEL} (running)"
  fi
}

uninstall_daemon() {
  launchctl bootout "gui/${UID}/${PLIST_LABEL}" >/dev/null 2>&1 || true
  rm -f "$PLIST_DEST"
  log "Removed launchd daemon ${PLIST_LABEL}"
}

install_workspace_timer() {
  [[ -f "$WS_PLIST_TEMPLATE" ]] || die "missing plist template: ${WS_PLIST_TEMPLATE}"
  # State dir must exist before bootstrap (launchd does not create log parent dirs).
  mkdir -p "$WS_STATE_DIR"
  mkdir -p "$(dirname "$WS_PLIST_DEST")"

  # launchd 的 bootstrap 上下文里 macOS 给 tmux 算出的默认 socket 目录
  # （_CS_DARWIN_USER_TEMP_DIR，落在 /var/folders/…）和交互 shell 不一致，
  # 会让定时器连错 socket、列不到窗口。从运行中的 server 推导真实 socket 目录
  # （socket_path 去掉 /tmux-<uid>/<name> 两级）并钉进 plist 的 TMUX_TMPDIR。
  local ws_tmpdir="/tmp"
  if [[ -n "${TMUX:-}" ]]; then
    local sp
    sp="$(tmux display-message -p '#{socket_path}' 2>/dev/null || true)"
    [[ -n "$sp" ]] && ws_tmpdir="$(dirname "$(dirname "$sp")")"
  elif [[ -n "${TMUX_TMPDIR:-}" ]]; then
    ws_tmpdir="$TMUX_TMPDIR"
  fi

  # launchd 的默认 PATH 不含 Homebrew，裸调 tmux 会找不到 → 守卫静默 exit。
  # 把部署时探测到的 tmux 所在目录拼进 plist PATH。
  local tmux_bin tmux_dir ws_path
  tmux_bin="$(command -v tmux || echo /opt/homebrew/bin/tmux)"
  tmux_dir="$(dirname "$tmux_bin")"
  ws_path="${tmux_dir}:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

  sed -e "s#__SCRIPT__#${WS_SAVE_SCRIPT}#g" \
      -e "s#__STATE__#${WS_STATE_DIR}#g" \
      -e "s#__TMUX_TMPDIR__#${ws_tmpdir}#g" \
      -e "s#__PATH__#${ws_path}#g" \
      "$WS_PLIST_TEMPLATE" > "$WS_PLIST_DEST"
  chmod 644 "$WS_PLIST_DEST"

  # Validate before touching launchd (same rationale as install_daemon).
  plutil -lint "$WS_PLIST_DEST" >/dev/null 2>&1 \
    || die "rendered plist failed plutil -lint: ${WS_PLIST_DEST}"

  local has_old=0
  if [[ -f "$OLD_WS_PLIST_DEST" ]] || launchctl print "gui/${UID}/${OLD_WS_PLIST_LABEL}" >/dev/null 2>&1; then
    has_old=1
  fi

  if [[ "$has_old" -eq 1 ]]; then
    # ── Label-neutralisation migration (transactional) ──
    mkdir -p "$BACKUP_DIR"
    local backup="${BACKUP_DIR}/${OLD_WS_PLIST_LABEL}.plist"
    [[ -f "$OLD_WS_PLIST_DEST" ]] && cp "$OLD_WS_PLIST_DEST" "$backup"

    launchctl bootout "gui/${UID}/${OLD_WS_PLIST_LABEL}" >/dev/null 2>&1 || true
    # Defensive: bootout the new label too (re-entry may leave it loaded) so the
    # bootstrap below can't hit "already loaded" and trip a needless rollback.
    launchctl bootout "gui/${UID}/${WS_PLIST_LABEL}" >/dev/null 2>&1 || true

    if ! launchctl bootstrap "gui/${UID}" "$WS_PLIST_DEST" >/dev/null 2>&1; then
      launchctl bootout "gui/${UID}/${WS_PLIST_LABEL}" >/dev/null 2>&1 || true
      [[ -f "$backup" ]] && launchctl bootstrap "gui/${UID}" "$backup" >/dev/null 2>&1 || true
      die "workspace timer migration failed: could not bootstrap ${WS_PLIST_LABEL}; rolled back to ${OLD_WS_PLIST_LABEL}. Manual recovery: launchctl bootstrap gui/${UID} ${OLD_WS_PLIST_DEST}"
    fi
    # StartInterval job sits "waiting" between runs — health = job present.
    if ! launchctl print "gui/${UID}/${WS_PLIST_LABEL}" >/dev/null 2>&1; then
      launchctl bootout "gui/${UID}/${WS_PLIST_LABEL}" >/dev/null 2>&1 || true
      [[ -f "$backup" ]] && launchctl bootstrap "gui/${UID}" "$backup" >/dev/null 2>&1 || true
      die "workspace timer migration failed: ${WS_PLIST_LABEL} not loaded after bootstrap; rolled back to ${OLD_WS_PLIST_LABEL}. Manual recovery: launchctl bootstrap gui/${UID} ${OLD_WS_PLIST_DEST}"
    fi

    rm -f "$OLD_WS_PLIST_DEST"
    log "Migrated workspace auto-save timer ${OLD_WS_PLIST_LABEL} → ${WS_PLIST_LABEL} (every 180s; old plist backed up at ${backup})"
  else
    launchctl bootout "gui/${UID}/${WS_PLIST_LABEL}" >/dev/null 2>&1 || true
    launchctl bootstrap "gui/${UID}" "$WS_PLIST_DEST" \
      || die "launchctl bootstrap failed for ${WS_PLIST_DEST}"
    # StartInterval job sits in "waiting" between runs — only confirm it loaded.
    log "Installed workspace auto-save timer ${WS_PLIST_LABEL} (every 180s)"
  fi
}

uninstall_workspace_timer() {
  launchctl bootout "gui/${UID}/${WS_PLIST_LABEL}" >/dev/null 2>&1 || true
  rm -f "$WS_PLIST_DEST"
  log "Removed workspace auto-save timer ${WS_PLIST_LABEL}"
}

# Merge (install) or strip (uninstall) the managed Claude Stop hook inside
# ~/.claude/settings.json, touching only the managed entries (identified by a
# command that invokes our report wrapper). Other settings are preserved and a
# parse failure aborts without overwriting the file. MODE is via $CLAUDE_REG_MODE.
#
# NOTE: no MCP server is registered any more (planned debt landed) — the
# tracker-mcp `claude mcp add` was dropped because state now comes from the
# daemon's sessions-json poll. The Stop hook is RETAINED because it also captures
# the Claude session id, which `workspace --resume` needs to restore panes.
claude_hooks_merge() {
  [[ -f "$CLAUDE_SETTINGS" ]] || die "missing ${CLAUDE_SETTINGS}"
  CLAUDE_REG_MODE="$1" \
  CLAUDE_SETTINGS="$CLAUDE_SETTINGS" \
  REPORT_SH="$CLAUDE_REPORT_SH" \
  python3 - <<'PY' || die "failed to update ${CLAUDE_SETTINGS}"
import json, os, sys, tempfile

mode = os.environ["CLAUDE_REG_MODE"]
path = os.environ["CLAUDE_SETTINGS"]
report = os.environ["REPORT_SH"]

try:
    with open(path) as f:
        data = json.load(f)
except Exception as e:
    sys.stderr.write("parse error: %s\n" % e)
    sys.exit(1)

def is_managed(entry):
    # An entry is hat-managed if any of its commands invokes our report wrapper.
    for h in entry.get("hooks", []):
        if isinstance(h, dict) and report in str(h.get("command", "")):
            return True
    return False

# Currently managed hook events. The Notification → notify hook was retired:
# notifications now come from the daemon's sessions-json poll (busy→idle /
# →asking), and the old hook only ever fired `notify` without a summary (a
# silent error). Stop stays — it also captures the Claude session id for
# workspace --resume restore.
EVENTS = {"Stop": "finish_task"}
# Every event we have ever managed; install strips our stale entries from all of
# them (so a retired event like Notification is cleaned up), then re-adds EVENTS.
MANAGED_EVENTS = ("Stop", "Notification")

def strip_managed(hooks):
    for event in MANAGED_EVENTS:
        if event in hooks:
            kept = [e for e in hooks[event] if not is_managed(e)]
            if kept:
                hooks[event] = kept
            else:
                hooks.pop(event, None)

if mode == "install":
    hooks = data.setdefault("hooks", {})
    if not isinstance(hooks, dict):  # e.g. pre-existing "hooks": null/[]
        hooks = data["hooks"] = {}
    strip_managed(hooks)
    for event, action in EVENTS.items():
        lst = hooks.get(event, [])
        lst.append({"hooks": [{"type": "command",
                               "command": "%s %s" % (report, action)}]})
        hooks[event] = lst
elif mode == "uninstall":
    hooks = data.get("hooks", {})
    strip_managed(hooks)
else:
    sys.stderr.write("unknown mode: %s\n" % mode)
    sys.exit(1)

fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path), suffix=".tmp")
with os.fdopen(fd, "w") as f:
    json.dump(data, f, indent=2, ensure_ascii=False)
    f.write("\n")
os.replace(tmp, path)
PY
}

# Only the Stop hook is managed here now (see claude_hooks_merge note). Editing
# settings.json needs no `claude` binary, so we no longer hard-die on its
# absence — the hook merge is a plain JSON edit.
register_claude() {
  claude_hooks_merge install
  log "Registered Claude Stop hook"
}

unregister_claude() {
  [[ -f "$CLAUDE_SETTINGS" ]] && claude_hooks_merge uninstall
  log "Removed Claude Stop hook"
}

# statusLine registration is independent of the Stop hook (separate --skip gate).
# The rendering command is this repo's claude_statusline.sh (absolute path). The
# chain it delegates to, plus the user's pre-takeover statusLine, live in
# agent-config.json's statusline_chain (written by setup — the only transport).
#
# Safety (review I6): if statusline_chain.original was recorded (we previously
# took over a user statusLine), only (re)write settings.json.statusLine while we
# still own it — current == original (not yet flipped) or current == our script
# (already installed). If the user changed it out from under us → ABORT, do not
# clobber. With no recorded original, register only when nothing meaningful is
# there (absent or already ours), else abort so a stray user statusLine is not
# silently lost.
register_statusline() {
  [[ -f "$CLAUDE_SETTINGS" ]] || die "missing ${CLAUDE_SETTINGS}"
  STATUSLINE_MODE=install \
  CLAUDE_SETTINGS="$CLAUDE_SETTINGS" \
  AGENT_CONFIG_JSON="$AGENT_CONFIG_JSON" \
  STATUSLINE_SCRIPT="$STATUSLINE_SCRIPT" \
  python3 - <<'PY' || die "failed to register statusLine in ${CLAUDE_SETTINGS}"
import json, os, sys, tempfile

mode = os.environ["STATUSLINE_MODE"]
settings_path = os.environ["CLAUDE_SETTINGS"]
config_path = os.environ["AGENT_CONFIG_JSON"]
script = os.environ["STATUSLINE_SCRIPT"]

try:
    with open(settings_path) as f:
        settings = json.load(f)
except Exception as e:
    sys.stderr.write("parse error (%s): %s\n" % (settings_path, e))
    sys.exit(1)

chain = None
if os.path.exists(config_path):
    try:
        with open(config_path) as f:
            chain = (json.load(f) or {}).get("statusline_chain")
    except Exception as e:
        sys.stderr.write("parse error (%s): %s\n" % (config_path, e))
        sys.exit(1)

managed = {"type": "command", "command": script}
current = settings.get("statusLine")
ours = isinstance(current, dict) and current.get("command") == script

if mode == "install":
    has_original = isinstance(chain, dict) and "original" in chain
    if has_original:
        original = chain["original"]
        if current == original or ours:
            settings["statusLine"] = managed
        else:
            sys.stderr.write(
                "abort: settings.json statusLine differs from the recorded "
                "original — it was changed after our takeover. Re-run setup's "
                "statusline decision; refusing to overwrite.\n")
            sys.exit(1)
    else:
        if current is None or ours:
            settings["statusLine"] = managed
        else:
            sys.stderr.write(
                "abort: an existing statusLine is present but no statusline_chain "
                "is recorded in agent-config.json. Run setup's statusline decision "
                "first; refusing to overwrite.\n")
            sys.exit(1)
elif mode == "uninstall":
    # Only undo our own registration; never touch a statusLine the user owns.
    if ours:
        if isinstance(chain, dict) and "original" in chain:
            settings["statusLine"] = chain["original"]
        else:
            settings.pop("statusLine", None)
    else:
        sys.exit(0)
else:
    sys.stderr.write("unknown mode: %s\n" % mode)
    sys.exit(1)

fd, tmp = tempfile.mkstemp(dir=os.path.dirname(settings_path), suffix=".tmp")
with os.fdopen(fd, "w") as f:
    json.dump(settings, f, indent=2, ensure_ascii=False)
    f.write("\n")
os.replace(tmp, settings_path)
PY
  log "Registered Claude statusLine (${STATUSLINE_SCRIPT})"
}

unregister_statusline() {
  [[ -f "$CLAUDE_SETTINGS" ]] || return 0
  STATUSLINE_MODE=uninstall \
  CLAUDE_SETTINGS="$CLAUDE_SETTINGS" \
  AGENT_CONFIG_JSON="$AGENT_CONFIG_JSON" \
  STATUSLINE_SCRIPT="$STATUSLINE_SCRIPT" \
  python3 - <<'PY' || die "failed to unregister statusLine in ${CLAUDE_SETTINGS}"
import json, os, sys, tempfile

settings_path = os.environ["CLAUDE_SETTINGS"]
config_path = os.environ["AGENT_CONFIG_JSON"]
script = os.environ["STATUSLINE_SCRIPT"]

try:
    with open(settings_path) as f:
        settings = json.load(f)
except Exception as e:
    sys.stderr.write("parse error (%s): %s\n" % (settings_path, e))
    sys.exit(1)

chain = None
if os.path.exists(config_path):
    try:
        with open(config_path) as f:
            chain = (json.load(f) or {}).get("statusline_chain")
    except Exception as e:
        # Fail closed: a corrupt config might hide a recorded `original`, and
        # popping statusLine instead of restoring it would silently lose the
        # user's pre-takeover statusLine. Abort rather than guess.
        sys.stderr.write("parse error (%s): %s\n" % (config_path, e))
        sys.exit(1)

current = settings.get("statusLine")
ours = isinstance(current, dict) and current.get("command") == script
if not ours:
    sys.exit(0)

if isinstance(chain, dict) and "original" in chain:
    settings["statusLine"] = chain["original"]
else:
    settings.pop("statusLine", None)

fd, tmp = tempfile.mkstemp(dir=os.path.dirname(settings_path), suffix=".tmp")
with os.fdopen(fd, "w") as f:
    json.dump(settings, f, indent=2, ensure_ascii=False)
    f.write("\n")
os.replace(tmp, settings_path)
PY
  log "Removed Claude statusLine registration"
}

# The `agent` launcher alias lives in the Syncthing-synced shell config so it is
# available in every interactive shell. Append it idempotently on a fresh setup.
# When that shared file is absent (non-Hat machines), fall back to a managed
# block in ~/.zshrc.
ALIAS_COMMON="${HOME}/.hat-env/shared/alias-common"
AGENT_LAUNCHER="${HOME}/.hat-config/tmux/scripts/agent"
TMUX_RESUME="${REPO_DIR}/tmux/scripts/tmux_resume.sh"
ZSHRC="${HOME}/.zshrc"
ALIAS_START_MARKER="# >>> hat-tmux-agent-workbench aliases"
ALIAS_END_MARKER="# <<< hat-tmux-agent-workbench aliases"

# Idempotently write the managed alias block into ~/.zshrc (strip any prior copy
# first, matching write_managed_block's approach).
write_alias_zshrc_block() {
  local tmp
  tmp="$(mktemp)"
  if [[ -f "$ZSHRC" ]]; then
    awk -v start="$ALIAS_START_MARKER" -v end="$ALIAS_END_MARKER" '
      $0 == start { skip = 1; next }
      $0 == end { skip = 0; next }
      skip != 1 { print }
    ' "$ZSHRC" > "$tmp"
  fi
  if [[ -s "$tmp" ]] && [[ "$(tail -c 1 "$tmp")" != "" ]]; then
    printf '\n' >> "$tmp"
  fi
  cat >> "$tmp" <<EOF
${ALIAS_START_MARKER}
alias agent='${AGENT_LAUNCHER}'
alias tmux-resume='${TMUX_RESUME}'
${ALIAS_END_MARKER}
EOF
  mv "$tmp" "$ZSHRC"
}

register_alias() {
  if [[ -f "$ALIAS_COMMON" ]]; then
    if ! grep -q "tmux/scripts/agent" "$ALIAS_COMMON"; then
      printf "\n# agent 启动器（hat-config managed）\nalias agent='%s'\n" "$AGENT_LAUNCHER" >> "$ALIAS_COMMON"
      log "Added agent alias to ${ALIAS_COMMON}"
    fi
    if ! grep -q "tmux/scripts/tmux_resume.sh" "$ALIAS_COMMON"; then
      printf "\n# tmux workspace 恢复（hat-config managed）\nalias tmux-resume='%s'\n" \
        "$TMUX_RESUME" >> "$ALIAS_COMMON"
      log "Added tmux-resume alias to ${ALIAS_COMMON}"
    fi
    return 0
  fi

  # Fallback: no Syncthing-synced alias file — offer to manage ~/.zshrc instead.
  if [[ "$ASSUME_YES" -eq 1 ]]; then
    log "note: ${ALIAS_COMMON} not found (non-interactive) — add these aliases manually to your shell config:"
    log "    alias agent='${AGENT_LAUNCHER}'"
    log "    alias tmux-resume='${TMUX_RESUME}'"
    return 0
  fi
  if confirm "未找到 ${ALIAS_COMMON}，写入 ~/.zshrc?"; then
    write_alias_zshrc_block
    log "Added agent / tmux-resume aliases to ${ZSHRC}"
  else
    log "Skipped alias registration; add these manually to your shell config:"
    log "    alias agent='${AGENT_LAUNCHER}'"
    log "    alias tmux-resume='${TMUX_RESUME}'"
  fi
}

unregister_alias() {
  # Target 1: the Syncthing-synced alias-common (in-place line delete preserves
  # perms/inode — mktemp+mv would flip 644 → 600 — and is atomic per line).
  if [[ -f "$ALIAS_COMMON" ]] \
     && grep -qE "tmux/scripts/agent|tmux/scripts/tmux_resume.sh" "$ALIAS_COMMON"; then
    sed -i '' \
      -e '/tmux\/scripts\/agent/d' -e '/agent 启动器/d' \
      -e '/tmux\/scripts\/tmux_resume.sh/d' -e '/tmux workspace 恢复/d' \
      "$ALIAS_COMMON"
    log "Removed agent / tmux-resume aliases from ${ALIAS_COMMON}"
  fi

  # Target 2: the ~/.zshrc managed block (fallback target).
  if [[ -f "$ZSHRC" ]] && grep -Fq "$ALIAS_START_MARKER" "$ZSHRC"; then
    local tmp
    tmp="$(mktemp)"
    awk -v start="$ALIAS_START_MARKER" -v end="$ALIAS_END_MARKER" '
      $0 == start { skip = 1; next }
      $0 == end { skip = 0; next }
      skip != 1 { print }
    ' "$ZSHRC" > "$tmp"
    mv "$tmp" "$ZSHRC"
    log "Removed agent / tmux-resume aliases from ${ZSHRC}"
  fi
}

# Project marker embedded in scripts/git-hooks/pre-commit; also the string we
# grep for to tell OUR hook apart from a user's pre-existing one.
PRE_COMMIT_SRC="${REPO_DIR}/scripts/git-hooks/pre-commit"
PRE_COMMIT_MARKER="# hat-tmux-agent-workbench pre-commit"

# Install the reserved-path guard into .git/hooks/pre-commit. Never clobber a
# user's own hook: if a pre-commit exists without our marker, leave it and tell
# the user to merge manually.
install_pre_commit_hook() {
  [[ -d "${REPO_DIR}/.git" ]] || { log "skip pre-commit hook: ${REPO_DIR}/.git absent (not a plain git repo)"; return 0; }
  [[ -f "$PRE_COMMIT_SRC" ]] || { log "skip pre-commit hook: ${PRE_COMMIT_SRC} missing"; return 0; }

  local hooks_dir="${REPO_DIR}/.git/hooks"
  local dest="${hooks_dir}/pre-commit"
  mkdir -p "$hooks_dir"

  if [[ -e "$dest" ]] && ! grep -Fq "$PRE_COMMIT_MARKER" "$dest" 2>/dev/null; then
    log "note: an existing ${dest} is not managed by this project — leaving it untouched."
    log "      Merge the reserved-path guard from ${PRE_COMMIT_SRC} manually if you want it."
    return 0
  fi
  cp "$PRE_COMMIT_SRC" "$dest"
  chmod +x "$dest"
  log "Installed git pre-commit hook (reserved-path guard)"
}

sync_mini_if_available() {
  [[ "$SKIP_MINI_SYNC" == "1" ]] && return 0

  local ssh_opts=(-o BatchMode=yes -o ConnectTimeout=3)
  if ! command -v ssh >/dev/null 2>&1 \
     || ! ssh "${ssh_opts[@]}" "$MINI_HOST" true >/dev/null 2>&1; then
    log "mini sync: ${MINI_HOST} is not reachable over SSH; skipped"
    return 0
  fi
  command -v rsync >/dev/null 2>&1 || die "mini sync: rsync is required when ${MINI_HOST} is reachable"

  log "mini sync: syncing project files to ${MINI_HOST}:~/.hat-config"
  rsync -a --delete \
    --exclude='/.git/' \
    --exclude='/.claude/' \
    --exclude='/.tasks/' \
    --exclude='/.stfolder/' \
    --exclude='/.stignore' \
    --exclude='/.stignore-shared' \
    --exclude='/CLAUDE.md' \
    --exclude='/private/' \
    --exclude='/state/' \
    --exclude='/tmp/' \
    --exclude='/agent-tracker/bin/' \
    --exclude='/agent-tracker/agent' \
    --exclude='/agent-tracker/agent-config.json' \
    --exclude='/snippets/private/' \
    --exclude='/snippets/.favorites' \
    --exclude='/.DS_Store' \
    --exclude='*/.DS_Store' \
    --exclude='__pycache__/' \
    --exclude='*.pyc' \
    "${REPO_DIR}/" "${MINI_HOST}:.hat-config/"

  local remote_cmd="cd \"\$HOME/.hat-config\" && PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin HAT_CONFIG_SKIP_MINI_SYNC=1 scripts/deploy.sh update --yes"
  [[ "$RELOAD_TMUX" -eq 0 ]] && remote_cmd+=" --no-reload"
  [[ "$SKIP_TMUX" -eq 1 ]] && remote_cmd+=" --skip-tmux"
  [[ "$SKIP_DAEMON" -eq 1 ]] && remote_cmd+=" --skip-daemon"
  [[ "$SKIP_WS_TIMER" -eq 1 ]] && remote_cmd+=" --skip-ws-timer"
  [[ "$SKIP_STOP_HOOK" -eq 1 ]] && remote_cmd+=" --skip-stop-hook"
  [[ "$SKIP_STATUSLINE" -eq 1 ]] && remote_cmd+=" --skip-statusline"
  [[ "$SKIP_ALIAS" -eq 1 ]] && remote_cmd+=" --skip-alias"
  ssh "${ssh_opts[@]}" "$MINI_HOST" "$remote_cmd"
  log "mini sync: ${MINI_HOST} updated"
}

install_or_update() {
  preflight
  ensure_runtime
  build_binaries
  [[ "$SKIP_DAEMON" -eq 1 ]] || install_daemon
  [[ "$SKIP_WS_TIMER" -eq 1 ]] || install_workspace_timer
  [[ "$SKIP_STOP_HOOK" -eq 1 ]] || register_claude
  [[ "$SKIP_STATUSLINE" -eq 1 ]] || register_statusline
  [[ "$SKIP_ALIAS" -eq 1 ]] || register_alias
  if [[ "$SKIP_TMUX" -eq 0 ]]; then
    write_managed_block "$TMUX_CONF"
    reload_tmux
    log "Installed Hat Config tmux integration into ${TMUX_CONF}"
  fi
  install_pre_commit_hook
  sync_mini_if_available
}

uninstall() {
  [[ "$SKIP_DAEMON" -eq 1 ]] || uninstall_daemon
  [[ "$SKIP_WS_TIMER" -eq 1 ]] || uninstall_workspace_timer
  [[ "$SKIP_STOP_HOOK" -eq 1 ]] || unregister_claude
  [[ "$SKIP_STATUSLINE" -eq 1 ]] || unregister_statusline
  [[ "$SKIP_ALIAS" -eq 1 ]] || unregister_alias
  if [[ "$SKIP_TMUX" -eq 0 ]]; then
    remove_managed_block "$TMUX_CONF"
    cleanup_active_tmux_keys
  fi

  if [[ "$REMOVE_STATE" -eq 0 && "$KEEP_STATE" -eq 0 && "$ASSUME_YES" -eq 0 ]]; then
    if confirm "Remove local tracker state at ${REPO_DIR}/state?"; then
      REMOVE_STATE=1
    fi
  fi

  if [[ "$REMOVE_STATE" -eq 1 ]]; then
    rm -rf "${REPO_DIR}/state"
    log "Removed local tracker state."
  fi

  log "Uninstalled Hat Config tmux integration from ${TMUX_CONF}"
}

status() {
  log "Repository: ${REPO_DIR}"
  log "tmux config: ${TMUX_CONF}"

  if [[ -f "$TMUX_CONF" ]] && grep -Fq "$START_MARKER" "$TMUX_CONF"; then
    log "Deployment: installed"
  else
    log "Deployment: not installed"
  fi

  if [[ -d "${REPO_DIR}/state" ]]; then
    log "State: present"
  else
    log "State: absent"
  fi

  local dinfo
  if dinfo="$(launchctl print "gui/${UID}/${PLIST_LABEL}" 2>/dev/null)"; then
    local dstate exitcode
    dstate="$(awk -F'= ' '/^[[:space:]]*state =/{print $2; exit}' <<<"$dinfo")"
    exitcode="$(awk -F'= ' '/^[[:space:]]*last exit code =/{print $2; exit}' <<<"$dinfo")"
    log "Daemon (${PLIST_LABEL}): loaded (state = ${dstate:-unknown}, last exit = ${exitcode:-n/a})"
    if [[ -n "$exitcode" && "$exitcode" != "0" && "$exitcode" != *never* ]]; then
      log "  WARN: daemon has non-zero last exit code; check ${STATE_DIR}/daemon.err.log"
    fi
  else
    log "Daemon (${PLIST_LABEL}): not loaded"
  fi

  if launchctl print "gui/${UID}/${WS_PLIST_LABEL}" >/dev/null 2>&1; then
    log "Timer (${WS_PLIST_LABEL}): loaded (workspace auto-save, every 180s)"
  else
    log "Timer (${WS_PLIST_LABEL}): not loaded"
  fi
}

main() {
  parse_args "$@"
  if [[ -z "$ACTION" ]]; then
    choose_action
  fi

  case "$ACTION" in
    install|update) install_or_update ;;
    uninstall) uninstall ;;
    status) status ;;
    *) die "unknown action: $ACTION" ;;
  esac
}

main "$@"
