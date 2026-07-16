package statustag

import "testing"

func TestForStatus(t *testing.T) {
	cases := map[string]string{
		"busy": "[B] ", "shell": "[I] ", "BUSY ": "[B] ",
		"idle":   "[I] ",
		"asking": "[?] ", "waiting": "[?] ", "paused": "[?] ",
		"limited": "[L] ",
		"error":   "[E] ",
		"":        "", "unknown": "",
	}
	for in, want := range cases {
		if got := ForStatus(in); got != want {
			t.Errorf("ForStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrefixStripStatusOf(t *testing.T) {
	if Prefix("[B] proj/title") != "[B] " {
		t.Fatalf("Prefix 应识别 [B] ")
	}
	if Prefix("proj/title") != "" {
		t.Fatalf("无前缀应返回空")
	}
	if Strip("[?] 🌐 mini") != "🌐 mini" {
		t.Fatalf("Strip 应剥掉 [?] ")
	}
	if Strip("plain") != "plain" {
		t.Fatalf("Strip 对无前缀名应原样返回")
	}
	if StatusOf("[E] x") != "error" || StatusOf("[L] x") != "limited" || StatusOf("x") != "" {
		t.Fatalf("StatusOf 映射错误")
	}
}
