package gitlink

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestWindow(t *testing.T) {
	commit := func(parentAgo time.Duration) Commit {
		c := Commit{CommitTime: t0}
		if parentAgo != 0 {
			c.ParentTime = t0.Add(-parentAgo)
		}
		return c
	}
	for _, tc := range []struct {
		name      string
		commit    Commit
		wantSince time.Duration // before t0
	}{
		{"parent a minute ago still sees the floor", commit(time.Minute), DefaultMinLookback},
		{"parent five hours ago bounds the window", commit(5 * time.Hour), 5 * time.Hour},
		{"parent a month ago is capped", commit(30 * 24 * time.Hour), DefaultMaxLookback},
		{"root commit uses the cap", commit(0), DefaultMaxLookback},
		{"parent after the commit (clock skew) keeps the floor", Commit{CommitTime: t0, ParentTime: t0.Add(time.Hour)}, DefaultMinLookback},
	} {
		since, until := Window(tc.commit, 0, DefaultMaxLookback)
		if want := t0.Add(-tc.wantSince); !since.Equal(want) {
			t.Errorf("%s: since %v, want %v", tc.name, since, want)
		}
		if !until.Equal(t0.Add(commitTimeSlack)) {
			t.Errorf("%s: until %v", tc.name, until)
		}
	}
}

func TestReadEvidence(t *testing.T) {
	r := newTestRepo(t)
	repo := r.open()
	root := repo.Root
	logPath := filepath.Join(t.TempDir(), "logs", "runtime.jsonl")
	since, until := t0.Add(-time.Hour), t0
	in := t0.Add(-10 * time.Minute)
	writeLog(t, logPath,
		// Absolute path, file event.
		logEvent{at: in, harness: "claude_code", session: "s1", cwd: root, action: "file.modified", filePath: filepath.Join(root, "a.go"), operation: "modify"},
		// A read is not a write.
		logEvent{at: in, harness: "claude_code", session: "s1", cwd: root, action: "tool.invoked", filePath: filepath.Join(root, "read-only.go"), operation: "read"},
		// A write-type operation on a tool event counts.
		logEvent{at: in, harness: "claude_code", session: "s1", cwd: root, action: "tool.invoked", filePath: filepath.Join(root, "b.go"), operation: "create"},
		// Outside the window on either side.
		logEvent{at: since.Add(-time.Second), harness: "claude_code", session: "s1", action: "file.modified", filePath: filepath.Join(root, "old.go"), operation: "modify"},
		logEvent{at: until.Add(time.Second), harness: "claude_code", session: "s1", action: "file.modified", filePath: filepath.Join(root, "late.go"), operation: "modify"},
		// Outside the repository, and inside .git.
		logEvent{at: in, harness: "claude_code", session: "s1", action: "file.modified", filePath: filepath.Join(filepath.Dir(root), "elsewhere.go"), operation: "modify"},
		logEvent{at: in, harness: "claude_code", session: "s1", action: "file.modified", filePath: filepath.Join(root, ".git", "config"), operation: "modify"},
		// Relative path whose working directory only a later event reports.
		logEvent{at: in, harness: "cursor", session: "c1", action: "file.modified", filePath: "rel/c.go", operation: "modify"},
		logEvent{at: in.Add(time.Second), harness: "cursor", session: "c1", cwd: root, action: "session.started"},
		// Shell and patch evidence, relative to the session's directory.
		logEvent{at: in, harness: "codex", session: "x1", cwd: filepath.Join(root, "sub"), action: "command.executed", command: "rm stale.txt && echo ok > out.log"},
		logEvent{at: in, harness: "codex", session: "x1", cwd: root, action: "tool.invoked", args: map[string]interface{}{"input": "*** Begin Patch\n*** Update File: p.go\n*** End Patch"}},
		// A raw harness name is normalized, so both rows are one session.
		logEvent{at: in, harness: "claude", session: "s1", cwd: root, action: "file.created", filePath: filepath.Join(root, "c.go")},
		// A session that wrote nothing in the repository is not evidence.
		logEvent{at: in, harness: "gemini_cli", session: "g1", cwd: root, action: "prompt.submitted"},
		// No session id.
		logEvent{at: in, harness: "claude_code", action: "file.modified", filePath: filepath.Join(root, "orphan.go"), operation: "modify"},
	)
	got, err := ReadEvidence(context.Background(), repo, logPath, since, until)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]string{}
	for key, s := range got {
		for f := range s.Files {
			files[key] = append(files[key], f)
		}
		sort.Strings(files[key])
	}
	want := map[string][]string{
		"claude_code/s1": {"a.go", "b.go", "c.go"},
		"cursor/c1":      {"rel/c.go"},
		"codex_cli/x1":   {"p.go", "sub/out.log", "sub/stale.txt"},
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("evidence\n got %v\nwant %v", files, want)
	}
	if !got["claude_code/s1"].Files["a.go"].Equal(in) {
		t.Fatalf("write time %v", got["claude_code/s1"].Files["a.go"])
	}
}

