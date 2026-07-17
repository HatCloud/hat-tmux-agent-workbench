package agentclient

import (
	"testing"
)

type fakeAdapter struct {
	id      string
	detect  func(idx *Index, panePID int) (LiveSession, bool)
	wantPID int
}

func (f *fakeAdapter) ID() string { return f.id }

func (f *fakeAdapter) Detect(idx *Index, panePID int) (LiveSession, bool) {
	if f.detect != nil {
		return f.detect(idx, panePID)
	}
	if f.wantPID != 0 && panePID != f.wantPID {
		return LiveSession{}, false
	}
	return LiveSession{Client: f.id, Status: StatusIdle, PID: panePID}, true
}

func TestDetectForPane_TagPreferWhenDetectOK(t *testing.T) {
	r := &Registry{Adapters: []Adapter{
		&fakeAdapter{id: "claude"},
		&fakeAdapter{id: "codex"},
		&fakeAdapter{id: "grok"},
	}}
	idx := &Index{Children: map[int][]int{}, Commands: map[int]string{}}
	s, ok := r.DetectForPane(idx, 42, "grok")
	if !ok || s.Client != "grok" {
		t.Fatalf("want grok, got ok=%v client=%q", ok, s.Client)
	}
}

func TestDetectForPane_TagMissFallthrough(t *testing.T) {
	r := &Registry{Adapters: []Adapter{
		&fakeAdapter{id: "claude"},
		&fakeAdapter{id: "codex"},
		&fakeAdapter{
			id: "grok",
			detect: func(idx *Index, panePID int) (LiveSession, bool) {
				return LiveSession{}, false // strict miss
			},
		},
	}}
	idx := &Index{}
	s, ok := r.DetectForPane(idx, 7, "grok")
	if !ok || s.Client != "claude" {
		t.Fatalf("want fallthrough claude, got ok=%v client=%q", ok, s.Client)
	}
}

func TestDetectForPane_NoTagOrder(t *testing.T) {
	r := &Registry{Adapters: []Adapter{
		&fakeAdapter{id: "claude", wantPID: 1},
		&fakeAdapter{id: "codex", wantPID: 1},
		&fakeAdapter{id: "grok", wantPID: 1},
	}}
	// claude always succeeds first in order
	s, ok := r.DetectForPane(&Index{}, 1, "")
	if !ok || s.Client != "claude" {
		t.Fatalf("want claude first, got ok=%v client=%q", ok, s.Client)
	}
}

func TestDetectForPane_UnknownTagIgnored(t *testing.T) {
	r := &Registry{Adapters: []Adapter{
		&fakeAdapter{id: "claude"},
		&fakeAdapter{id: "codex"},
	}}
	s, ok := r.DetectForPane(&Index{}, 9, "not-a-client")
	if !ok || s.Client != "claude" {
		t.Fatalf("want claude after unknown tag, got ok=%v client=%q", ok, s.Client)
	}
}

func TestDetectForPane_EmptyRegistry(t *testing.T) {
	r := &Registry{}
	if _, ok := r.DetectForPane(&Index{}, 1, "claude"); ok {
		t.Fatal("empty registry must miss")
	}
}

func TestRegisterDefault_Idempotent(t *testing.T) {
	ResetDefaultForTest()
	t.Cleanup(ResetDefaultForTest)
	a := &fakeAdapter{id: "claude"}
	RegisterDefault(a)
	RegisterDefault(a)
	r := DefaultRegistry()
	if len(r.Adapters) != 1 {
		t.Fatalf("want 1 adapter, got %d", len(r.Adapters))
	}
}
