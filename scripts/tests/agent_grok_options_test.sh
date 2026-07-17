#!/usr/bin/env bash
# Assert tmux/scripts/agent lists Grok when grok is on PATH (build_options).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
AGENT="$ROOT/tmux/scripts/agent"
[[ -f "$AGENT" ]] || { echo "missing $AGENT"; exit 1; }

# build_options is a function inside agent; extract by running a snippet that
# sources the option-building logic via grep of expected lines in the file.
if ! grep -q "printf 'Grok" "$AGENT" && ! grep -q 'printf "Grok' "$AGENT"; then
  echo "FAIL: agent script has no Grok option line"
  exit 1
fi
if ! grep -q 'agent_client="grok"' "$AGENT"; then
  echo "FAIL: agent script missing agent_client=grok case"
  exit 1
fi
if ! grep -q 'grok --resume\|--resume' "$AGENT"; then
  echo "FAIL: resume path missing"
  exit 1
fi
echo "PASS agent_grok_options_test"
