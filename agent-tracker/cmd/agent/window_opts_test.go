package main

import "testing"

// 写穿透：pass 内写入必须立刻反映到 memo（同 pass 有写后读依赖，如 @agent_error_*）。
func TestWindowOptMemoWriteThrough(t *testing.T) {
	beginWindowOptMemo()
	defer endWindowOptMemo()
	windowOptMemo["@999"] = map[string]string{"@agent_model": "old", "@agent_error_at": "123"}

	setWindowOption("@999", "@agent_model", "new")
	if v, ok := memoWindowOption("@999", "@agent_model"); !ok || v != "new" {
		t.Fatalf("setWindowOption 后 memo 应读到 new，实为 %q ok=%v", v, ok)
	}
	unsetWindowOption("@999", "@agent_error_at")
	if v, ok := memoWindowOption("@999", "@agent_error_at"); !ok || v != "" {
		t.Fatalf("unsetWindowOption 后 memo 应读到空，实为 %q ok=%v", v, ok)
	}
}

// memo 未激活时不介入（ok=false 走慢路径）。
func TestWindowOptMemoInactive(t *testing.T) {
	endWindowOptMemo()
	if _, ok := memoWindowOption("@999", "@agent_model"); ok {
		t.Fatalf("memo 未激活时应返回 ok=false")
	}
}
