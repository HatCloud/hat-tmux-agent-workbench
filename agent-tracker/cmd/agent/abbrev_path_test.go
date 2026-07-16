package main

import (
	"path/filepath"
	"testing"
)

// abbrevPath 与 tmux/scripts/agent 的 _abbrev_dir 是同一条规则的双语实现
// （knowledge duplication，无法跨语言合并）。本测试与
// scripts/tests/agent_helpers_test.sh 使用同一组用例钉死两侧行为——改一侧
// 必须两个测试都过。
func TestAbbrevPath(t *testing.T) {
	home := homeDir()
	cases := []struct{ in, want string }{
		{filepath.Join(home, "Projects", "foo"), "~/P/foo"},
		{filepath.Join(home, ".hat-config", "x"), "~/.h/x"},
		{filepath.Join(home, "a", "b", "c"), "~/a/b/c"}, // 中段已是单字符，缩写幂等
		{filepath.Join(home, "foo"), "~/foo"},
		{"/usr/local/bin", "/u/l/bin"},
		{"/tmp", "/tmp"},
	}
	for _, c := range cases {
		if got := abbrevPath(c.in); got != c.want {
			t.Errorf("abbrevPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
