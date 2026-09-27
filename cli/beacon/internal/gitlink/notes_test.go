package gitlink

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func TestOpenRepoResolvesRootAndDirs(t *testing.T) {
	r := newTestRepo(t)
	r.write("sub/dir/x.txt", "x")
	repo, err := OpenRepo(context.Background(), Git{Dir: filepath.Join(r.root, "sub", "dir"), Env: r.env})
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, _ := filepath.EvalSymlinks(r.root)
	gotRoot, _ := filepath.EvalSymlinks(repo.Root)
	if gotRoot != wantRoot {
		t.Fatalf("root %q, want %q", repo.Root, r.root)
	}
	if filepath.Base(repo.GitDir) != ".git" || filepath.Base(repo.CommonDir) != ".git" {
		t.Fatalf("git dir %q common %q", repo.GitDir, repo.CommonDir)
	}
	if !filepath.IsAbs(repo.CommonDir) {
		t.Fatalf("common dir %q is not absolute", repo.CommonDir)
	}
}

func TestOpenRepoOutsideRepository(t *testing.T) {
	r := newTestRepo(t)
	outside := t.TempDir()
	_, err := OpenRepo(context.Background(), Git{Dir: outside, Env: append(r.env, "GIT_CEILING_DIRECTORIES="+filepath.Dir(outside))})
	if !errors.Is(err, ErrNotRepository) {
		t.Fatalf("got %v, want ErrNotRepository", err)
	}
}

func TestOpenRepoInLinkedWorktreeSharesCommonDir(t *testing.T) {
	r := newTestRepo(t)
	r.write("a.txt", "a")
	r.commitAt("init", t0)
	wt := filepath.Join(filepath.Dir(r.root), "wt")
	r.git("worktree", "add", "-q", "-b", "feature", wt)
	repo, err := OpenRepo(context.Background(), Git{Dir: wt, Env: r.env})
	if err != nil {
		t.Fatal(err)
	}
	main := r.open()
	same := func(a, b string) bool {
		ra, _ := filepath.EvalSymlinks(a)
		rb, _ := filepath.EvalSymlinks(b)
		return ra == rb
	}
	if !same(repo.CommonDir, main.CommonDir) {
		t.Fatalf("worktree common dir %q, main %q", repo.CommonDir, main.CommonDir)
	}
	if same(repo.GitDir, main.GitDir) {
		t.Fatal("a linked worktree has its own git dir")
	}
}

func TestChangedFilesCoversRenamesRootAndMerges(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	r.write("a.go", "package a\n\nfunc A() {}\n")
	r.write("dir/b.go", "package b\n")
	root := r.commitAt("root", t0)
	got, err := ChangedFiles(ctx, r.gitClient(), root)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"a.go", "dir/b.go"}) {
		t.Fatalf("root commit files %q", got)
	}

	r.git("mv", "a.go", "renamed.go")
	r.write("dir/b.go", "package b\n\nvar X = 1\n")
	rename := r.commitAt("rename", t0.Add(time.Minute))
	got, _ = ChangedFiles(ctx, r.gitClient(), rename)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"a.go", "dir/b.go", "renamed.go"}) {
		t.Fatalf("rename commit files %q", got)
	}

	// A merge is compared with its first parent: the files the merge brought in.
	r.git("checkout", "-q", "-b", "side")
	r.write("side.txt", "side")
	r.commitAt("side", t0.Add(2*time.Minute))
	r.git("checkout", "-q", "main")
	r.write("main.txt", "main")
	r.commitAt("main", t0.Add(3*time.Minute))
	r.git("merge", "-q", "--no-ff", "--no-edit", "side")
	merge := r.git("rev-parse", "HEAD")
	got, _ = ChangedFiles(ctx, r.gitClient(), merge)
	if !reflect.DeepEqual(got, []string{"side.txt"}) {
		t.Fatalf("merge commit files %q", got)
	}
}

func TestChangedFilesKeepsUnusualNames(t *testing.T) {
	r := newTestRepo(t)
	name := "sp ace/tab\tand\"quote.txt"
	if strings.ContainsAny(name, "\t\"") && os.PathSeparator == '\\' {
		t.Skip("name not representable on this filesystem")
	}
	r.write(name, "x")
	sha := r.commitAt("odd", t0)
	got, err := ChangedFiles(context.Background(), r.gitClient(), sha)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{name}) {
		t.Fatalf("got %q", got)
	}
}

