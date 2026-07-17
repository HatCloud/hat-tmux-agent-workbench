#!/usr/bin/env bash
# Dual-read parsing for workspace restore columns (unit-level, no tmux).
set -euo pipefail

parse_line() {
  local line="$1"
  local session_name window_index window_name repo_root layout col6 col7
  IFS=$'\t' read -r session_name window_index window_name repo_root layout col6 col7 <<<"$line"
  agent_client=""
  session_key=""
  if [[ -n "${col7:-}" ]]; then
    agent_client="${col6:-}"
    session_key="${col7%%[[:space:]]*}"
  elif [[ -n "${col6:-}" ]]; then
    agent_client="claude"
    session_key="${col6%%[[:space:]]*}"
  fi
  case "$agent_client" in
    codex) echo "codex resume $session_key" ;;
    grok)  echo "grok --resume $session_key" ;;
    claude|"") echo "claude --resume $session_key" ;;
    *) echo "LAYOUT_ONLY" ;;
  esac
}

# 6-col legacy
got="$(parse_line $'s\t0\tw\t/repo\tlandscape\tsid-old')"
[[ "$got" == "claude --resume sid-old" ]] || { echo "FAIL 6col: $got"; exit 1; }

# 7-col grok
got="$(parse_line $'s\t0\tw\t/repo\tlandscape\tgrok\tsid-g')"
[[ "$got" == "grok --resume sid-g" ]] || { echo "FAIL 7col grok: $got"; exit 1; }

# 7-col codex
got="$(parse_line $'s\t0\tw\t/repo\tlandscape\tcodex\tsid-c')"
[[ "$got" == "codex resume sid-c" ]] || { echo "FAIL 7col codex: $got"; exit 1; }

echo "PASS workspace_resume_format_test"
