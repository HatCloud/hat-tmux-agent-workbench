package main

import (
	"errors"
	"testing"
)

// When tmux_id is empty the handler must fall back to autodetectContext rather
// than erroring out immediately.
func TestResolveContextAutodetectFallback(t *testing.T) {
	want := tmuxContext{SessionID: "$3", WindowID: "@12", PaneID: "%30"}
	called := false
	got, err := resolveContext("", func() (tmuxContext, error) {
		called = true
		return want, nil
	})
	if err != nil {
		t.Fatalf("resolveContext empty: unexpected error %v", err)
	}
	if !called {
		t.Fatal("expected autodetect fallback to be invoked for empty tmux_id")
	}
	if got != want {
		t.Fatalf("resolveContext = %+v, want %+v", got, want)
	}
}

func TestResolveContextAutodetectError(t *testing.T) {
	_, err := resolveContext("", func() (tmuxContext, error) {
		return tmuxContext{}, errors.New("no tmux")
	})
	if err == nil {
		t.Fatal("expected error when autodetect fails for empty tmux_id")
	}
}

func TestDetermineContextInvalid(t *testing.T) {
	cases := []struct{ name, in string }{
		{"empty", ""},
		{"one segment", "bad"},
		{"two segments", "$3::@12"},
		{"four segments", "$3::@12::%30::x"},
		{"empty session", "::@12::%30"},
		{"empty window", "$3::::%30"},
		{"empty pane", "$3::@12::"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := determineContext(tc.in); err == nil {
				t.Fatalf("determineContext(%q) expected error, got nil", tc.in)
			}
		})
	}
}

func TestDetermineContextValid(t *testing.T) {
	got, err := determineContext("$3::@12::%30")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := tmuxContext{SessionID: "$3", WindowID: "@12", PaneID: "%30"}
	if got != want {
		t.Fatalf("determineContext = %+v, want %+v", got, want)
	}
}

func TestResolveContextExplicitSkipsAutodetect(t *testing.T) {
	got, err := resolveContext("$3::@12::%30", func() (tmuxContext, error) {
		t.Fatal("autodetect must not run when tmux_id is provided")
		return tmuxContext{}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := tmuxContext{SessionID: "$3", WindowID: "@12", PaneID: "%30"}
	if got != want {
		t.Fatalf("resolveContext = %+v, want %+v", got, want)
	}
}
