package gitlink

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// fakeBeacon writes an executable that records each invocation's arguments and working directory
// to a file, and returns both paths.
func fakeBeacon(t *testing.T, dir string) (bin, calls string) {
	t.Helper()
	testenv.RequirePOSIXExecutableFixtures(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "beacon")
	calls = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"$(pwd -P) $*\" >> " + shellQuote(calls) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, calls
}

func readCalls(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (r *testRepo) commitWithEnv(message string, extra ...string) {
	r.t.Helper()
	r.git("add", "-A")
	cmd := exec.Command("git", "-C", r.root, "commit", "-q", "--allow-empty", "-m", message)
	cmd.Env = append(append([]string{}, r.env...), extra...)
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("commit: %v\n%s", err, out)
	}
}

func TestInstallHooksFreshRepository(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	bin, calls := fakeBeacon(t, filepath.Join(t.TempDir(), "bin with 'quote"))
	repo := r.open()

	res, err := InstallHooks(ctx, repo, InstallOptions{BeaconPath: bin})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reports) != len(ManagedHooks) || res.Reports[0].Action != "created" || !res.RewriteRefSet {
		t.Fatalf("install result %+v", res)
	}
	hook, _ := os.ReadFile(res.Reports[0].Path)
	if !strings.HasPrefix(string(hook), "#!/bin/sh\n"+hookBlockStart) {
		t.Fatalf("hook:\n%s", hook)
	}
	if testenv.HasPOSIXFileModes() {
		for _, p := range []string{res.Reports[0].Path, res.Reports[0].Script} {
			if info, _ := os.Stat(p); info.Mode()&0o111 == 0 {
				t.Fatalf("%s is not executable", p)
			}
		}
	}
	if got := r.git("config", "--get-all", "notes.rewriteRef"); got != NotesRef {
		t.Fatalf("rewriteRef %q", got)
	}

	r.write("a", "1")
	r.commitWithEnv("first")
	got := readCalls(t, calls)
	root, _ := filepath.EvalSymlinks(r.root)
	if len(got) != 1 || got[0] != root+" git hook post-commit" {
		t.Fatalf("calls %q, want one from %s", got, root)
	}

	again, err := InstallHooks(ctx, repo, InstallOptions{BeaconPath: bin})
	if err != nil || again.Reports[0].Action != "unchanged" || again.Reports[1].Action != "unchanged" || again.RewriteRefSet {
		t.Fatalf("second install %+v %v", again, err)
	}
	if got := r.git("config", "--get-all", "notes.rewriteRef"); got != NotesRef {
		t.Fatalf("rewriteRef duplicated: %q", got)
	}

	st, err := HookStatus(ctx, repo)
	if err != nil || !st.Installed() || !st.RewriteRef {
		t.Fatalf("status %+v %v", st, err)
	}
}

func TestInstallHooksKeepsAnExistingShellHook(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	bin, calls := fakeBeacon(t, filepath.Join(t.TempDir(), "bin"))
	repo := r.open()
	loc, _ := ResolveHooks(ctx, repo)
	marker := filepath.Join(t.TempDir(), "theirs.log")
	// Ends in `exit 0`: a block appended after it would never run.
	original := "#!/usr/bin/env bash\nset -e\necho ran >> '" + marker + "'\nexit 0\n"
	if err := os.MkdirAll(loc.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loc.Dir, "post-commit"), []byte(original), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := InstallHooks(ctx, repo, InstallOptions{BeaconPath: bin})
	if err != nil || res.Reports[0].Action != "added" {
		t.Fatalf("install %+v %v", res, err)
	}
	r.write("a", "1")
	r.commitWithEnv("c")
	if len(readCalls(t, calls)) != 1 {
		t.Fatal("Beacon's hook did not run")
	}
	if data, _ := os.ReadFile(marker); string(data) != "ran\n" {
		t.Fatalf("the existing hook did not run: %q", data)
	}

	rm, err := RemoveHooks(ctx, repo)
	if err != nil || rm.Reports[0].Action != "removed" || !rm.RewriteRefRemoved {
		t.Fatalf("remove %+v %v", rm, err)
	}
	after, _ := os.ReadFile(filepath.Join(loc.Dir, "post-commit"))
	if string(after) != original {
		t.Fatalf("remove did not restore the hook:\n%q\nwant\n%q", after, original)
	}
	if _, err := os.Stat(filepath.Join(loc.Dir, "beacon-post-commit")); !os.IsNotExist(err) {
		t.Fatal("script left behind")
	}
}

