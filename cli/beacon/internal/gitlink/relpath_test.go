package gitlink

import (
	"net/url"
	"path/filepath"
	"testing"
)

// Editors and MCP tools report files as file: URIs. They name the same file as the plain path.
func TestRelPathReadsFileURIs(t *testing.T) {
	r := newTestRepo(t)
	repo := r.open()
	file := filepath.Join(repo.Root, "docs", "My Notes.md")
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(file)}).String()
	for _, path := range []string{file, uri, "file://localhost" + (&url.URL{Path: filepath.ToSlash(file)}).EscapedPath()} {
		if rel, ok := repo.RelPath(path, ""); !ok || rel != "docs/My Notes.md" {
			t.Errorf("RelPath(%q) = %q, %v; want docs/My Notes.md", path, rel, ok)
		}
	}
	if rel, ok := repo.RelPath("file://elsewhere/share/x.md", repo.Root); ok {
		t.Errorf("a file on another host was placed in the repository as %q", rel)
	}
}
