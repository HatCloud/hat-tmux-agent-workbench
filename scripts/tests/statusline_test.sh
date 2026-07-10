#!/bin/bash
# AT7 — automated assertions for tmux/scripts/claude_statusline.sh.
# Fakes stdin payloads and a temp state/config dir; no real HOME/state touched.
set -u

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)
STATUSLINE="$SCRIPT_DIR/../../tmux/scripts/claude_statusline.sh"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
export HAT_STATE_DIR="$TMP/state"
export HAT_CONFIG_FILE="$TMP/agent-config.json"
mkdir -p "$HAT_STATE_DIR"

fail=0
pass() { printf 'PASS: %s\n' "$1"; }
die() {
	printf 'FAIL: %s\n' "$1"
	fail=1
}

FULL_JSON='{"model":{"display_name":"Opus"},"workspace":{"current_dir":"'"$HOME"'/proj"},"rate_limits":{"five_hour":{"used_percentage":42.5,"resets_at":9999999999},"seven_day":{"used_percentage":10.0,"resets_at":9999999999}}}'
cache="$HAT_STATE_DIR/claude-rate-limits.json"

# ① full JSON → cache written with written_at + five_hour/seven_day
rm -f "$cache"
printf '%s' "$FULL_JSON" | "$STATUSLINE" >/dev/null 2>&1
if [ -f "$cache" ] && jq -e '.written_at and .five_hour.used_percentage and .five_hour.resets_at and .seven_day.used_percentage and .seven_day.resets_at' "$cache" >/dev/null 2>&1; then
	pass "① cache has written_at + five_hour/seven_day"
else
	die "① cache missing/incomplete: $(cat "$cache" 2>/dev/null)"
fi

# ② chain = cat → passthrough identical
printf '{"statusline_chain":{"command":"cat"}}' >"$HAT_CONFIG_FILE"
out=$(printf '%s' "$FULL_JSON" | "$STATUSLINE" 2>/dev/null)
if [ "$out" = "$FULL_JSON" ]; then
	pass "② chain=cat passthrough identical"
else
	die "② passthrough mismatch: got [$out]"
fi

# ③ chain = sleep 10 → hard 2s timeout, builtin output, first-time stderr warning
printf '{"statusline_chain":{"command":"sleep 10"}}' >"$HAT_CONFIG_FILE"
rm -f "$HAT_STATE_DIR/.statusline-chain-warn"
start=$(date +%s)
out3=$(printf '%s' "$FULL_JSON" | "$STATUSLINE" 2>"$TMP/err3")
end=$(date +%s)
elapsed=$((end - start))
if [ "$elapsed" -lt 5 ]; then pass "③ chain timeout returned in ${elapsed}s (<5)"; else die "③ too slow: ${elapsed}s (timeout not enforced)"; fi
if printf '%s' "$out3" | grep -q "Opus"; then pass "③ fell back to builtin render"; else die "③ builtin render missing: [$out3]"; fi
if grep -q "chain failed" "$TMP/err3"; then pass "③ first-time stderr warning emitted"; else die "③ no stderr warning"; fi

# ③b chain = PIPELINE that outlives 2s → timeout must still bound wall-clock
#     (regression guard: a pipeline chain forks, so the group must be killed).
printf '{"statusline_chain":{"command":"sleep 8 | cat"}}' >"$HAT_CONFIG_FILE"
start=$(date +%s)
out3b=$(printf '%s' "$FULL_JSON" | "$STATUSLINE" 2>/dev/null)
end=$(date +%s)
elapsed=$((end - start))
if [ "$elapsed" -lt 5 ]; then pass "③b pipeline chain timeout bounded (${elapsed}s <5)"; else die "③b pipeline chain NOT bounded: ${elapsed}s"; fi
if printf '%s' "$out3b" | grep -q "Opus"; then pass "③b pipeline chain → builtin fallback"; else die "③b builtin missing: [$out3b]"; fi

# ④ same epoch hour repeat → stderr silent (throttled)
printf '%s' "$FULL_JSON" | "$STATUSLINE" 2>"$TMP/err4" >/dev/null
if [ -s "$TMP/err4" ]; then die "④ warning not throttled: $(cat "$TMP/err4")"; else pass "④ warning throttled (silent)"; fi

# ⑤ chain = true (exit 0, empty output) → builtin fallback
printf '{"statusline_chain":{"command":"true"}}' >"$HAT_CONFIG_FILE"
out5=$(printf '%s' "$FULL_JSON" | "$STATUSLINE" 2>/dev/null)
if printf '%s' "$out5" | grep -q "Opus"; then pass "⑤ empty chain output → builtin"; else die "⑤ builtin fallback missing: [$out5]"; fi

# ⑥ no rate_limits → no cache write but stdout non-empty
rm -f "$HAT_CONFIG_FILE" "$cache"
NORL_JSON='{"model":{"display_name":"Sonnet"},"workspace":{"current_dir":"'"$HOME"'/x"}}'
out6=$(printf '%s' "$NORL_JSON" | "$STATUSLINE" 2>/dev/null)
if [ -f "$cache" ]; then die "⑥ cache written despite no rate_limits"; else pass "⑥ no cache when rate_limits absent"; fi
if [ -n "$out6" ]; then pass "⑥ stdout non-empty (builtin)"; else die "⑥ empty stdout"; fi

if [ "$fail" -eq 0 ]; then
	printf '\nALL PASS\n'
	exit 0
else
	printf '\nSOME FAILED\n'
	exit 1
fi
