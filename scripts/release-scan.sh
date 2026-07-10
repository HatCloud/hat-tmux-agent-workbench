#!/bin/bash
#
# release-scan.sh — pre-publish release gate scanner.
#
# Greps the git HEAD tree (committed content, NOT the working tree) and commit
# metadata for personal identifiers that must not ship in a public open-source
# release. Read-only: it never modifies the repo.
#
# The PUBLIC brand `HatCloud` (GitHub account/repo, LICENSE copyright) is
# intentionally NOT flagged — only true personal identifiers are.
#
# Exit 0 = clean, exit 1 = at least one hit (do not swallow this).
#
# Usage:
#   scripts/release-scan.sh [--json] [--patterns-file FILE] [--repo DIR]
#
set -euo pipefail

JSON=0
PATTERNS_FILE=""
REPO_DIR=""

usage() {
  cat <<'EOF'
Usage: release-scan.sh [options]

Scans the git HEAD tree + commit metadata for personal identifiers.

Options:
  --json                 Emit machine-readable JSON instead of text.
  --patterns-file FILE   Append extra ERE patterns (one per line, '#' comments
                         and blank lines ignored) to the built-in list.
  --repo DIR             Repository to scan (default: current directory).
  -h, --help             Show this help.

Exit status: 0 = no hits, 1 = at least one hit.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --json) JSON=1; shift ;;
    --patterns-file) PATTERNS_FILE="${2:-}"; shift 2 ;;
    --repo) REPO_DIR="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "release-scan: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ -n "$REPO_DIR" ]; then
  cd "$REPO_DIR"
fi

if ! git rev-parse --git-dir >/dev/null 2>&1; then
  echo "release-scan: not a git repository" >&2
  exit 2
fi

if ! git rev-parse --verify HEAD >/dev/null 2>&1; then
  echo "release-scan: HEAD has no commits" >&2
  exit 2
fi

# --- Pattern list -----------------------------------------------------------
# ERE patterns. Only TRUE personal identifiers — the brand `HatCloud`
# (no underscore) is deliberately absent so the gate can pass.
#
# Self-exclusion: any pattern that is a plain literal (no regex metachar between
# its chars) would match its OWN definition line when this tracked file is
# scanned, so the gate could never reach zero. Such literals wrap one char in a
# `[x]` char class — matches the id elsewhere, but the verbatim literal never
# appears in THIS file. Patterns that already contain a metachar (`\.`, `[a-z_]`)
# don't self-match (the backslash/bracket breaks the literal), so they're left as-is.
PATTERNS=(
  'hat[_]cloud'        # local unix username (underscore)
  'mr\.hatcloud@'      # personal email localpart
  'claude0[2]@'        # personal email localpart
  'linear\.app'        # internal issue tracker
  '[d]68035b5'         # Linear project UUID prefix
  '[c]ac83401'         # Linear team UUID prefix
  '/Users/hat[_]cloud' # the maintainer's absolute home path (a bare /Users/[a-z_]+
                       # would false-flag benign fixtures like /Users/me and never
                       # let the gate reach zero; the real home is targeted here)
)

# Extra patterns from --patterns-file (one ERE per line).
if [ -n "$PATTERNS_FILE" ]; then
  if [ ! -f "$PATTERNS_FILE" ]; then
    echo "release-scan: patterns file not found: $PATTERNS_FILE" >&2
    exit 2
  fi
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      ''|'#'*) continue ;;
    esac
    PATTERNS+=("$line")
  done < "$PATTERNS_FILE"
fi

# Single combined alternation for one-pass grep on content/metadata.
COMBINED=""
for p in "${PATTERNS[@]}"; do
  if [ -z "$COMBINED" ]; then
    COMBINED="$p"
  else
    COMBINED="$COMBINED|$p"
  fi
done

# --- Hit accumulation -------------------------------------------------------
# Each hit is one record: surface \t file \t line \t pattern-context
HITS_FILE="$(mktemp -t release-scan.XXXXXX)"
trap 'rm -f "$HITS_FILE"' EXIT

record_hit() {
  # surface, file, line, text
  printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" >> "$HITS_FILE"
}

# ① Filenames --------------------------------------------------------------
while IFS= read -r name; do
  [ -n "$name" ] || continue
  if printf '%s' "$name" | grep -Eqi "$COMBINED"; then
    match="$(printf '%s' "$name" | grep -Eoi "$COMBINED" | head -1)"
    record_hit "filename" "$name" "0" "$match"
  fi
