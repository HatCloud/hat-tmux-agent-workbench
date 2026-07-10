package main

import (
	"reflect"
	"testing"
)

// isPUARune 判定 r 是否落在 Unicode Private Use Area（BMP 及 Plane 15/16）。
func isPUARune(r rune) bool {
	return (r >= 0xE000 && r <= 0xF8FF) || (r >= 0xF0000 && r <= 0xFFFFD) || (r >= 0x100000 && r <= 0x10FFFD)
}

// iconSetFields 反射展开 iconSet 的全部字段为 name->value。
func iconSetFields(set iconSet) map[string]string {
	v := reflect.ValueOf(set)
	tp := v.Type()
	out := make(map[string]string, tp.NumField())
	for i := 0; i < tp.NumField(); i++ {
		out[tp.Field(i).Name] = v.Field(i).String()
	}
	return out
}

// TestIconSetCompleteness 断言三套图标集的每个字段都非空。
func TestIconSetCompleteness(t *testing.T) {
	for name, set := range map[string]iconSet{"nerd": iconSetNerd, "emoji": iconSetEmoji, "ascii": iconSetASCII} {
		for field, val := range iconSetFields(set) {
			if val == "" {
				t.Errorf("%s set field %s is empty", name, field)
			}
		}
	}
}

// TestIconSetEmojiASCIINoPUA 断言 emoji 与 ascii 集不含任何 PUA 码点（避免无 Nerd Font 时的 ⍰ 回退）。
func TestIconSetEmojiASCIINoPUA(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  iconSet
	}{{"emoji", iconSetEmoji}, {"ascii", iconSetASCII}} {
		for field, val := range iconSetFields(tc.set) {
			for _, r := range val {
				if isPUARune(r) {
					t.Errorf("%s set field %s contains PUA rune %#x", tc.name, field, r)
				}
			}
		}
	}
}

// TestIconSetASCIIPureASCII 断言 ascii 集每个字段都是纯 ASCII（< 0x80）。
func TestIconSetASCIIPureASCII(t *testing.T) {
	for field, val := range iconSetFields(iconSetASCII) {
		for _, r := range val {
			if r >= 0x80 {
				t.Errorf("ascii set field %s contains non-ASCII rune %#x", field, r)
			}
		}
	}
}
