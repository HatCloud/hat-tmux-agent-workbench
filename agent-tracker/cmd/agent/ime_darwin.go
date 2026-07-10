//go:build darwin

package main

/*
#cgo LDFLAGS: -framework Carbon
#include <Carbon/Carbon.h>
#include <stdlib.h>

// selectInputSourceByID 枚举已启用输入源，按 kTISPropertyInputSourceID 精确匹配后选中。
// 返回 0 成功；-1 未找到（该源未在系统设置里启用）；-2/-3 内部分配失败；其余为 TISSelectInputSource 的 OSStatus。
static int selectInputSourceByID(const char *wantID) {
	CFStringRef want = CFStringCreateWithCString(NULL, wantID, kCFStringEncodingUTF8);
	if (want == NULL) return -2;
	CFArrayRef list = TISCreateInputSourceList(NULL, false);
	if (list == NULL) { CFRelease(want); return -3; }
	int rc = -1;
	CFIndex n = CFArrayGetCount(list);
	for (CFIndex i = 0; i < n; i++) {
		TISInputSourceRef src = (TISInputSourceRef)CFArrayGetValueAtIndex(list, i);
		CFStringRef sid = (CFStringRef)TISGetInputSourceProperty(src, kTISPropertyInputSourceID);
		if (sid != NULL && CFStringCompare(sid, want, 0) == kCFCompareEqualTo) {
			OSStatus st = TISSelectInputSource(src);
			rc = (st == noErr) ? 0 : (int)st;
			break;
		}
	}
	CFRelease(list);
	CFRelease(want);
	return rc;
}

// currentInputSourceMatches 返回 1 表示当前键盘输入源即 wantID，0 不是，-1 取不到当前源。
static int currentInputSourceMatches(const char *wantID) {
	TISInputSourceRef cur = TISCopyCurrentKeyboardInputSource();
	if (cur == NULL) return -1;
	CFStringRef sid = (CFStringRef)TISGetInputSourceProperty(cur, kTISPropertyInputSourceID);
	int match = 0;
	if (sid != NULL) {
		CFStringRef want = CFStringCreateWithCString(NULL, wantID, kCFStringEncodingUTF8);
		if (want != NULL) {
			match = (CFStringCompare(sid, want, 0) == kCFCompareEqualTo) ? 1 : 0;
			CFRelease(want);
		}
	}
	CFRelease(cur);
	return match;
}
*/
import "C"

import (
	"flag"
	"fmt"
	"os"
	"unsafe"
)

// 默认目标输入源：ABC（纯键盘布局，无 input_mode_id，切到它是 macOS 上可靠的方向）。
const defaultInputSourceID = "com.apple.keylayout.ABC"

func runImeCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agent ime <switch>")
	}
	switch args[0] {
	case "switch":
		return runImeSwitch(args[1:])
	default:
		return fmt.Errorf("unknown ime subcommand: %s", args[0])
	}
}

func runImeSwitch(args []string) error {
	fs := flag.NewFlagSet("agent ime switch", flag.ContinueOnError)
	to := fs.String("to", defaultInputSourceID, "input source id to select")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return selectInputSource(*to)
}

// selectInputSource 走 Carbon TIS API 直接切换，无需加载 config / 连接 daemon——
// 这是 tmux prefix 绑定每次按键都会调的快路径。
func selectInputSource(id string) error {
	cID := C.CString(id)
	defer C.free(unsafe.Pointer(cID))

	if C.currentInputSourceMatches(cID) == 1 {
		return nil
	}
	switch rc := int(C.selectInputSourceByID(cID)); {
	case rc == 0:
		return nil
	case rc == -1:
		return fmt.Errorf("input source %q not found (enable it in System Settings ▸ Keyboard)", id)
	default:
		return fmt.Errorf("select input source %q failed (status %d)", id, rc)
	}
}