func TestResolveCommitReadsParentTime(t *testing.T) {
	r := newTestRepo(t)
	r.write("a", "1")
	r.commitAt("first", t0)
	r.write("a", "2")
	sha := r.commitAt("second\n\nbody", t0.Add(90*time.Minute))
	c, err := ResolveCommit(context.Background(), r.gitClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.SHA != sha || c.Subject != "second" || !c.CommitTime.Equal(t0.Add(90*time.Minute)) || !c.ParentTime.Equal(t0) || len(c.Parents) != 1 {
		t.Fatalf("got %+v", c)
	}
	if _, err := ResolveCommit(context.Background(), r.gitClient(), "does-not-exist"); err == nil {
		t.Fatal("want an error for an unknown revision")
	}
}

func TestAddLinksCreatesMergesAndIsIdempotent(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	r.write("a", "1")
	sha := r.commitAt("c", t0)
	g := r.gitClient()

	if note, err := ReadNote(ctx, g, sha); err != nil || note != "" {
		t.Fatalf("fresh repo note %q, %v", note, err)
	}
	changed, err := AddLinks(ctx, g, sha, []Link{{"codex", "s1"}})
	if err != nil || !changed {
		t.Fatalf("first add: %v %v", changed, err)
	}
	// A person's own note line on the same commit survives Beacon's update.
	r.git("notes", "--ref="+NotesRef, "append", "-m", "Reviewed-by: someone", sha)
	changed, err = AddLinks(ctx, g, sha, []Link{{"codex", "s1"}, {"claude_code", "s2"}})
	if err != nil || !changed {
		t.Fatalf("second add: %v %v", changed, err)
	}
	changed, err = AddLinks(ctx, g, sha, []Link{{"claude_code", "s2"}})
	if err != nil || changed {
		t.Fatalf("repeat add changed=%v err=%v", changed, err)
	}
	note, _ := ReadNote(ctx, g, sha)
	want := "beacon:codex/s1\nReviewed-by: someone\nbeacon:claude_code/s2"
	if note != want {
		t.Fatalf("note %q, want %q", note, want)
	}
	if refs := r.git("for-each-ref", "--format=%(refname)", "refs/notes/"); refs != NotesRef {
		t.Fatalf("leftover refs:\n%s", refs)
	}
	if changed, err := AddLinks(ctx, g, sha, []Link{{"Bad Harness", "x"}}); err != nil || changed {
		t.Fatalf("invalid links must be ignored: %v %v", changed, err)
	}
}

func TestAddLinksConcurrentWritersLoseNothing(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	var shas []string
	for i := 0; i < 3; i++ {
		r.write("f", fmt.Sprint(i))
		shas = append(shas, r.commitAt(fmt.Sprint("c", i), t0.Add(time.Duration(i)*time.Minute)))
	}
	g := r.gitClient()
	const writers = 6
	var wg sync.WaitGroup
	errs := make(chan error, writers*len(shas))
	for w := 0; w < writers; w++ {
		for _, sha := range shas {
			wg.Add(1)
			go func(w int, sha string) {
				defer wg.Done()
				// Retries are bounded, so a writer may legitimately give up under this much
				// contention; it must then say so rather than drop the link silently.
				for attempt := 0; attempt < 10; attempt++ {
					_, err := AddLinks(ctx, g, sha, []Link{{"codex", fmt.Sprint("w", w)}})
					if err == nil {
						return
					}
					if !errors.Is(err, errRefMoved) {
						errs <- err
						return
					}
				}
				errs <- fmt.Errorf("writer %d never landed on %s", w, sha)
			}(w, sha)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for _, sha := range shas {
		note, err := ReadNote(ctx, g, sha)
		if err != nil {
			t.Fatal(err)
		}
		got := keys(ParseNote(note))
		sort.Strings(got)
		var want []string
		for w := 0; w < writers; w++ {
			want = append(want, fmt.Sprint("codex/w", w))
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: links %v, want %v", sha[:7], got, want)
		}
	}
	if refs := r.git("for-each-ref", "--format=%(refname)", "refs/notes/"); refs != NotesRef {
		t.Fatalf("leftover transaction refs:\n%s", refs)
	}
}

func TestListLinked(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	g := r.gitClient()
	if got, err := ListLinked(ctx, g, "", 10); err != nil || len(got) != 0 {
		t.Fatalf("empty repo: %v %v", got, err)
	}
	r.write("a", "1")
	first := r.commitAt("first", t0)
	r.write("a", "2")
	r.commitAt("second (unlinked)", t0.Add(time.Minute))
	r.write("a", "3")
	third := r.commitAt("third", t0.Add(2*time.Minute))
	if _, err := AddLinks(ctx, g, first, []Link{{"cursor", "c1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddLinks(ctx, g, third, []Link{{"codex", "x"}, {"claude_code", "y"}}); err != nil {
		t.Fatal(err)
	}
	// A note on the ref that is not Beacon's is not a linked commit.
	r.git("notes", "--ref="+NotesRef, "add", "-m", "just text", "HEAD~1")
	// A displayRef the user configured must not leak into the parsed output.
	r.git("notes", "--ref=refs/notes/commits", "add", "-m", "beacon:codex/forged", third)
	r.git("config", "notes.displayRef", "refs/notes/commits")

	got, err := ListLinked(ctx, g, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SHA != third || got[1].SHA != first {
		t.Fatalf("got %+v", got)
	}
	if !reflect.DeepEqual(keys(got[0].Links), []string{"codex/x", "claude_code/y"}) || got[0].Subject != "third" {
		t.Fatalf("third: %+v", got[0])
	}
	if !got[1].CommitTime.Equal(t0) {
		t.Fatalf("commit time %v", got[1].CommitTime)
	}
	// The limit counts commits walked, linked or not.
	if got, _ := ListLinked(ctx, g, "", 2); len(got) != 1 || got[0].SHA != third {
		t.Fatalf("limit 2: %+v", got)
	}
}
