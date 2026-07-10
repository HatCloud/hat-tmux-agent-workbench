package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// snippet is one reusable text entry in the content library. Stored as a plain
// file under ~/.hat-config/snippets/<group>/<name>; the first line may be a
// "# description" header, the rest is the body. Variables use {{name}} syntax.
type snippet struct {
	Name        string // file basename (no dir)
	Group       string // relative subdir from root; "" = ungrouped (root)
	Path        string // absolute file path (edit/delete/favorite key)
	Description string
	Content     string
	Favorite    bool
	Vars        []string
}

const snippetFavoritesFile = ".favorites"

var snippetVarRegex = regexp.MustCompile(`\{\{([a-zA-Z_][a-zA-Z0-9_]*)\}\}`)

// snippetsRootDir is the on-disk root of the snippet library.
// Kept identical to the legacy path (palette.go) for backward compatibility.
func snippetsRootDir() string {
	return filepath.Join(os.Getenv("HOME"), ".hat-config", "snippets")
}

func extractSnippetVars(content string) []string {
	seen := make(map[string]bool)
	var vars []string
	for _, match := range snippetVarRegex.FindAllStringSubmatch(content, -1) {
		if len(match) > 1 && !seen[match[1]] {
			seen[match[1]] = true
			vars = append(vars, match[1])
		}
	}
	return vars
}

func renderSnippet(content string, values map[string]string) string {
	result := content
	for name, value := range values {
		result = strings.ReplaceAll(result, "{{"+name+"}}", value)
	}
	return result
}

// loadSnippets loads the full library from the default root.
func loadSnippets() []snippet {
	return loadSnippetsFrom(snippetsRootDir())
}

// loadSnippetsFrom recursively walks root; each regular file (excluding
// dotfiles, "_"-prefixed files and the .favorites index) becomes a snippet
// whose Group is the relative directory ("" at root).
func loadSnippetsFrom(root string) []snippet {
	favs := loadFavorites(root)
	var snippets []snippet
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		group := filepath.Dir(rel)
		if group == "." {
			group = ""
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		content := string(data)
		lines := strings.SplitN(content, "\n", 2)
		description := ""
		body := content
		if len(lines) > 0 && strings.HasPrefix(lines[0], "#") {
			description = strings.TrimSpace(strings.TrimPrefix(lines[0], "#"))
			if len(lines) > 1 {
				body = strings.TrimPrefix(content, lines[0]+"\n")
			} else {
				body = ""
			}
		}
		key := snippetRelKey(group, name)
		snippets = append(snippets, snippet{
			Name:        name,
			Group:       group,
			Path:        path,
			Description: description,
			Content:     strings.TrimRight(body, "\n"),
			Favorite:    favs[key],
			Vars:        extractSnippetVars(body),
		})
		return nil
	})
	sort.Slice(snippets, func(i, j int) bool {
		if snippets[i].Group != snippets[j].Group {
			return snippets[i].Group < snippets[j].Group
		}
		return snippets[i].Name < snippets[j].Name
	})
	return snippets
}

// listSnippets returns snippets in group; group=="" returns all.
func listSnippets(group string) []snippet {
	all := loadSnippets()
	if group == "" {
		return all
	}
	var out []snippet
	for _, s := range all {
		if s.Group == group {
			out = append(out, s)
		}
	}
	return out
}

// snippetGroups returns distinct non-empty groups, sorted.
func snippetGroups() []string {
	seen := map[string]bool{}
	var groups []string
	for _, s := range loadSnippets() {
		if s.Group != "" && !seen[s.Group] {
			seen[s.Group] = true
			groups = append(groups, s.Group)
		}
	}
	sort.Strings(groups)
	return groups
}

// ── favorites (sidecar .favorites index at root) ────────────────────────────

func snippetRelKey(group, name string) string {
	if group == "" {
		return name
	}
	return filepath.ToSlash(filepath.Join(group, name))
}

// relKeyForPath maps an absolute snippet path back to its relative favorites key.
func relKeyForPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.Base(path)
	}
	return filepath.ToSlash(rel)
}

func favoritesPath(root string) string {
	return filepath.Join(root, snippetFavoritesFile)
}

func loadFavorites(root string) map[string]bool {
	favs := map[string]bool{}
	data, err := os.ReadFile(favoritesPath(root))
	if err != nil {
		return favs
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			favs[line] = true
		}
	}
	return favs
}

