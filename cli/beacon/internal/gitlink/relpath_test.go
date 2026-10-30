package gitlink

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
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

func TestRepoPreservesWhitespacePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows normalizes trailing spaces in paths")
	}
	r := newTestRepo(t)
	root := r.root + " "
	if err := os.Rename(r.root, root); err != nil {
		t.Fatal(err)
	}
	r.root = root
	repo, err := OpenRepo(context.Background(), r.gitClient())
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Root != canonical {
		t.Fatalf("Root = %q, want %q", repo.Root, canonical)
	}
	for _, name := range []string{" notes.md", "notes.md ", " docs / notes.md "} {
		r.write(name, "content")
		for _, path := range []string{name, filepath.Join(root, name)} {
			if rel, ok := repo.RelPath(path, root); !ok || rel != name {
				t.Errorf("RelPath(%q) = %q, %v; want %q", path, rel, ok, name)
			}
		}
	}
}

func TestReadEvidencePreservesWhitespaceWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows normalizes trailing spaces in paths")
	}
	r := newTestRepo(t)
	repo := r.open()
	cwd := filepath.Join(repo.Root, "sub ")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeLog(t, logPath, logEvent{at: t0, harness: "codex", session: "s", cwd: cwd, action: "file.modified", filePath: " notes.md ", operation: "modify"})
	got, err := ReadEvidence(context.Background(), repo, logPath, t0.Add(-time.Minute), t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	evidence := got["codex_cli/s"]
	if evidence == nil {
		t.Fatal("missing session evidence")
	}
	if _, ok := evidence.Files["sub / notes.md "]; !ok {
		t.Fatalf("recorded files = %v", evidence.Files)
	}
}
