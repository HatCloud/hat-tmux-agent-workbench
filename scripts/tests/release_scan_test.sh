#!/bin/bash
# Assertions for scripts/release-scan.sh — the pre-publish release gate.
# Builds throwaway git repos in a sandbox; never touches the real repo.
set -u

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)
SCANNER="$SCRIPT_DIR/../release-scan.sh"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

fail=0
pass() { printf 'PASS: %s\n' "$1"; }
die() {
	printf 'FAIL: %s\n' "$1"
	fail=1
}

# mkrepo <dir>: init a git repo with a committed identity, echo its path.
mkrepo() {
	local d="$1"
	mkdir -p "$d"
	git -C "$d" init -q
	git -C "$d" config user.email a@b
	git -C "$d" config user.name tester
	printf '%s' "$d"
}
commit() { git -C "$1" add -A && git -C "$1" commit -q -m x; }

# Personal-identifier fixtures are assembled from fragments so this test file
# itself contains no verbatim literal (else the release gate would flag the very
# test that verifies it — the same self-reference the scanner avoids via [x]
# char classes). Adjacent quoted strings concatenate at runtime.
ID_USER="hat""_cloud"                  # runtime: maintainer unix username
ID_USER_UP="Hat""_Cloud"               # runtime: uppercase variant (case-insensitive)
ID_EMAIL="mr.""hatcloud@gmail.com"     # runtime: maintainer email
ID_HOME="/Users/""hat""_cloud"         # runtime: maintainer home path

# ── 1. self-exclusion: a repo whose only file is the scanner itself → exit 0 ──
# This is the regression guard: the scanner must NOT flag its own pattern list.
r=$(mkrepo "$TMP/selftest")
mkdir -p "$r/scripts"
cp "$SCANNER" "$r/scripts/release-scan.sh"
commit "$r"
if "$SCANNER" --repo "$r" >/dev/null 2>&1; then
	pass "① scanner does not self-flag its own pattern list (exit 0)"
else
	die "① scanner self-flags: $("$SCANNER" --repo "$r" 2>&1 | grep -E '^\s+\[' | head -3)"
fi

# ── 2. clean repo → exit 0 ──
r=$(mkrepo "$TMP/clean")
echo "hello world" >"$r/readme.txt"
commit "$r"
"$SCANNER" --repo "$r" >/dev/null 2>&1 && pass "② clean repo exit 0" || die "② clean repo should exit 0"

# ── 3. personal id in content → exit 1 ──
r=$(mkrepo "$TMP/dirty")
printf 'contact %s and user %s\n' "$ID_EMAIL" "$ID_USER" >"$r/notes.txt"
commit "$r"
"$SCANNER" --repo "$r" >/dev/null 2>&1 && die "③ personal id not detected" || pass "③ personal id → exit 1"

# ── 4. case-insensitive: uppercase username variant caught ──
r=$(mkrepo "$TMP/case")
printf 'author %s\n' "$ID_USER_UP" >"$r/f.txt"
commit "$r"
"$SCANNER" --repo "$r" >/dev/null 2>&1 && die "④ uppercase username variant not caught" || pass "④ case-insensitive username variant → exit 1"

# ── 5. CJK filename content scanned ──
r=$(mkrepo "$TMP/cjk")
mkdir -p "$r/笔记"
printf 'has %s inside\n' "$ID_USER" >"$r/笔记/个人.txt"
commit "$r"
"$SCANNER" --repo "$r" >/dev/null 2>&1 && die "⑤ CJK-filename content skipped (false negative)" || pass "⑤ CJK filename content scanned → exit 1"

# ── 6. brand HatCloud (no underscore) NOT flagged ──
r=$(mkrepo "$TMP/brand")
printf 'Copyright HatCloud\ngithub.com/HatCloud/repo\n' >"$r/LICENSE"
commit "$r"
"$SCANNER" --repo "$r" >/dev/null 2>&1 && pass "⑥ brand HatCloud not flagged (exit 0)" || die "⑥ brand HatCloud falsely flagged"

# ── 7. --json output is valid JSON ──
r=$(mkrepo "$TMP/jsonrepo")
printf 'user %s\n' "$ID_USER" >"$r/f.txt"
commit "$r"
if "$SCANNER" --repo "$r" --json 2>/dev/null | jq -e '.hit_count and .hits' >/dev/null 2>&1; then
	pass "⑦ --json emits valid JSON with hit_count/hits"
else
	die "⑦ --json output not valid"
fi

# ── 8. benign /Users/me fixture NOT flagged, real maintainer home IS ──
r=$(mkrepo "$TMP/homepath")
printf 'fixture path /Users/me/project\n' >"$r/f.txt"
commit "$r"
"$SCANNER" --repo "$r" >/dev/null 2>&1 && pass "⑧a /Users/me fixture not flagged (exit 0)" || die "⑧a /Users/me falsely flagged (gate would never pass)"
r=$(mkrepo "$TMP/realhome")
printf 'real home %s/x\n' "$ID_HOME" >"$r/f.txt"
commit "$r"
"$SCANNER" --repo "$r" >/dev/null 2>&1 && die "⑧b maintainer home path not flagged" || pass "⑧b real maintainer home path → exit 1"

if [ "$fail" -eq 0 ]; then
	printf '\nALL PASS\n'
	exit 0
else
	printf '\nSOME FAILED\n'
	exit 1
fi
