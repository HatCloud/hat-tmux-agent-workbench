#!/bin/bash
#
# setup_sandbox_test.sh — fresh-machine end-to-end sandbox test for scripts/setup
# and scripts/deploy.sh (plan Task 13, the acceptance-test assertions).
#
# HERMETIC BY CONSTRUCTION. Nothing here touches the real HOME, real launchd,
# real ~/.claude/settings.json, or runs real brew/launchctl:
#   * everything runs under HOME=$(mktemp -d)/home with a `git clone --local`
#     of this repo;
#   * a stub bin/ (fake launchctl, brew, go) is prepended to PATH so system
#     mutations are logged, not performed — go is stubbed so `go build` neither
#     compiles nor hits the network (No network dependence guardrail);
#   * every tmux server is confined to a sandbox TMUX_TMPDIR and killed on exit;
#   * children run under `env -i` with an explicit whitelist so a real $CI /
#     $TMUX / provider vars can never leak in.
# The dry-run seam ($HAT_SETUP_DRYRUN) and the HAT_* path overrides (added in
# plan Task 8) are used to intercept the deploy call and redirect config paths.
#
# Usage: /bin/bash scripts/tests/setup_sandbox_test.sh
# Exit 0 iff every assertion passes.

set -uo pipefail

# ---------------------------------------------------------------------------
# Real state we must NOT perturb (captured before anything is set up).
# ---------------------------------------------------------------------------
REAL_HOME="$HOME"
_sum() { [ -f "$1" ] && md5 -q "$1" 2>/dev/null || echo ABSENT; }
_hat_plists() { # list hat-* launchagent basenames, sorted, comma-joined
  local d="$REAL_HOME/Library/LaunchAgents" f out=""
  [ -d "$d" ] || { printf ''; return; }
  for f in "$d"/*[Hh]at*; do
    [ -e "$f" ] && out="$out$(basename "$f"),"
  done
  printf '%s' "$out"
}
REAL_TMUX_CONF_BEFORE="$(_sum "$REAL_HOME/.tmux.conf")"
REAL_SETTINGS_BEFORE="$(_sum "$REAL_HOME/.claude/settings.json")"
REAL_LA_BEFORE="$(_hat_plists)"

SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SELF_DIR/../.." && pwd)"

# ---------------------------------------------------------------------------
# Sandbox skeleton.
# ---------------------------------------------------------------------------
SANDBOX="$(mktemp -d)"
HOME_DIR="$SANDBOX/home"
STUB_BIN="$SANDBOX/stub-bin"
mkdir -p "$HOME_DIR" "$STUB_BIN"
# tmux sockets must live under a SHORT path: macOS caps unix socket sun_path at
# ~104 bytes, and $SANDBOX (/var/folders/.../tmp.XXXX/...) is already deep enough
# that $SANDBOX/tmux/tmux-<uid>/<socket> overflows and every trial-load / scan
# server dies with "File name too long". Keep it short and independent of $HOME.
TMUX_TMPDIR_DIR="$(mktemp -d /tmp/hst.XXXXXX)"

ORIG_PATH="$PATH"
UID_NUM="$(id -u)"
TMUX_SOCK_DIR="$TMUX_TMPDIR_DIR/tmux-$UID_NUM"

cleanup() {
  # Kill any tmux server confined to the sandbox socket dir, then nuke sandbox.
  if [ -d "$TMUX_SOCK_DIR" ]; then
    local s
    for s in "$TMUX_SOCK_DIR"/*; do
      [ -S "$s" ] && tmux -S "$s" kill-server >/dev/null 2>&1
    done
  fi
  [ -n "${SANDBOX:-}" ] && rm -rf "$SANDBOX"
  [ -n "${TMUX_TMPDIR_DIR:-}" ] && rm -rf "$TMUX_TMPDIR_DIR"
}
trap cleanup EXIT

git clone --local --quiet "$REPO_ROOT" "$HOME_DIR/.hat-config" \
  || { echo "FATAL: git clone --local failed"; exit 1; }

SBOX_REPO="$HOME_DIR/.hat-config"

# `git clone --local` only checks out HEAD, so uncommitted edits to
# scripts/setup, deploy.sh, tmux/, agent-tracker/ would NOT be under test —
# a false-green trap when a dev runs this to verify their in-progress changes.
# Overlay the WORKING TREE on top of the clone (keeping the clone's .git for the
# pre-commit-hook install); exclude runtime state + build artefacts.
rsync -a \
  --exclude='.git/' --exclude='/state/' --exclude='/agent-tracker/bin/' \
  "$REPO_ROOT"/ "$SBOX_REPO"/ \
  || { echo "FATAL: working-tree overlay failed"; exit 1; }

SBOX_SETUP="$SBOX_REPO/scripts/setup"
SBOX_DEPLOY="$SBOX_REPO/scripts/deploy.sh"

# ---------------------------------------------------------------------------
# Stub system-command layer.
# ---------------------------------------------------------------------------
cat > "$STUB_BIN/launchctl" <<'STUB'
#!/bin/bash
# Fake launchctl: log every invocation, honour a few probes the deploy path
# depends on. Never talks to the real launchd.
: "${STUB_LOG:?STUB_LOG unset}"
printf 'launchctl %s\n' "$*" >> "$STUB_LOG"
case "${1:-}" in
  bootstrap)
    plist="${3:-}"
    if [ -n "${STUB_FAIL_BOOTSTRAP_NEW:-}" ]; then
      case "$plist" in
        *app.hat-tmux-workbench.agent-tracker.plist) exit 1 ;;
      esac
    fi
    exit 0 ;;
  bootout) exit 0 ;;
  print)
    # Only the (new) active labels report as loaded; old me.hatcloud.* labels
    # report "not loaded" so has_old detection is driven by the on-disk plist.
    case "${2:-}" in
      *app.hat-tmux-workbench.agent-tracker) printf 'state = running\nlast exit code = 0\n'; exit 0 ;;
      *app.hat-tmux-workbench.workspace-save) printf 'state = waiting\n'; exit 0 ;;
      *) exit 1 ;;
    esac ;;
  *) exit 0 ;;
esac
STUB

cat > "$STUB_BIN/brew" <<'STUB'
#!/bin/bash
printf 'brew %s\n' "$*" >> "${STUB_LOG:-/dev/null}"
exit 0
STUB

cat > "$STUB_BIN/go" <<'STUB'
#!/bin/bash
# Fake go: satisfy preflight `go version` and make `go build -o <path>` emit a
# harmless runnable stub instead of compiling (no network, no build cache).
case "${1:-}" in
  version) echo "go version go1.23.0 darwin/arm64"; exit 0 ;;
  build)
    out=""
    shift
    while [ "$#" -gt 0 ]; do
      [ "$1" = "-o" ] && { out="${2:-}"; shift 2; continue; }
      shift
    done
    if [ -n "$out" ]; then
      mkdir -p "$(dirname "$out")"
      printf '#!/bin/bash\nexit 0\n' > "$out"
      chmod +x "$out"
    fi
    exit 0 ;;
  *) exit 0 ;;
esac
STUB

cat > "$STUB_BIN/ssh" <<'STUB'
#!/bin/bash
printf 'ssh %s\n' "$*" >> "${STUB_LOG:-/dev/null}"
exit 0
STUB

cat > "$STUB_BIN/rsync" <<'STUB'
#!/bin/bash
printf 'rsync %s\n' "$*" >> "${STUB_LOG:-/dev/null}"
exit 0
STUB

chmod +x "$STUB_BIN/launchctl" "$STUB_BIN/brew" "$STUB_BIN/go" \
  "$STUB_BIN/ssh" "$STUB_BIN/rsync"

# ---------------------------------------------------------------------------
# Invocation harness — clean env whitelist; stub bin first on PATH.
# ---------------------------------------------------------------------------
BASE_ENV=(
  "HOME=$HOME_DIR"
  "PATH=$STUB_BIN:$ORIG_PATH"
  "TMUX_TMPDIR=$TMUX_TMPDIR_DIR"
  "TERM=${TERM:-xterm}"
  "LANG=en_US.UTF-8"
)
run() { env -i "${BASE_ENV[@]}" "$@"; }

# ---------------------------------------------------------------------------
# Assertion plumbing.
# ---------------------------------------------------------------------------
PASS=0
FAIL=0
FAILED_LIST=()
pass() { PASS=$((PASS + 1)); printf '  PASS  %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); FAILED_LIST+=("$1"); printf '  FAIL  %s\n' "$1"; }
check() { # check <desc> <bool: 0=pass>
  if [ "$2" -eq 0 ]; then pass "$1"; else fail "$1"; fi
}
contains() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }

section() { printf '\n=== %s ===\n' "$1"; }

# ===========================================================================
# AT1 — fresh-machine non-interactive JSON run: valid JSONL, no external writes.
# ===========================================================================
section "AT1 non-interactive JSON, no writes outside sandbox"
find "$HOME_DIR" | sort > "$SANDBOX/snap.before"
at1_out="$(run HAT_SETUP_DRYRUN=1 STUB_LOG="$SANDBOX/at1.log" \
  /bin/bash "$SBOX_SETUP" --non-interactive --json --deps=check 2>/dev/null)"
at1_rc=$?
printf '%s\n' "$at1_out" > "$SANDBOX/at1.stdout"
find "$HOME_DIR" | sort > "$SANDBOX/snap.after"

check "AT1 exit code is 0" "$([ "$at1_rc" -eq 0 ] && echo 0 || echo 1)"

alljson=0
while IFS= read -r line; do
  if [ -z "$line" ] || ! printf '%s' "$line" | jq -e . >/dev/null 2>&1; then
    alljson=1; break
  fi
done < "$SANDBOX/at1.stdout"
check "AT1 every stdout line is valid JSON" "$alljson"

if tail -n1 "$SANDBOX/at1.stdout" | jq -e '.result' >/dev/null 2>&1; then
  check "AT1 last stdout line carries .result" 0
else
  check "AT1 last stdout line carries .result" 1
fi

# Snapshot diff: new paths must stay under the sandbox config dir; nothing removed.
newpaths="$(comm -13 "$SANDBOX/snap.before" "$SANDBOX/snap.after")"
removed="$(comm -23 "$SANDBOX/snap.before" "$SANDBOX/snap.after" | grep -v '^$')"
badnew=""
while IFS= read -r p; do
  [ -z "$p" ] && continue
  case "$p" in
    "$HOME_DIR"/.config|"$HOME_DIR"/.config/*) : ;;
    *) badnew="$badnew$p"$'\n' ;;
  esac
done <<< "$newpaths"
if [ -n "$badnew" ]; then
  printf '    unexpected new paths:\n%s' "$badnew"
fi
check "AT1 new paths confined to sandbox \$HOME/.config" "$([ -z "$badnew" ] && echo 0 || echo 1)"
check "AT1 no sandbox paths removed" "$([ -z "$removed" ] && echo 0 || echo 1)"

# ===========================================================================
# AT8 — agent-guide contract shape.
# ===========================================================================
section "AT8 agent-guide contract"
if run /bin/bash "$SBOX_SETUP" agent-guide 2>/dev/null \
   | jq -e '.flags and .decision_points and .output_schema' >/dev/null 2>&1; then
  check "AT8 agent-guide has flags + decision_points + output_schema" 0
else
  check "AT8 agent-guide has flags + decision_points + output_schema" 1
fi

# ===========================================================================
# AT11 (mapping layer, dry-run) — skip-decision → deploy flag mapping.
# ===========================================================================
section "AT11 mapping layer (dry-run)"
deploy_cmd_for() { # <extra setup flags...> -> printed deploy.sh command line
  local cfg="$SANDBOX/map-cfg.json"
  rm -f "$cfg"
  run HAT_SETUP_DRYRUN=1 STUB_LOG="$SANDBOX/map.log" HAT_AGENT_CONFIG="$cfg" \
    /bin/bash "$SBOX_SETUP" --non-interactive --json --deps=skip "$@" 2>/dev/null \
    | jq -r 'select(.step=="deploy")|.detail.command'
}

for pair in \
  "tmux:--skip-tmux" \
  "daemon:--skip-daemon" \
  "ws-timer:--skip-ws-timer" \
  "stop-hook:--skip-stop-hook" \
  "statusline:--skip-statusline" \
  "alias:--skip-alias"; do
  name="${pair%%:*}"; skipflag="${pair#*:}"
  cmd_install="$(deploy_cmd_for "--$name=install")"
  cmd_skip="$(deploy_cmd_for "--$name=skip")"
  if contains "$skipflag" "$cmd_install"; then
    check "AT11 --$name=install omits $skipflag" 1
  else
    check "AT11 --$name=install omits $skipflag" 0
  fi
  if contains "$skipflag" "$cmd_skip"; then
    check "AT11 --$name=skip includes $skipflag" 0
  else
    check "AT11 --$name=skip includes $skipflag" 1
  fi
done

# statusline=chain: config gets a statusline_chain object + deploy registers.
FIX="$SANDBOX/fixtures/user_statusline.sh"
mkdir -p "$SANDBOX/fixtures"
printf '#!/bin/bash\necho hi\n' > "$FIX"; chmod +x "$FIX"
chain_cfg="$SANDBOX/chain-cfg.json"; rm -f "$chain_cfg"
chain_set="$SANDBOX/chain-settings.json"
printf '{"statusLine":{"type":"command","command":"%s"}}\n' "$FIX" > "$chain_set"
chain_out="$(run HAT_SETUP_DRYRUN=1 STUB_LOG="$SANDBOX/chain.log" \
  HAT_AGENT_CONFIG="$chain_cfg" HAT_CLAUDE_SETTINGS="$chain_set" \
  /bin/bash "$SBOX_SETUP" --non-interactive --json --deps=skip --statusline=chain 2>/dev/null)"
chain_cmd="$(printf '%s\n' "$chain_out" | jq -r 'select(.step=="deploy")|.detail.command')"
if jq -e '(.statusline_chain|type=="object") and .statusline_chain.command and .statusline_chain.original' \
   "$chain_cfg" >/dev/null 2>&1; then
  check "AT11 --statusline=chain writes statusline_chain object to config" 0
else
  check "AT11 --statusline=chain writes statusline_chain object to config" 1
fi
if contains "--skip-statusline" "$chain_cmd"; then
  check "AT11 --statusline=chain deploy line registers (no --skip-statusline)" 1
else
  check "AT11 --statusline=chain deploy line registers (no --skip-statusline)" 0
fi

# ===========================================================================
# AT11 (real-action layer) — actually write the tmux block; daemon via stub.
# ===========================================================================
section "AT11 real-action layer"
TMUX_START_MARKER="# >>> hat-config managed tmux"

run STUB_LOG="$SANDBOX/real-tmux.log" /bin/bash "$SBOX_DEPLOY" install \
  --skip-daemon --skip-ws-timer --skip-stop-hook --skip-statusline --skip-alias \
  --no-reload >/dev/null 2>&1
if [ -f "$HOME_DIR/.tmux.conf" ] && grep -Fq "$TMUX_START_MARKER" "$HOME_DIR/.tmux.conf"; then
  check "AT11 deploy install --tmux writes managed block to ~/.tmux.conf" 0
else
  check "AT11 deploy install --tmux writes managed block to ~/.tmux.conf" 1
fi
if grep -Fq 'rsync ' "$SANDBOX/real-tmux.log" 2>/dev/null \
   && grep -Fq 'mini:.hat-config/' "$SANDBOX/real-tmux.log" 2>/dev/null; then
  check "AT11 reachable mini receives a project sync" 0
else
  check "AT11 reachable mini receives a project sync" 1
fi
if grep -Fq 'PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin' \
     "$SANDBOX/real-tmux.log" 2>/dev/null \
   && grep -Fq 'HAT_CONFIG_SKIP_MINI_SYNC=1' "$SANDBOX/real-tmux.log" 2>/dev/null \
   && grep -Fq 'scripts/deploy.sh update --yes' "$SANDBOX/real-tmux.log" 2>/dev/null; then
  check "AT11 mini runs deploy update with toolchain PATH and recursion disabled" 0
else
  check "AT11 mini runs deploy update with toolchain PATH and recursion disabled" 1
fi

run STUB_LOG="$SANDBOX/real-daemon.log" /bin/bash "$SBOX_DEPLOY" install \
  --skip-tmux --skip-ws-timer --skip-stop-hook --skip-statusline --skip-alias \
  --no-reload >/dev/null 2>&1
if grep -Eq 'launchctl bootstrap .*app\.hat-tmux-workbench\.agent-tracker\.plist' \
   "$SANDBOX/real-daemon.log"; then
  check "AT11 deploy install --daemon bootstraps the new label (stub log)" 0
else
  check "AT11 deploy install --daemon bootstraps the new label (stub log)" 1
fi

run STUB_LOG="$SANDBOX/real-tmux-un.log" /bin/bash "$SBOX_DEPLOY" uninstall \
  --skip-daemon --skip-ws-timer --skip-stop-hook --skip-statusline --skip-alias \
  --keep-state --yes --no-reload >/dev/null 2>&1
if [ -f "$HOME_DIR/.tmux.conf" ] && grep -Fq "$TMUX_START_MARKER" "$HOME_DIR/.tmux.conf"; then
  check "AT11 deploy uninstall removes the managed block symmetrically" 1
else
  check "AT11 deploy uninstall removes the managed block symmetrically" 0
fi

# ===========================================================================
# AT9 — label-migration rollback when the new bootstrap fails.
# ===========================================================================
section "AT9 label-migration rollback"
OLD_PLIST="$HOME_DIR/Library/LaunchAgents/me.hatcloud.agent-tracker.plist"
mkdir -p "$(dirname "$OLD_PLIST")"
cat > "$OLD_PLIST" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>me.hatcloud.agent-tracker</string></dict></plist>
PLIST
AT9_LOG="$SANDBOX/at9.log"
run STUB_LOG="$AT9_LOG" STUB_FAIL_BOOTSTRAP_NEW=1 /bin/bash "$SBOX_DEPLOY" install \
  --skip-tmux --skip-ws-timer --skip-stop-hook --skip-statusline --skip-alias \
  --no-reload >/dev/null 2>&1
at9_rc=$?
check "AT9 deploy install exits non-zero on failed migration" \
  "$([ "$at9_rc" -ne 0 ] && echo 0 || echo 1)"
# The rollback must re-bootstrap the backed-up OLD plist AFTER the failed new one.
new_ln="$(grep -n 'bootstrap .*app\.hat-tmux-workbench\.agent-tracker\.plist' "$AT9_LOG" | head -n1 | cut -d: -f1)"
old_ln="$(grep -n 'bootstrap .*backup/me\.hatcloud\.agent-tracker\.plist' "$AT9_LOG" | head -n1 | cut -d: -f1)"
if [ -n "$new_ln" ] && [ -n "$old_ln" ] && [ "$old_ln" -gt "$new_ln" ]; then
  check "AT9 rollback re-bootstraps the OLD backup plist after the failed new one" 0
else
  check "AT9 rollback re-bootstraps the OLD backup plist after the failed new one" 1
fi
rm -f "$OLD_PLIST"

# ===========================================================================
# AT10 — alias fallback to ~/.zshrc when ~/.hat-env is absent, removed on uninstall.
# ===========================================================================
section "AT10 alias fallback to ~/.zshrc"
ALIAS_MARKER="# >>> hat-tmux-agent-workbench aliases"
[ -e "$HOME_DIR/.hat-env" ] && rm -rf "$HOME_DIR/.hat-env"
printf 'y\n' | run STUB_LOG="$SANDBOX/at10.log" /bin/bash "$SBOX_DEPLOY" install \
  --skip-tmux --skip-daemon --skip-ws-timer --skip-stop-hook --skip-statusline \
  --no-reload >/dev/null 2>&1
if [ -f "$HOME_DIR/.zshrc" ] && grep -Fq "$ALIAS_MARKER" "$HOME_DIR/.zshrc"; then
  check "AT10 alias install writes managed block to ~/.zshrc" 0
else
  check "AT10 alias install writes managed block to ~/.zshrc" 1
fi
run STUB_LOG="$SANDBOX/at10.log" /bin/bash "$SBOX_DEPLOY" uninstall \
  --skip-tmux --skip-daemon --skip-ws-timer --skip-stop-hook --skip-statusline \
  --keep-state --yes --no-reload >/dev/null 2>&1
if [ -f "$HOME_DIR/.zshrc" ] && grep -Fq "$ALIAS_MARKER" "$HOME_DIR/.zshrc"; then
  check "AT10 alias uninstall removes the ~/.zshrc block symmetrically" 1
else
  check "AT10 alias uninstall removes the ~/.zshrc block symmetrically" 0
fi

# ===========================================================================
# AT13 — keymap injection defense (grammar rejects dangerous keys).
# ===========================================================================
section "AT13 keymap injection defense"
# (a) direct grammar assertions (covers the newline case that stdin/read cannot feed).
grammar_rejects() { # <key> ; returns 0 when keymap_valid_key REJECTS it
  ! run bash -c 'source "$1" >/dev/null 2>&1; keymap_valid_key "$2"' _ "$SBOX_SETUP" "$1"
}
grammar_accepts() { # <key> ; returns 0 when keymap_valid_key ACCEPTS it
  run bash -c 'source "$1" >/dev/null 2>&1; keymap_valid_key "$2"' _ "$SBOX_SETUP" "$1"
}
mal_desc=(";" "bare-backslash" "backslash-semicolon" "newline" "double-quote" "semicolon-injection")
mal_keys=(";" "\\" "\\;" $'\n' '"' '];kill-server')
i=0
while [ "$i" -lt "${#mal_keys[@]}" ]; do
  if grammar_rejects "${mal_keys[$i]}"; then
    check "AT13 grammar rejects ${mal_desc[$i]}" 0
  else
    check "AT13 grammar rejects ${mal_desc[$i]}" 1
  fi
  i=$((i + 1))
done
# anti-tautology: valid keys must be accepted.
for good in M-z F10 "]" M-s; do
  if grammar_accepts "$good"; then
    check "AT13 grammar accepts valid key '$good'" 0
  else
    check "AT13 grammar accepts valid key '$good'" 1
  fi
done

# (b) end-to-end: drive the per-binding wizard, feed the dangerous keys via stdin.
printf 'bind ] paste-buffer\n' > "$HOME_DIR/.tmux.conf"   # forces a prefix-] conflict
AT13_KM="$SANDBOX/at13-keymap.conf"; rm -f "$AT13_KM"
{ printf '%s\n' ';' '\' '\;' '"' '' 'n'; } > "$SANDBOX/at13.in"
run HAT_SETUP_DRYRUN=1 STUB_LOG="$SANDBOX/at13.log" HAT_KEYMAP_CONF="$AT13_KM" \
  /bin/bash "$SBOX_SETUP" --interactive --lang=en --icons=nerd --deps=skip --keymap=compat \
  --tmux=skip --daemon=skip --ws-timer=skip --stop-hook=skip --statusline=skip --alias=skip \
  < "$SANDBOX/at13.in" > "$SANDBOX/at13.out" 2>&1

if [ -f "$AT13_KM" ]; then
  check "AT13 wizard generated a keymap overlay" 0
  # Every bind key in the overlay must be grammar-valid and none of the injected tokens.
  badkey=""
  while IFS= read -r k; do
    [ -z "$k" ] && continue
    case "$k" in
      ';'|'\'|'\;'|'"') badkey="${badkey}[$k]" ; continue ;;
    esac
    grammar_accepts "$k" || badkey="${badkey}[$k]"
  done < <(awk '/^bind /{
      i=2
      while (i<=NF) {
        if ($i=="-n") { i++; continue }
        if ($i=="-T") { i+=2; continue }
        if (substr($i,1,1)=="-") { i++; continue }
        break
      }
      if (i<=NF) print $i
    }' "$AT13_KM")
  check "AT13 overlay contains no injected/invalid bind keys" \
    "$([ -z "$badkey" ] && echo 0 || echo 1)"
  [ -n "$badkey" ] && printf '    offending keys: %s\n' "$badkey"
else
  check "AT13 wizard generated a keymap overlay" 1
  check "AT13 overlay contains no injected/invalid bind keys" 1
fi

# ===========================================================================
# AT18 — keymap conflict detection + compat overlay trial-loads cleanly.
# ===========================================================================
section "AT18 keymap conflict + clean trial-load"
printf 'bind ] paste-buffer\n' > "$HOME_DIR/.tmux.conf"
AT18_KM="$SANDBOX/at18-keymap.conf"; rm -f "$AT18_KM"
at18_err="$(run HAT_SETUP_DRYRUN=1 STUB_LOG="$SANDBOX/at18.log" HAT_KEYMAP_CONF="$AT18_KM" \
  /bin/bash "$SBOX_SETUP" --non-interactive --lang=en --icons=nerd --deps=skip --keymap=compat \
  --tmux=skip --daemon=skip --ws-timer=skip --stop-hook=skip --statusline=skip --alias=skip 2>&1 1>/dev/null)"
if contains "conflicting" "$at18_err" && contains "new_here" "$at18_err"; then
  check "AT18 prefix-] conflict is detected" 0
else
  check "AT18 prefix-] conflict is detected" 1
fi

# A grammar-valid, free replacement is suggested for the occupied key.
at18_sug="$(run bash -c '
  source "$1" >/dev/null 2>&1
  keymap_build_table
  KM_OCCUPIED="prefix:]"
  keymap_suggest' _ "$SBOX_SETUP")"
if [ -n "$at18_sug" ] && [ "$at18_sug" != "]" ] && grammar_accepts "$at18_sug"; then
  check "AT18 a valid replacement key is suggested ($at18_sug)" 0
else
  check "AT18 a valid replacement key is suggested ($at18_sug)" 1
fi

# compat overlay trial-loads cleanly in a detached, random-socket server.
if [ -f "$AT18_KM" ]; then
  SOCK="at18-$$-$RANDOM"
  run tmux -L "$SOCK" new-session -d -x 80 -y 24 >/dev/null 2>&1
  run tmux -L "$SOCK" source-file "$SBOX_REPO/tmux/tmux.conf" >/dev/null 2>&1; a=$?
  run tmux -L "$SOCK" source-file "$AT18_KM" >/dev/null 2>&1; b=$?
  run tmux -L "$SOCK" kill-server >/dev/null 2>&1
  if [ "$a" -eq 0 ] && [ "$b" -eq 0 ]; then
    check "AT18 compat overlay trial-loads cleanly (detached server)" 0
  else
    check "AT18 compat overlay trial-loads cleanly (detached server)" 1
  fi
else
  check "AT18 compat overlay trial-loads cleanly (detached server)" 1
fi

# ===========================================================================
# Real-state spot check — nothing outside the sandbox changed.
# ===========================================================================
section "Real-state spot check (outside sandbox untouched)"
check "real ~/.tmux.conf unchanged" \
  "$([ "$(_sum "$REAL_HOME/.tmux.conf")" = "$REAL_TMUX_CONF_BEFORE" ] && echo 0 || echo 1)"
check "real ~/.claude/settings.json unchanged" \
  "$([ "$(_sum "$REAL_HOME/.claude/settings.json")" = "$REAL_SETTINGS_BEFORE" ] && echo 0 || echo 1)"
REAL_LA_AFTER="$(_hat_plists)"
check "real ~/Library/LaunchAgents hat-plists unchanged" \
  "$([ "$REAL_LA_AFTER" = "$REAL_LA_BEFORE" ] && echo 0 || echo 1)"

# ===========================================================================
# Summary.
# ===========================================================================
printf '\n============================================================\n'
printf 'RESULT: %d passed, %d failed\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  printf 'Failed assertions:\n'
  for f in "${FAILED_LIST[@]}"; do printf '  - %s\n' "$f"; done
  exit 1
fi
printf 'ALL ASSERTIONS PASSED\n'
exit 0