done < <(git -c core.quotepath=false ls-tree -r --name-only HEAD)

# ② Content ----------------------------------------------------------------
# Scan each tracked text file's committed content. grep -I skips binaries;
# go.sum is excluded (hash noise). git show streams committed blobs, so the
# working tree is never consulted.
while IFS= read -r f; do
  [ -n "$f" ] || continue
  case "$f" in
    go.sum|*/go.sum) continue ;;
  esac
  # grep -I => skip binary; -n => line number; -o via a second pass for context.
  while IFS= read -r hitline; do
    [ -n "$hitline" ] || continue
    lineno="${hitline%%:*}"
    match="$(printf '%s' "${hitline#*:}" | grep -Eoi "$COMBINED" | head -1)"
    record_hit "content" "$f" "$lineno" "$match"
  done < <(git show "HEAD:$f" 2>/dev/null | grep -InEi "$COMBINED" || true)
done < <(git -c core.quotepath=false ls-tree -r --name-only HEAD)

# ③ Commit metadata --------------------------------------------------------
# Author/committer name+email and signing key across all commits.
SIGNED_COUNT=0
TOTAL_COMMITS=0
# SHA is tab-separated from the scanned fields so a hit reports which commit to
# fix; only name/email/key are matched (the 40-hex SHA itself must not be able to
# false-match a UUID pattern).
while IFS="$(printf '\t')" read -r sha meta; do
  TOTAL_COMMITS=$((TOTAL_COMMITS + 1))
  if printf '%s' "$meta" | grep -Eqi "$COMBINED"; then
    match="$(printf '%s' "$meta" | grep -Eoi "$COMBINED" | head -1)"
    record_hit "commit-meta" "$sha" "0" "$match"
  fi
done < <(git log --format='%H%x09%an %ae %cn %ce %GK' HEAD)

# Signature presence note (independent of hits): count commits carrying a key.
SIGNED_COUNT="$(git log --format='%GK' HEAD | grep -c '[^[:space:]]' || true)"

# --- Report -----------------------------------------------------------------
HIT_COUNT=0
if [ -s "$HITS_FILE" ]; then
  HIT_COUNT="$(wc -l < "$HITS_FILE" | tr -d ' ')"
fi

if [ "$JSON" -eq 1 ]; then
  # Hand-rolled JSON (no jq dependency).
  json_escape() {
    local s="$1"
    s="${s//\\/\\\\}"
    s="${s//\"/\\\"}"
    s="${s//	/\\t}"
    printf '%s' "$s"
  }
  printf '{\n'
  printf '  "hit_count": %s,\n' "$HIT_COUNT"
  printf '  "total_commits": %s,\n' "$TOTAL_COMMITS"
  printf '  "signed_commits": %s,\n' "$SIGNED_COUNT"
  printf '  "hits": [\n'
  first=1
  if [ -s "$HITS_FILE" ]; then
    while IFS=$'\t' read -r surface file line text; do
      if [ "$first" -eq 1 ]; then first=0; else printf ',\n'; fi
      printf '    {"surface": "%s", "file": "%s", "line": %s, "match": "%s"}' \
        "$(json_escape "$surface")" "$(json_escape "$file")" \
        "${line:-0}" "$(json_escape "$text")"
    done < "$HITS_FILE"
    printf '\n'
  fi
  printf '  ]\n'
  printf '}\n'
else
  if [ "$HIT_COUNT" -eq 0 ]; then
    echo "release-scan: clean — no personal identifiers found in HEAD tree."
    echo "release-scan: scanned $TOTAL_COMMITS commit(s); $SIGNED_COUNT signed."
  else
    echo "release-scan: FOUND $HIT_COUNT hit(s) — release gate BLOCKED."
    echo "----------------------------------------------------------------"
    while IFS=$'\t' read -r surface file line text; do
      case "$surface" in
        content)  printf '  [%s] %s:%s:%s\n' "$surface" "$file" "$line" "$text" ;;
        filename) printf '  [%s] %s (pattern: %s)\n' "$surface" "$file" "$text" ;;
        *)        printf '  [%s] %s (pattern: %s)\n' "$surface" "$file" "$text" ;;
      esac
    done < "$HITS_FILE"
    echo "----------------------------------------------------------------"
    echo "release-scan: $SIGNED_COUNT/$TOTAL_COMMITS commit(s) carry a signature."
  fi
fi

if [ "$HIT_COUNT" -gt 0 ]; then
  exit 1
fi
exit 0
