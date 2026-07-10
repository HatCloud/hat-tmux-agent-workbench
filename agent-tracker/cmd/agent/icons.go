package main

import "strings"

// iconSet 汇集状态栏各模块与分隔符使用的图标字面量。三套实例分别面向
// nerd（Nerd Font PUA 字形）、emoji（通用 emoji 回退）、ascii（纯文本回退），
// 由 activeIconSet() 按配置 icon_set 选择，唯一权威在 agent-config.json。
type iconSet struct {
	CPU      string
	Network  string
	Memory   string
	Window   string
	Session  string
	Total    string
	Todos    string
	FlashMoe string
	SepLeft  string
	SepRight string
}

// iconSetNerd 原样迁自 tmux_status.go 的 statusIcon* 常量、left.sh 的
// SepLeft(U+E0B0) 与 tmux_status.go 右侧 SepRight(U+E0B2)。
var iconSetNerd = iconSet{
	CPU:      "",
	Network:  "\U000f05a9",
	Memory:   "",
	Window:   "\U000f05b2",
	Session:  "",
	Total:    "\U000f035b",
	Todos:    "\U000f039a",
	FlashMoe: "\U000f167a",
	SepLeft:  "",
	SepRight: "",
}

// iconSetEmoji 用通用 emoji / 文本符号替代 PUA 字形，供无 Nerd Font 的终端。
var iconSetEmoji = iconSet{
	CPU:      "🖥",
	Network:  "📡",
	Memory:   "🧠",
	Window:   "🪟",
	Session:  "📑",
	Total:    "Σ",
	Todos:    "☑",
	FlashMoe: "⚡",
	SepLeft:  "▐",
	SepRight: "▌",
}

// iconSetASCII 纯 ASCII 文本，供无 emoji 支持或需最大兼容的环境；分隔符退化为单空格。
var iconSetASCII = iconSet{
	CPU:      "CPU",
	Network:  "NET",
	Memory:   "MEM",
	Window:   "WIN",
	Session:  "SES",
	Total:    "TOT",
	Todos:    "TODO",
	FlashMoe: "moe",
	SepLeft:  " ",
	SepRight: " ",
}

// activeIconSet 按配置字段 icon_set 返回图标集，缺省或非法值回退 nerd。
func activeIconSet() iconSet {
	switch strings.TrimSpace(loadAppConfig().IconSet) {
	case "emoji":
		return iconSetEmoji
	case "ascii":
		return iconSetASCII
	default:
		return iconSetNerd
	}
}