func saveFavorites(root string, favs map[string]bool) error {
	keys := make([]string, 0, len(favs))
	for k, on := range favs {
		if on {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		// no favorites left → remove the index file if present
		err := os.Remove(favoritesPath(root))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(favoritesPath(root), []byte(strings.Join(keys, "\n")+"\n"), 0644)
}

// toggleFavorite flips the favorite state of the snippet at path; returns new state.
func toggleFavorite(path string) (bool, error) {
	root := snippetsRootDir()
	key := relKeyForPath(root, path)
	favs := loadFavorites(root)
	now := !favs[key]
	favs[key] = now
	if err := saveFavorites(root, favs); err != nil {
		return false, err
	}
	return now, nil
}

// ── CRUD ────────────────────────────────────────────────────────────────────

// validateSnippetName rejects names that loadSnippets would silently skip or
// that would escape the group directory.
func validateSnippetName(name string) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("name cannot contain path separators")
	}
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return fmt.Errorf("name cannot start with '.' or '_'")
	}
	if name == snippetFavoritesFile {
		return fmt.Errorf("%q is reserved", snippetFavoritesFile)
	}
	return nil
}

// validateSnippetGroup rejects groups that would escape the snippet root.
func validateSnippetGroup(group string) error {
	if group == "" {
		return nil
	}
	if filepath.IsAbs(group) {
		return fmt.Errorf("group cannot be an absolute path")
	}
	for _, part := range strings.FieldsFunc(group, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return fmt.Errorf("group cannot contain '..'")
		}
	}
	return nil
}

func snippetFileBody(description, content string) []byte {
	var b strings.Builder
	if strings.TrimSpace(description) != "" {
		b.WriteString("# " + strings.TrimSpace(description) + "\n")
	}
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// addSnippet creates a new snippet file under group; refuses to overwrite.
func addSnippet(group, name, description, content string) error {
	if err := validateSnippetName(name); err != nil {
		return err
	}
	if err := validateSnippetGroup(group); err != nil {
		return err
	}
	root := snippetsRootDir()
	dir := root
	if group != "" {
		dir = filepath.Join(root, group)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("snippet %q already exists in group %q", name, group)
	}
	return os.WriteFile(path, snippetFileBody(description, content), 0644)
}

// updateSnippet rewrites a snippet's content/description, optionally moving it to
// a new group/name. Favorites key is synced BEFORE the file move so a failed
// move never leaves an orphaned favorites key.
func updateSnippet(oldPath, group, name, description, content string) error {
	if err := validateSnippetName(name); err != nil {
		return err
	}
	if err := validateSnippetGroup(group); err != nil {
		return err
	}
	root := snippetsRootDir()
	dir := root
	if group != "" {
		dir = filepath.Join(root, group)
	}
	newPath := filepath.Join(dir, name)

	if newPath != oldPath {
		if _, err := os.Stat(newPath); err == nil {
			return fmt.Errorf("snippet %q already exists in group %q", name, group)
		}
		// sync favorites key (old → new) before moving the file
		oldKey := relKeyForPath(root, oldPath)
		newKey := snippetRelKey(group, name)
		favs := loadFavorites(root)
		movedFav := favs[oldKey]
		if movedFav {
			delete(favs, oldKey)
			favs[newKey] = true
			if err := saveFavorites(root, favs); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		if err := os.WriteFile(newPath, snippetFileBody(description, content), 0644); err != nil {
			// roll the favorites key back so the move stays atomic
			if movedFav {
				favs[oldKey] = true
				delete(favs, newKey)
				_ = saveFavorites(root, favs)
			}
			return err
		}
		if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		pruneEmptyGroupDir(root, oldPath)
		return nil
	}
	return os.WriteFile(oldPath, snippetFileBody(description, content), 0644)
}

// deleteSnippet removes the file, prunes its favorites key, and removes the
// group directory if it became empty.
func deleteSnippet(path string) error {
	root := snippetsRootDir()
	key := relKeyForPath(root, path)
	favs := loadFavorites(root)
	if favs[key] {
		delete(favs, key)
		if err := saveFavorites(root, favs); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	pruneEmptyGroupDir(root, path)
	return nil
}

// pruneEmptyGroupDir removes the snippet's parent dir if it is a (now empty)
// group subdirectory of root. Never removes root itself.
func pruneEmptyGroupDir(root, path string) {
	dir := filepath.Dir(path)
	// Only prune a strict subdirectory of root (a group dir); never root itself.
	if dir == root || dir == "." || !strings.HasPrefix(dir, root+string(filepath.Separator)) {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return
	}
	_ = os.Remove(dir)
}