func TestReadEvidenceStopsAtArchivesOlderThanTheWindow(t *testing.T) {
	r := newTestRepo(t)
	repo := r.open()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	in := t0.Add(-5 * time.Minute)
	ev := func(session string) logEvent {
		return logEvent{at: in, harness: "codex", session: session, cwd: repo.Root, action: "file.modified", filePath: filepath.Join(repo.Root, "f.go"), operation: "modify"}
	}
	writeLog(t, logPath, ev("active"))
	writeLog(t, logPath+".1", ev("archive1"))
	writeLog(t, logPath+".2", ev("archive2")) // in the window by timestamp, but the file is stale
	writeLog(t, logPath+".x", ev("not-an-archive"))
	touch := func(path string, at time.Time) {
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	touch(logPath, t0)
	touch(logPath+".1", t0.Add(-30*time.Minute))
	touch(logPath+".2", t0.Add(-3*time.Hour))
	touch(logPath+".x", t0)

	got, err := ReadEvidence(context.Background(), repo, logPath, t0.Add(-time.Hour), t0)
	if err != nil {
		t.Fatal(err)
	}
	var sessions []string
	for key := range got {
		sessions = append(sessions, key)
	}
	sort.Strings(sessions)
	if !reflect.DeepEqual(sessions, []string{"codex_cli/active", "codex_cli/archive1"}) {
		t.Fatalf("sessions %v", sessions)
	}
}

func TestReadEvidenceMissingLogIsEmpty(t *testing.T) {
	r := newTestRepo(t)
	got, err := ReadEvidence(context.Background(), r.open(), filepath.Join(t.TempDir(), "absent.jsonl"), t0.Add(-time.Hour), t0)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestReadEvidenceThroughASymlinkedRoot(t *testing.T) {
	r := newTestRepo(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(r.root, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	repo := r.open()
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	// The agent ran in the symlinked spelling of the repository.
	writeLog(t, logPath, logEvent{at: t0, harness: "codex", session: "s", cwd: link, action: "file.modified", filePath: filepath.Join(link, "x.go"), operation: "modify"})
	got, err := ReadEvidence(context.Background(), repo, logPath, t0.Add(-time.Hour), t0)
	if err != nil {
		t.Fatal(err)
	}
	if s := got["codex_cli/s"]; s == nil || s.Files["x.go"].IsZero() {
		t.Fatalf("got %+v", got)
	}
}

func TestAttributeLinksByOverlapAndRanks(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	r.write("a.go", "1")
	r.write("b.go", "1")
	r.write("c.go", "1")
	r.commitAt("base", t0.Add(-3*time.Hour))

	root := r.open().Root
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	write := func(at time.Duration, harness, session, file string) logEvent {
		return logEvent{at: t0.Add(-at), harness: harness, session: session, cwd: root, action: "file.modified", filePath: filepath.Join(root, file), operation: "modify"}
	}
	writeLog(t, logPath,
		write(50*time.Minute, "claude_code", "two-files", "a.go"),
		write(40*time.Minute, "claude_code", "two-files", "b.go"),
		write(10*time.Minute, "codex", "one-file-recent", "c.go"),
		write(30*time.Minute, "cursor", "one-file-older", "c.go"),
		write(20*time.Minute, "cursor", "unrelated", "not-in-commit.go"),
		// Before the parent commit: that work went into the parent.
		write(4*time.Hour, "codex", "before-parent", "a.go"),
	)
	r.write("a.go", "2")
	r.write("b.go", "2")
	r.write("c.go", "2")
	sha := r.commitAt("change", t0)
	repo := r.open()

	dry, err := Attribute(ctx, repo, Options{LogPath: logPath, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"claude_code/two-files", "codex_cli/one-file-recent", "cursor/one-file-older"}
	if got := keys(dry.Added); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("dry run links %v, want %v", got, wantOrder)
	}
	if dry.NoteUpdated || dry.Commit.SHA != sha || dry.ChangedFiles != 3 {
		t.Fatalf("dry run result %+v", dry)
	}
	if note, _ := ReadNote(ctx, repo.Git, sha); note != "" {
		t.Fatalf("dry run wrote a note: %q", note)
	}
	if got := dry.Candidates[0].Files; !reflect.DeepEqual(got, []string{"a.go", "b.go"}) {
		t.Fatalf("candidate files %v", got)
	}

	res, err := Attribute(ctx, repo, Options{LogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	if !res.NoteUpdated {
		t.Fatal("note not updated")
	}
	note, _ := ReadNote(ctx, repo.Git, sha)
	if got := keys(ParseNote(note)); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("note links %v", got)
	}

	// Running again finds the same sessions already linked and writes nothing.
	again, err := Attribute(ctx, repo, Options{LogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	if again.NoteUpdated || len(again.Added) != 0 || len(again.Candidates) != 3 || !again.Candidates[0].AlreadyLinked {
		t.Fatalf("second run %+v", again)
	}
}

func TestAttributeEmptyCommitAndNoEvidence(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	r.write("a", "1")
	r.commitAt("base", t0.Add(-time.Hour))
	r.commitAt("empty", t0)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	res, err := Attribute(ctx, r.open(), Options{LogPath: logPath})
	if err != nil || res.ChangedFiles != 0 || len(res.Added) != 0 {
		t.Fatalf("empty commit: %+v %v", res, err)
	}
	r.write("a", "2")
	r.commitAt("real", t0.Add(time.Minute))
	res, err = Attribute(ctx, r.open(), Options{LogPath: logPath})
	if err != nil || res.ChangedFiles != 1 || len(res.Candidates) != 0 || res.NoteUpdated {
		t.Fatalf("no evidence: %+v %v", res, err)
	}
}

func TestAttributeOlderCommitUsesItsOwnWindow(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	r.write("a.go", "1")
	r.commitAt("base", t0.Add(-48*time.Hour))
	r.write("a.go", "2")
	old := r.commitAt("old", t0.Add(-24*time.Hour))
	r.write("a.go", "3")
	r.commitAt("new", t0)
	root := r.open().Root
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeLog(t, logPath,
		logEvent{at: t0.Add(-25 * time.Hour), harness: "codex", session: "made-old", cwd: root, action: "file.modified", filePath: filepath.Join(root, "a.go"), operation: "modify"},
		logEvent{at: t0.Add(-time.Hour), harness: "codex", session: "made-new", cwd: root, action: "file.modified", filePath: filepath.Join(root, "a.go"), operation: "modify"},
	)
	res, err := Attribute(ctx, r.open(), Options{LogPath: logPath, Rev: old, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(res.Added); !reflect.DeepEqual(got, []string{"codex_cli/made-old"}) {
		t.Fatalf("old commit links %v", got)
	}
}
