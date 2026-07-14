package main

import "testing"

// 字段顺序须与 refresh() 的 list-windows 格式串一致：
// 0 session_id|1 session_name|2 window_index|3 window_id|4 window_name|
// 5 window_flags|6 window_activity|7 window_bell_flag|8 pane_current_path|
// 9 @agent_dir|10 @agent_provider|11 @agent_model|12 @agent_client|13 @agent_title
func TestParseWindowNavLine(t *testing.T) {
	// agent 窗口（@agent_client 已设）
	agent := "$1|main|2|@5|[I] proj/title|*|123|0|/p|p|claude|sonnet|claude|title"
	w, ok := parseWindowNavLine(agent)
	if !ok {
		t.Fatalf("parseWindowNavLine 应解析成功")
	}
	if !w.isAgent {
		t.Errorf("isAgent 应为 true（@agent_client 已设）")
	}
	if w.status != "idle" {
		t.Errorf("status 应为 idle，实际 %q", w.status)
	}
	if w.windowName != "proj/title" {
		t.Errorf("windowName 应去掉状态前缀，实际 %q", w.windowName)
	}

	// 非 agent 窗口（无 @agent_client）
	plain := "$1|main|1|@4|plain|*|100|0|/p|||||"
	p, ok := parseWindowNavLine(plain)
	if !ok {
		t.Fatalf("普通窗口应解析成功")
	}
	if p.isAgent {
		t.Errorf("无 @agent_client 时 isAgent 应为 false")
	}
}

func TestParseWindowNavLineFields(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		wantOK     bool
		wantStatus string
		wantBell   bool
	}{
		{"empty", "", false, "", false},
		{"too few fields", "$1|s|1|@1", false, "", false},
		{"busy prefix", "$1|s|1|@1|[B] x|*|10|0|/p|p|c|m|claude|t", true, "busy", false},
		{"idle prefix", "$1|s|1|@1|[I] x|*|10|0|/p|||||", true, "idle", false},
		{"asking prefix", "$1|s|1|@1|[?] x|*|10|0|/p|||||", true, "asking", false},
		{"error prefix", "$1|s|1|@1|[E] x|*|10|0|/p|||||", true, "error", false},
		{"native bell", "$1|s|1|@1|plain|*|10|1|/p|||||", true, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, ok := parseWindowNavLine(c.line)
			if ok != c.wantOK {
				t.Fatalf("ok=%v，期望 %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if w.status != c.wantStatus {
				t.Errorf("status=%q，期望 %q", w.status, c.wantStatus)
			}
			if w.bell != c.wantBell {
				t.Errorf("bell=%v，期望 %v", w.bell, c.wantBell)
			}
		})
	}
}

func TestErrorWindowRemainsLive(t *testing.T) {
	if got := windowLivenessTier(windowNavRow{isAgent: true, status: "error"}); got != 0 {
		t.Fatalf("error agent liveness tier = %d, want 0", got)
	}
}

func TestNeedsAttentionOnlyForUnreadBell(t *testing.T) {
	cases := []struct {
		name string
		row  windowNavRow
		want bool
	}{
		{"completed unread", windowNavRow{bell: true, status: "idle"}, true},
		{"asking unread", windowNavRow{bell: true, status: "asking"}, true},
		{"limited unread", windowNavRow{bell: true, status: "limited"}, true},
		{"asking acknowledged", windowNavRow{bell: false, status: "asking"}, false},
		{"limited acknowledged", windowNavRow{bell: false, status: "limited"}, false},
		{"busy", windowNavRow{bell: false, status: "busy"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsAttention(tc.row); got != tc.want {
				t.Fatalf("needsAttention(%+v) = %v, want %v", tc.row, got, tc.want)
			}
		})
	}
}
