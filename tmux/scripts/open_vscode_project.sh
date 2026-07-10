#!/usr/bin/env bash
set -euo pipefail

path="${1:-$PWD}"

if [[ -z "$path" ]]; then
  path="$PWD"
fi

if [[ -f "$path" ]]; then
  path="$(dirname "$path")"
fi

if [[ ! -d "$path" ]]; then
  path="$PWD"
fi

if root="$(git -C "$path" rev-parse --show-toplevel 2>/dev/null)"; then
  project_root="$root"
else
  project_root="$path"
fi

if command -v code >/dev/null 2>&1; then
  code "$project_root" >/dev/null 2>&1 || {
    tmux display-message "VS Code: failed to open $project_root" 2>/dev/null || true
    exit 1
  }
elif [[ -d "/Applications/Visual Studio Code.app" ]]; then
  open -a "Visual Studio Code" "$project_root" >/dev/null 2>&1 || {
    tmux display-message "VS Code: failed to open $project_root" 2>/dev/null || true
    exit 1
  }
else
  tmux display-message "VS Code: code command not found" 2>/dev/null || true
  exit 127
fi

open -a "Visual Studio Code" >/dev/null 2>&1 || true
tmux display-message "VS Code: $project_root" 2>/dev/null || true
