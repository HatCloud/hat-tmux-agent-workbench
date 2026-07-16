#!/bin/bash
# tmux/scripts/agent 内 _abbrev_dir 的回归测试。与 Go 侧 TestAbbrevPath
# （cmd/agent/abbrev_path_test.go）使用同一组用例——两者是同一条规则的
# 双语镜像实现，改一侧必须两个测试都过。
set -u

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)
AGENT="$SCRIPT_DIR/../../tmux/scripts/agent"

# 从脚本源文本抽出函数定义再 source，避免执行脚本主体。
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
sed -n '/^_abbrev_dir()/,/^}/p' "$AGENT" > "$TMP/fn.sh"
# shellcheck disable=SC1091
source "$TMP/fn.sh"

fail=0
check() {
	local in="$1" want="$2" got
	got=$(_abbrev_dir "$in")
	if [[ "$got" == "$want" ]]; then
		printf 'PASS: _abbrev_dir %s -> %s\n' "$in" "$got"
	else
		printf 'FAIL: _abbrev_dir %s -> %s (want %s)\n' "$in" "$got" "$want"
		fail=1
	fi
}

check "$HOME/Projects/foo" "~/P/foo"
check "$HOME/.hat-config/x" "~/.h/x"
check "$HOME/a/b/c" "~/a/b/c"
check "$HOME/foo" "~/foo"
check "/usr/local/bin" "/u/l/bin"
check "/tmp" "/tmp"

exit "$fail"
