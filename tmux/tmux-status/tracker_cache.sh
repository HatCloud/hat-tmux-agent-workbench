#!/usr/bin/env bash
set -euo pipefail

CACHE_FILE="$HOME/.hat-config/state/agent-tracker/tmux-tracker-cache.json"
CACHE_MAX_AGE=1

agent_bin="$HOME/.hat-config/agent-tracker/bin/agent"

# Ensure the runtime state dir exists (deploy creates it, but the status bar may
# render before the first deploy on a fresh checkout).
mkdir -p "$(dirname "$CACHE_FILE")" 2>/dev/null || true

# Check if cache is fresh enough. Probe stat flavor by capability, not by
# $OSTYPE: macOS with Homebrew coreutils on PATH ships GNU stat (-c), where the
# BSD form (-f) means --file-system and fails. Try GNU first, fall back to BSD.
if [[ -f "$CACHE_FILE" ]]; then
  mtime=$(stat -c %Y "$CACHE_FILE" 2>/dev/null || stat -f %m "$CACHE_FILE" 2>/dev/null || echo 0)
  file_age=$(( $(date +%s) - mtime ))
  if (( file_age < CACHE_MAX_AGE )); then
    exit 0
  fi
fi

# Simple lock using mkdir (atomic on all systems); per-user to avoid cross-user
# contention on a shared /tmp.
LOCK_DIR="/tmp/tmux-tracker-cache-$(id -u).lock"
if ! mkdir "$LOCK_DIR" 2>/dev/null; then
  exit 0
fi
trap 'rmdir "$LOCK_DIR" 2>/dev/null || true' EXIT

if [[ -x "$agent_bin" ]]; then
  if "$agent_bin" tracker state 2>/dev/null > "$CACHE_FILE.tmp"; then
    mv "$CACHE_FILE.tmp" "$CACHE_FILE"
  else
    # daemon 不可达（连不上 socket）→ 触发自愈 watchdog（自带 30s 节流），
    # 覆盖 launchd KeepAlive 救不回的「job 被 bootout」场景。保留旧 cache 不动。
    rm -f "$CACHE_FILE.tmp" 2>/dev/null || true
    "$HOME/.hat-config/tmux/scripts/ensure_tracker_daemon.sh" >/dev/null 2>&1 &
  fi
else
  echo '{}' > "$CACHE_FILE.tmp" && mv "$CACHE_FILE.tmp" "$CACHE_FILE"
fi
