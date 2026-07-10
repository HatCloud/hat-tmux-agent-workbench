package main

import (
	"os"
	"path/filepath"
	"testing"
)

// withSnippetRoot points snippetsRootDir at a temp dir for the duration of fn
// by overriding HOME (snippetsRootDir = $HOME/.hat-config/snippets).
func withSnippetRoot(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".hat-config", "snippets")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSnippetsGrouping(t *testing.T) {
	root := withSnippetRoot(t)
	writeFile(t, filepath.Join(root, "timer", "a"), "# alpha\nhello {{x}}")
	writeFile(t, filepath.Join(root, "git", "b"), "# beta\nworld")
	writeFile(t, filepath.Join(root, "c"), "# gamma\nroot snippet")

	snaps := loadSnippets()
	got := map[string]string{} // name -> group
	var aSnip *snippet
	for i := range snaps {
		s := snaps[i]
		got[s.Name] = s.Group
		if s.Name == "a" {
			cp := s
			aSnip = &cp
		}
	}
	if got["a"] != "timer" || got["b"] != "git" || got["c"] != "" {
		t.Fatalf("group mismatch: %+v", got)
	}
	if aSnip == nil || len(aSnip.Vars) != 1 || aSnip.Vars[0] != "x" {
		t.Fatalf("expected var x on snippet a, got %+v", aSnip)
	}
}

func TestFavoritesRoundtrip(t *testing.T) {
	root := withSnippetRoot(t)
	p := filepath.Join(root, "timer", "deploy")
	writeFile(t, p, "# deploy\nmake deploy")

	on, err := toggleFavorite(p)
	if err != nil || !on {
		t.Fatalf("toggle on failed: %v on=%v", err, on)
	}
	found := false
	for _, s := range loadSnippets() {
		if s.Name == "deploy" {
			found = s.Favorite
		}
	}
	if !found {
		t.Fatal("expected deploy to be favorite after toggle")
	}
	// toggle off removes the key
	off, err := toggleFavorite(p)
	if err != nil || off {
		t.Fatalf("toggle off failed: %v off=%v", err, off)
	}
	for _, s := range loadSnippets() {
		if s.Name == "deploy" && s.Favorite {
			t.Fatal("expected deploy not favorite after second toggle")
		}
	}
}

func TestAddDeleteSnippet(t *testing.T) {
	root := withSnippetRoot(t)
	if err := addSnippet("timer", "standup", "daily standup", "post standup {{date}}"); err != nil {
		t.Fatalf("addSnippet: %v", err)
	}
	path := filepath.Join(root, "timer", "standup")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file created: %v", err)
	}
	// duplicate rejected
	if err := addSnippet("timer", "standup", "x", "y"); err == nil {
		t.Fatal("expected duplicate addSnippet to error")
	}
	// delete removes file and prunes empty group dir
	if err := deleteSnippet(path); err != nil {
		t.Fatalf("deleteSnippet: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected file removed")
	}
	if _, err := os.Stat(filepath.Join(root, "timer")); !os.IsNotExist(err) {
		t.Fatal("expected empty group dir pruned")
	}
}

func TestAddSnippetRejectsBadName(t *testing.T) {
	withSnippetRoot(t)
	for _, bad := range []string{"", ".hidden", "_tmp", "a/b", ".favorites"} {
		if err := addSnippet("timer", bad, "d", "c"); err == nil {
			t.Fatalf("expected reject for name %q", bad)
		}
	}
}

func TestRenderSnippet(t *testing.T) {
	cases := []struct {
		content string
		vals    map[string]string
		want    string
	}{
		{"hello {{name}}", map[string]string{"name": "world"}, "hello world"},
		{"{{a}}-{{b}}", map[string]string{"a": "1", "b": "2"}, "1-2"},
		{"{{x}} and {{x}}", map[string]string{"x": "z"}, "z and z"},
		{"empty {{e}}", map[string]string{"e": ""}, "empty "},
		{"no vars here", map[string]string{}, "no vars here"},
	}
	for _, c := range cases {
		if got := renderSnippet(c.content, c.vals); got != c.want {
			t.Errorf("renderSnippet(%q,%v)=%q want %q", c.content, c.vals, got, c.want)
		}
	}
}

func TestExtractSnippetVars(t *testing.T) {
	got := extractSnippetVars("{{a}} {{b}} {{a}} plain")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("expected [a b] (dedup, ordered), got %v", got)
	}
}

func TestUpdateSnippetMovesGroup(t *testing.T) {
	root := withSnippetRoot(t)
	oldPath := filepath.Join(root, "drafts", "x")
	writeFile(t, oldPath, "# d\nbody")
	if _, err := toggleFavorite(oldPath); err != nil {
		t.Fatal(err)
	}
	// move drafts/x -> timer/y
	if err := updateSnippet(oldPath, "timer", "y", "d2", "body2"); err != nil {
		t.Fatalf("updateSnippet: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatal("expected old file removed")
	}
	newPath := filepath.Join(root, "timer", "y")
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("expected new file: %v", err)
	}
	// favorites key synced to new path
	favs := loadFavorites(root)
	if !favs["timer/y"] || favs["drafts/x"] {
		t.Fatalf("favorites not synced: %+v", favs)
	}
}
