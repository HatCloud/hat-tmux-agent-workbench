package main

import (
	"strings"
	"testing"
)

func TestStatusIconError(t *testing.T) {
	if got := statusIcon("error"); !strings.Contains(got, "E") {
		t.Fatalf("statusIcon(error) = %q, want red E glyph", got)
	}
}
