#!/usr/bin/env bash
set -euo pipefail

agent_bin="$HOME/.hat-config/agent-tracker/bin/agent"
[[ -x "$agent_bin" ]] || exit 0

exec "$agent_bin" tmux right-status "$@"