func TestRemoveHooksDeletesAHookItCreatedAndKeepsOtherRewriteRefs(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	bin, _ := fakeBeacon(t, filepath.Join(t.TempDir(), "bin"))
	repo := r.open()
	r.git("config", "--add", "notes.rewriteRef", "refs/notes/commits")
	if _, err := InstallHooks(ctx, repo, InstallOptions{BeaconPath: bin}); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveHooks(ctx, repo); err != nil {
		t.Fatal(err)
	}
	loc, _ := ResolveHooks(ctx, repo)
	if _, err := os.Stat(filepath.Join(loc.Dir, "post-commit")); !os.IsNotExist(err) {
		t.Fatal("a hook that held only Beacon's block should be deleted")
	}
	if got := r.git("config", "--get-all", "notes.rewriteRef"); got != "refs/notes/commits" {
		t.Fatalf("rewriteRef %q", got)
	}
	// Removing twice is fine.
	again, err := RemoveHooks(ctx, repo)
	if err != nil || again.Reports[0].Action != "absent" || again.RewriteRefRemoved {
		t.Fatalf("second remove %+v %v", again, err)
	}
}

func TestInstallHooksRefusesANonShellHook(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	repo := r.open()
	loc, _ := ResolveHooks(ctx, repo)
	_ = os.MkdirAll(loc.Dir, 0o755)
	python := "#!/usr/bin/env python3\nprint('hi')\n"
	if err := os.WriteFile(filepath.Join(loc.Dir, "post-commit"), []byte(python), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := InstallHooks(ctx, repo, InstallOptions{BeaconPath: "/nonexistent/beacon"})
	if !errors.Is(err, ErrForeignHook) {
		t.Fatalf("got %v, want ErrForeignHook", err)
	}
	if data, _ := os.ReadFile(filepath.Join(loc.Dir, "post-commit")); string(data) != python {
		t.Fatal("the python hook was modified")
	}
	if _, err := os.Stat(filepath.Join(loc.Dir, "beacon-post-commit")); !os.IsNotExist(err) {
		t.Fatal("a refusal must not leave a script behind")
	}
	if out, _ := r.gitClient().Run(ctx, nil, "config", "--get-all", "notes.rewriteRef"); out != "" {
		t.Fatal("a refusal must not change config")
	}
}

func TestShellShebangs(t *testing.T) {
	for line, want := range map[string]bool{
		"#!/bin/sh": true, "#!/bin/bash": true, "#!/usr/bin/env bash": true, "#!/usr/bin/env -S bash -e": true,
		"#! /bin/sh": true, "#!/bin/zsh": true, "#!/usr/local/bin/dash": true,
		"#!/usr/bin/env python3": false, "#!/usr/bin/env node": false, "#!/usr/bin/perl": false, "#!/bin/fish": false,
		"#!/usr/bin/shellcheck": false,
	} {
		if got := shellShebang.MatchString(line); got != want {
			t.Errorf("%q: got %v, want %v", line, got, want)
		}
	}
}

func TestInstallHooksHonorsCoreHooksPath(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	bin, calls := fakeBeacon(t, filepath.Join(t.TempDir(), "bin"))
	r.git("config", "core.hooksPath", ".husky")
	repo := r.open()
	_, err := InstallHooks(ctx, repo, InstallOptions{BeaconPath: bin})
	if !errors.Is(err, ErrHooksPathSet) {
		t.Fatalf("got %v, want ErrHooksPathSet", err)
	}
	if _, err := os.Stat(filepath.Join(r.root, ".husky")); !os.IsNotExist(err) {
		t.Fatal("a refusal must not create the directory")
	}
	res, err := InstallHooks(ctx, repo, InstallOptions{BeaconPath: bin, AllowHooksPath: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(r.root, ".husky", "post-commit"); res.Reports[0].Path != want {
		t.Fatalf("installed at %s, want %s", res.Reports[0].Path, want)
	}
	r.write("a", "1")
	r.commitWithEnv("c")
	if len(readCalls(t, calls)) != 1 {
		t.Fatal("hook in core.hooksPath did not run")
	}
}

func TestInstallHooksFromALinkedWorktree(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	bin, calls := fakeBeacon(t, filepath.Join(t.TempDir(), "bin"))
	r.write("a", "1")
	r.commitAt("init", t0)
	wt := filepath.Join(filepath.Dir(r.root), "wt")
	r.git("worktree", "add", "-q", "-b", "feature", wt)
	wtRepo, err := OpenRepo(ctx, Git{Dir: wt, Env: r.env})
	if err != nil {
		t.Fatal(err)
	}
	res, err := InstallHooks(ctx, wtRepo, InstallOptions{BeaconPath: bin})
	if err != nil {
		t.Fatal(err)
	}
	common, _ := filepath.EvalSymlinks(filepath.Join(r.root, ".git", "hooks"))
	got, _ := filepath.EvalSymlinks(filepath.Dir(res.Reports[0].Path))
	if got != common {
		t.Fatalf("installed in %s, want the shared %s", got, common)
	}
	// A commit in either worktree runs the hook, from that worktree.
	w := &testRepo{t: t, root: wt, env: r.env}
	w.write("b", "1")
	w.commitWithEnv("in worktree")
	r.write("c", "1")
	r.commitWithEnv("in main")
	callsSeen := readCalls(t, calls)
	wtReal, _ := filepath.EvalSymlinks(wt)
	rootReal, _ := filepath.EvalSymlinks(r.root)
	if len(callsSeen) != 2 || !strings.HasPrefix(callsSeen[0], wtReal+" ") || !strings.HasPrefix(callsSeen[1], rootReal+" ") {
		t.Fatalf("calls %q", callsSeen)
	}
}

func TestHookScriptDisableAndPathFallback(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	binDir := filepath.Join(t.TempDir(), "bin")
	_, calls := fakeBeacon(t, binDir)
	repo := r.open()
	// The recorded path is gone (an upgrade moved the binary); beacon on PATH is used instead.
	if _, err := InstallHooks(ctx, repo, InstallOptions{BeaconPath: "/nonexistent/beacon"}); err != nil {
		t.Fatal(err)
	}
	path := "PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	r.write("a", "1")
	r.commitWithEnv("disabled", path, DisableEnv+"=0")
	if len(readCalls(t, calls)) != 0 {
		t.Fatal("BEACON_GIT_HOOKS=0 must skip the hook")
	}
	r.write("a", "2")
	r.commitWithEnv("enabled", path)
	if len(readCalls(t, calls)) != 1 {
		t.Fatal("the hook did not fall back to beacon on PATH")
	}
	// With no beacon anywhere the hook is a quiet no-op, and the commit still succeeds.
	r.write("a", "3")
	r.commitWithEnv("no beacon", "PATH=/usr/bin:/bin")
}

func TestHookStatusDetectsABrokenInstall(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	bin, _ := fakeBeacon(t, filepath.Join(t.TempDir(), "bin"))
	repo := r.open()
	if st, _ := HookStatus(ctx, repo); st.Installed() {
		t.Fatal("fresh repository reports installed")
	}
	res, err := InstallHooks(ctx, repo, InstallOptions{BeaconPath: bin})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(res.Reports[0].Script); err != nil {
		t.Fatal(err)
	}
	st, err := HookStatus(ctx, repo)
	if err != nil || st.Installed() || !st.States[0].BlockPresent || st.States[0].ScriptExists {
		t.Fatalf("status %+v %v", st, err)
	}
	// Setup repairs it.
	if _, err := InstallHooks(ctx, repo, InstallOptions{BeaconPath: bin}); err != nil {
		t.Fatal(err)
	}
	if st, _ := HookStatus(ctx, repo); !st.Installed() {
		t.Fatal("setup did not repair the install")
	}
}

func TestReplayInProgress(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	repo := r.open()
	r.write("f", "base\n")
	r.commitAt("base", t0)
	if ReplayInProgress(ctx, repo) {
		t.Fatal("idle repository reports a replay")
	}
	r.git("checkout", "-q", "-b", "side")
	r.write("f", "side\n")
	r.commitAt("side", t0.Add(time.Minute))
	r.git("checkout", "-q", "main")
	r.write("f", "main\n")
	r.commitAt("main", t0.Add(2*time.Minute))
	if _, err := r.gitClient().Run(ctx, nil, "cherry-pick", "side"); err == nil {
		t.Fatal("expected a conflict")
	}
	if !ReplayInProgress(ctx, repo) {
		t.Fatal("a conflicted cherry-pick is a replay")
	}
	r.git("cherry-pick", "--abort")
	if _, err := r.gitClient().Run(ctx, nil, "rebase", "side"); err == nil {
		t.Fatal("expected a conflict")
	}
	if !ReplayInProgress(ctx, repo) {
		t.Fatal("a conflicted rebase is a replay")
	}
	r.git("rebase", "--abort")
	if ReplayInProgress(ctx, repo) {
		t.Fatal("an aborted rebase is over")
	}
}

func TestAmendCarriesLinksThroughRewriteRef(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	bin, _ := fakeBeacon(t, filepath.Join(t.TempDir(), "bin"))
	repo := r.open()
	if _, err := InstallHooks(ctx, repo, InstallOptions{BeaconPath: bin}); err != nil {
		t.Fatal(err)
	}
	r.write("a", "1")
	r.commitWithEnv("c")
	if _, err := AddLinks(ctx, repo.Git, r.git("rev-parse", "HEAD"), []Link{{"codex_cli", "s"}}); err != nil {
		t.Fatal(err)
	}
	r.git("commit", "-q", "--amend", "-m", "c (amended)")
	note, err := ReadNote(ctx, repo.Git, "HEAD")
	if err != nil || !strings.Contains(note, "beacon:codex_cli/s") {
		t.Fatalf("amended commit note %q %v", note, err)
	}
}

func TestPostRewriteHookGetsGitsStdinAndArgument(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	testenv.RequirePOSIXExecutableFixtures(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "beacon")
	seen := filepath.Join(dir, "seen.log")
	// Record the arguments and whatever arrived on stdin.
	script := "#!/bin/sh\necho \"args: $*\" >> " + shellQuote(seen) + "\ncat >> " + shellQuote(seen) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := r.open()
	if _, err := InstallHooks(ctx, repo, InstallOptions{BeaconPath: bin}); err != nil {
		t.Fatal(err)
	}
	r.write("a", "1")
	r.commitWithEnv("c")
	old := r.git("rev-parse", "HEAD")
	r.git("commit", "-q", "--amend", "-m", "c2")
	amended := r.git("rev-parse", "HEAD")
	data, _ := os.ReadFile(seen)
	text := string(data)
	if !strings.Contains(text, "args: git hook post-rewrite amend") || !strings.Contains(text, old+" "+amended) {
		t.Fatalf("post-rewrite saw:\n%s", text)
	}
}

func TestNormalizeNoteFoldsAnAmendedCopy(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	r.write("a", "1")
	sha := r.commitAt("c", t0)
	g := r.gitClient()
	// What `git commit --amend` leaves: the hook's note, a blank line, then the copied old note.
	r.git("notes", "--ref="+NotesRef, "add", "-m", "beacon:codex_cli/a\nbeacon:codex_cli/b\n\nbeacon:codex_cli/a", sha)
	changed, err := NormalizeNote(ctx, g, sha)
	if err != nil || !changed {
		t.Fatalf("normalize: %v %v", changed, err)
	}
	if note, _ := ReadNote(ctx, g, sha); note != "beacon:codex_cli/a\nbeacon:codex_cli/b" {
		t.Fatalf("note %q", note)
	}
	if changed, err := NormalizeNote(ctx, g, sha); err != nil || changed {
		t.Fatalf("second normalize: %v %v", changed, err)
	}
	// A commit with no note stays without one.
	r.write("a", "2")
	bare := r.commitAt("d", t0.Add(time.Minute))
	if changed, err := NormalizeNote(ctx, g, bare); err != nil || changed {
		t.Fatalf("no-note normalize: %v %v", changed, err)
	}
	if note, _ := ReadNote(ctx, g, bare); note != "" {
		t.Fatalf("note appeared: %q", note)
	}
}
