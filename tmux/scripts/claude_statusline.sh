#!/bin/bash
# Project-internal Claude Code statusLine entry.
#
#   1. Cache the injected rate_limits snapshot so agent-tracker's reset timer has
#      a fallback fire time (schema matches quota.go's claudeFallbackResetAt:
#      top-level five_hour/seven_day.{used_percentage,resets_at}; written_at is an
#      extra epoch stamp that quota.go ignores).
#   2. If the user configured a statusline "chain" command, delegate rendering to
#      it (2s hard timeout); on failure/timeout/empty output fall back to builtin.
#   3. Otherwise render a builtin line: cwd + model + rate-limit countdown.
#
# bash 3.2 compatible: no bash 4+ syntax, no `timeout` binary (perl alarm instead).
# stdout carries ONLY the rendered status; side-effect noise is sent to /dev/null,
# except the throttled chain-failure warning which intentionally goes to stderr.

set -u

input=$(head -c 1048576)

# ── resolve paths (overridable for tests) ───────────────────────────────────
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)
repo_root=$(cd "$script_dir/../.." 2>/dev/null && pwd)
STATE_DIR="${HAT_STATE_DIR:-$repo_root/state/agent-tracker}"
CONFIG_FILE="${HAT_CONFIG_FILE:-$HOME/.config/agent-tracker/agent-config.json}"

# run_chain <seconds> <cmd...>: pipe our stdin through the command in its OWN
# process group, and on timeout kill the whole group. A negative-pid TERM/KILL
# reaps pipeline children too, so a pipeline chain (e.g. `foo | jq`) can't hold
# the stdout pipe open past the deadline (bash 3.2: no `timeout` binary). Prints
# the command's stdout; returns its exit code, or 124 on timeout.
run_chain() {
	perl -e '
		use POSIX qw(setsid);
		my $secs = shift @ARGV;
		my $pid = fork();
		exit 127 unless defined $pid;
		if ($pid == 0) { setsid(); exec @ARGV; exit 127; }
		my $timed = 0;
		$SIG{ALRM} = sub { $timed = 1; kill("-TERM", $pid); };
		alarm($secs);
		waitpid($pid, 0);
		my $st = $?;
		alarm(0);
		if ($timed) { kill("-KILL", $pid); exit 124; }
		exit($st >> 8);
	' "$@"
}

# ── 1. cache rate_limits snapshot (atomic; skip silently if no state dir) ────
rl=$(printf '%s' "$input" | jq -c '.rate_limits // empty' 2>/dev/null)
if [ -n "$rl" ] && [ -d "$STATE_DIR" ]; then
  now_epoch=$(date +%s)
  merged=$(printf '%s' "$rl" | jq -c --argjson w "$now_epoch" '. + {written_at: $w}' 2>/dev/null)
  if [ -n "$merged" ]; then
    tmp="$STATE_DIR/claude-rate-limits.json.$$"
    if printf '%s\n' "$merged" >"$tmp" 2>/dev/null; then
      mv -f "$tmp" "$STATE_DIR/claude-rate-limits.json" 2>/dev/null || rm -f "$tmp" 2>/dev/null
    fi
  fi
fi

# ── throttled chain-failure warning (design C4: once per epoch hour) ─────────
warn_chain_failure() {
  local warn_file="$STATE_DIR/.statusline-chain-warn"
  local this_hour last=""
  this_hour=$(( now_epoch_or_now / 3600 ))
  [ -f "$warn_file" ] && last=$(cat "$warn_file" 2>/dev/null)
  if [ "$this_hour" != "$last" ]; then
    printf 'statusline chain failed, using builtin\n' >&2
    [ -d "$STATE_DIR" ] && printf '%s\n' "$this_hour" >"$warn_file" 2>/dev/null
  fi
}

# ── builtin renderer ────────────────────────────────────────────────────────
format_remaining() {
  local reset_at=$1 now remaining days hours minutes
  # Only a plain integer epoch is usable; a malformed / non-numeric resets_at
  # (empty, float, string) must fail quietly, not leak a bash arithmetic error
  # to stderr (only the throttled chain warning is allowed on stderr).
  case "$reset_at" in
    ''|*[!0-9]*) return 1 ;;
  esac
  now=$(date +%s)
  remaining=$((reset_at - now))
  if [ "$remaining" -le 0 ]; then printf 'now'; return 0; fi
  days=$((remaining / 86400))
  hours=$(((remaining % 86400) / 3600))
  minutes=$(((remaining % 3600) / 60))
  if [ "$days" -gt 0 ]; then printf '%dd%dh' "$days" "$hours"
  elif [ "$hours" -gt 0 ]; then printf '%dh%dm' "$hours" "$minutes"
  else printf '%dm' "$minutes"; fi
}

render_builtin() {
  local cwd model disp out="" five week five_reset week_reset
  cwd=$(printf '%s' "$input" | jq -r '.workspace.current_dir // .cwd // empty' 2>/dev/null)
  model=$(printf '%s' "$input" | jq -r '.model.display_name // .model.id // empty' 2>/dev/null)
  disp="$cwd"
  case "$disp" in
    "$HOME"/*) disp="~${disp#"$HOME"}" ;;
    "$HOME") disp="~" ;;
  esac
  [ -n "$disp" ] && out="$disp"
  [ -n "$model" ] && out="${out:+$out }$model"

  five=$(printf '%s' "$input" | jq -r '.rate_limits.five_hour.used_percentage // empty' 2>/dev/null)
  week=$(printf '%s' "$input" | jq -r '.rate_limits.seven_day.used_percentage // empty' 2>/dev/null)
  five_reset=$(printf '%s' "$input" | jq -r '.rate_limits.five_hour.resets_at // empty' 2>/dev/null)
  week_reset=$(printf '%s' "$input" | jq -r '.rate_limits.seven_day.resets_at // empty' 2>/dev/null)
  if [ -n "$five" ]; then
    local seg="5h:${five%.*}%"
    local rem; rem=$(format_remaining "$five_reset")
    [ -n "$rem" ] && seg="$seg($rem)"
    out="${out:+$out }$seg"
  fi
  if [ -n "$week" ]; then
    local seg="7d:${week%.*}%"
    local rem; rem=$(format_remaining "$week_reset")
    [ -n "$rem" ] && seg="$seg($rem)"
    out="${out:+$out }$seg"
  fi
  printf '%s' "$out"
}

# now_epoch may be unset if there was no rate_limits payload; fall back for warn.
now_epoch_or_now=${now_epoch:-$(date +%s)}

# ── 2. chain delegation (optional) ──────────────────────────────────────────
chain_cmd=$(jq -r '.statusline_chain.command // empty' "$CONFIG_FILE" 2>/dev/null || true)
if [ -n "$chain_cmd" ]; then
  chain_out=$(printf '%s' "$input" | run_chain 2 sh -c "$chain_cmd" 2>/dev/null)
  chain_rc=$?
  chain_out=$(printf '%s' "$chain_out" | head -c 8192)
  if [ "$chain_rc" -eq 0 ] && [ -n "$chain_out" ]; then
    printf '%s' "$chain_out"
    exit 0
  fi
  warn_chain_failure
fi

render_builtin
