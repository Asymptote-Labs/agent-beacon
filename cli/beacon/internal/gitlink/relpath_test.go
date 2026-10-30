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

// symlinkTree is a repository whose session is logged through a symlinked parent directory, the
// way macOS logs /tmp for /private/tmp, while git reports the real root.
type symlinkTree struct {
	repo Repo
	cwd  string // the repository root as the session spelled it
	sub  string // a symlink to the repository's sub directory, from outside it
}

func newSymlinkTree(t *testing.T) symlinkTree {
	t.Helper()
	r := newTestRepo(t)
	repo := r.open()
	outside := filepath.Dir(r.root)
	if err := os.MkdirAll(filepath.Join(outside, "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "shared", "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.write("a.txt", "a")
	r.write("sub/b.txt", "b")
	symlink(t, filepath.Join("..", "shared", "config.json"), filepath.Join(r.root, "cfg.json"))
	symlink(t, "a.txt", filepath.Join(r.root, "alias.txt"))
	links := t.TempDir()
	via := filepath.Join(links, "via")
	symlink(t, outside, via)
	sub := filepath.Join(links, "sub")
	symlink(t, filepath.Join(r.root, "sub"), sub)
	return symlinkTree{repo: repo, cwd: filepath.Join(via, filepath.Base(r.root)), sub: sub}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

// Git tracks a symlink as itself. A link the session created is the change, wherever it points,
// and a path spelled through a symlinked directory still names a file in the repository.
func TestRelPathKeepsSymlinksTheSessionCreated(t *testing.T) {
	tree := newSymlinkTree(t)
	for _, tc := range []struct {
		name, path, base, want string
	}{
		{"link to a file outside", "cfg.json", tree.cwd, "cfg.json"},
		{"absolute link to a file outside", filepath.Join(tree.cwd, "cfg.json"), "", "cfg.json"},
		{"link to a file inside", "alias.txt", tree.cwd, "alias.txt"},
		{"plain file", "a.txt", tree.cwd, "a.txt"},
		{"file not yet written", filepath.Join("new", "c.txt"), tree.cwd, "new/c.txt"},
		{"working directory is a symlink into the repository", "b.txt", tree.sub, "sub/b.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rel, ok := tree.repo.RelPath(tc.path, tc.base); !ok || rel != tc.want {
				t.Errorf("RelPath(%q, %q) = %q, %v; want %q", tc.path, tc.base, rel, ok, tc.want)
			}
		})
	}
}

func TestReadEvidenceKeepsSymlinksTheSessionCreated(t *testing.T) {
	tree := newSymlinkTree(t)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	var events []logEvent
	for _, name := range []string{"cfg.json", "alias.txt", "a.txt"} {
		events = append(events, logEvent{at: t0, harness: "codex", session: "s", cwd: tree.cwd, action: "file.modified", filePath: name, operation: "modify"})
	}
	writeLog(t, logPath, events...)
	got, err := ReadEvidence(context.Background(), tree.repo, logPath, t0.Add(-time.Minute), t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	evidence := got["codex_cli/s"]
	if evidence == nil {
		t.Fatal("missing session evidence")
	}
	if len(evidence.Files) != 3 {
		t.Fatalf("recorded files = %v, want cfg.json, alias.txt, a.txt", evidence.Files)
	}
	for _, name := range []string{"cfg.json", "alias.txt", "a.txt"} {
		if _, ok := evidence.Files[name]; !ok {
			t.Errorf("recorded files = %v, missing %s", evidence.Files, name)
		}
	}
}
