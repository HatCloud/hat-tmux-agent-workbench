#!/bin/bash
# tmux/scripts/open_urls.sh --extract 段的回归测试：URL/文件/文件夹分类、
# 相对路径按 base 解析、:line:col 剥离、/dev 类噪声排除、payload 去重、
# 最近出现在前的排序。显式用 bash 跑（脚本依赖 BASH_REMATCH，zsh 下为空）。
set -u

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)
OPEN_URLS="$SCRIPT_DIR/../../tmux/scripts/open_urls.sh"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/subdir"
printf 'hi\n' > "$TMP/real.txt"
printf 'x\n' > "$TMP/app.py"
printf 'd\n' > "$TMP/subdir/deep.txt"

text="首行 https://example.com/a?b=1 和 www.foo.org 链接
配置在 /dev/null 与 ./real.txt 以及 /etc/hosts
错误 app.py:12:3 目录 subdir 绝对写法 $TMP/app.py:7
引用（\`subdir/deep.txt:5\`）多段相对路径
不存在 nope/ghost.py 裸词 real.txt"

# 文件夹条目受设置开关控制（默认 off）；测试用 env 覆盖保证与本机配置无关
out=$(printf '%s\n' "$text" | OPEN_URLS_SHOW_DIRS=on bash "$OPEN_URLS" --extract "$TMP")

fail=0
expect() { # expect <描述> <应精确存在的整行>
	local desc="$1" want="$2"
	if grep -qxF "$want" <<< "$out"; then
		printf 'PASS: %s\n' "$desc"
	else
		printf 'FAIL: %s\n  want: %q\n' "$desc" "$want"
		fail=1
	fi
}
absent() { # absent <描述> <不应出现的子串>
	local desc="$1" pat="$2"
	if grep -qF "$pat" <<< "$out"; then
		printf 'FAIL: %s（不应出现 %s）\n' "$desc" "$pat"
		fail=1
	else
		printf 'PASS: %s\n' "$desc"
	fi
}

# 记录格式：kind \t display \t payload \t root。
# display：base 之下显示相对 base 的路径，之外显示绝对全称；root 仅项目内文件非空。
expect "URL 提取"                 $'url\t🔗 https://example.com/a?b=1\thttps://example.com/a?b=1'
expect "www 裸域"                 $'url\t🔗 www.foo.org\twww.foo.org'
expect "裸相对文件（display 相对 + root）" \
	$'file\t📄 real.txt\t'"$TMP/real.txt"$'\t'"$TMP"
expect "file:line:col 剥离跳行 + root" \
	$'file\t📄 app.py:12:3\t'"$TMP/app.py:12:3"$'\t'"$TMP"
expect "项目内绝对写法也显示相对" \
	$'file\t📄 app.py:7\t'"$TMP/app.py:7"$'\t'"$TMP"
expect "无前缀多段相对路径 + 行号（不被拆成首段裸词）" \
	$'file\t📄 subdir/deep.txt:5\t'"$TMP/subdir/deep.txt:5"$'\t'"$TMP"
expect "裸词目录（display 相对、root 空）" \
	$'dir\t📁 subdir\t'"$TMP/subdir"$'\t'
expect "根外绝对路径（display 绝对、root 空、单开）" \
	$'file\t📄 /etc/hosts\t/etc/hosts\t'
absent "/dev 噪声排除"            "/dev/null"
absent "不存在路径过滤"           "ghost.py"

# payload 去重：./real.txt(第2行) 与 real.txt(第4行) 归一后同条，只留最近一次
n=$(grep -cF "$TMP/real.txt" <<< "$out")
if [[ "$n" == 1 ]]; then
	printf 'PASS: payload 去重（real.txt 仅 1 条）\n'
else
	printf 'FAIL: payload 去重（real.txt 出现 %s 条）\n' "$n"
	fail=1
fi

# 文件夹开关 off：dir 行消失、file/url 行不受影响
out_nodirs=$(printf '%s\n' "$text" | OPEN_URLS_SHOW_DIRS=off bash "$OPEN_URLS" --extract "$TMP")
if grep -q $'^dir\t' <<< "$out_nodirs"; then
	printf 'FAIL: 文件夹开关 off 仍有 dir 行\n'
	fail=1
elif ! grep -qF "$TMP/real.txt" <<< "$out_nodirs"; then
	printf 'FAIL: 文件夹开关 off 误伤 file 行\n'
	fail=1
else
	printf 'PASS: 文件夹开关 off（无 dir 行、file 行保留）\n'
fi

# 排序：最近出现在前 → 第 4 行的 real.txt 应排在第 3 行的 app.py 之前
if [[ $(grep -nF "real.txt" <<< "$out" | head -1 | cut -d: -f1) -lt \
      $(grep -nF "app.py" <<< "$out" | head -1 | cut -d: -f1) ]]; then
	printf 'PASS: 最近出现在前\n'
else
	printf 'FAIL: 最近出现在前\n  out:\n%s\n' "$out"
	fail=1
fi

exit "$fail"
