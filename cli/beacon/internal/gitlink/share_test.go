package gitlink

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// team is a bare remote with two clones of it, alice and bob, sharing a first commit.
type team struct {
	remote     string
	alice, bob *testRepo
	first      string
}

func newTeam(t *testing.T) *team {
	t.Helper()
	a := newTestRepo(t)
	base := filepath.Dir(a.root)
	remote := filepath.Join(base, "remote.git")
	a.git("init", "-q", "--bare", "-b", "main", remote)
	a.write("f", "1")
	first := a.commitAt("first", t0)
	a.git("remote", "add", "origin", remote)
	a.git("push", "-q", "origin", "main")
	bobRoot := filepath.Join(base, "bob")
	a.git("clone", "-q", remote, bobRoot)
	b := &testRepo{t: t, root: bobRoot, env: a.env}
	return &team{remote: remote, alice: a, bob: b, first: first}
}

func linksOn(t *testing.T, r *testRepo, ref, commit string) []string {
	t.Helper()
	note, err := readNote(context.Background(), r.gitClient(), ref, commit)
	if err != nil {
		t.Fatal(err)
	}
	got := keys(ParseNote(note))
	sort.Strings(got)
	return got
}

func TestPushNotesMergesTeammatesInsteadOfOverwriting(t *testing.T) {
	tm := newTeam(t)
	ctx := context.Background()
	alice, bob := tm.alice.open(), tm.bob.open()
	if _, err := AddLinks(ctx, alice.Git, tm.first, []Link{{"codex_cli", "alice"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddLinks(ctx, bob.Git, tm.first, []Link{{"cursor", "bob"}}); err != nil {
		t.Fatal(err)
	}
	res, err := PushNotes(ctx, alice, "origin")
	if err != nil || !res.Pushed || res.Merged {
		t.Fatalf("alice push %+v %v", res, err)
	}
	// Bob's history of refs/notes/beacon does not contain Alice's: a plain push would be rejected.
	res, err = PushNotes(ctx, bob, "origin")
	if err != nil || !res.Pushed || !res.Merged {
		t.Fatalf("bob push %+v %v", res, err)
	}
	remote := &testRepo{t: t, root: tm.remote, env: tm.alice.env}
	want := []string{"codex_cli/alice", "cursor/bob"}
	if got := linksOn(t, remote, NotesRef, tm.first); !reflect.DeepEqual(got, want) {
		t.Fatalf("remote links %v, want %v", got, want)
	}
	if got := linksOn(t, tm.bob, NotesRef, tm.first); !reflect.DeepEqual(got, want) {
		t.Fatalf("bob's local links %v", got)
	}
	// Alice catches up the same way.
	if res, err := PushNotes(ctx, alice, "origin"); err != nil || !res.Merged {
		t.Fatalf("alice second push %+v %v", res, err)
	}
	if got := linksOn(t, tm.alice, NotesRef, tm.first); !reflect.DeepEqual(got, want) {
		t.Fatalf("alice's local links %v", got)
	}
	// Nothing left behind but the local ref and origin's tracking copy.
	refs := tm.bob.git("for-each-ref", "--format=%(refname)", "refs/notes/")
	if refs != NotesRef+"\n"+TrackingRef("origin") {
		t.Fatalf("refs:\n%s", refs)
	}
}

func TestPushNotesEdgeCases(t *testing.T) {
	tm := newTeam(t)
	ctx := context.Background()
	alice := tm.alice.open()
	// Nothing anywhere: nothing to push, no error.
	if res, err := PushNotes(ctx, alice, "origin"); err != nil || res.Pushed || res.Merged {
		t.Fatalf("empty push %+v %v", res, err)
	}
	if found, err := FetchNotes(ctx, alice, "origin"); err != nil || found {
		t.Fatalf("fetch from a remote without links: %v %v", found, err)
	}
	// A push to a URL rather than a named remote uses a throwaway incoming ref.
	if _, err := AddLinks(ctx, alice.Git, tm.first, []Link{{"codex_cli", "a"}}); err != nil {
		t.Fatal(err)
	}
	if res, err := PushNotes(ctx, alice, tm.remote); err != nil || !res.Pushed {
		t.Fatalf("push to URL %+v %v", res, err)
	}
	if refs := tm.alice.git("for-each-ref", "--format=%(refname)", "refs/notes/"); refs != NotesRef {
		t.Fatalf("a URL push must leave no incoming or tracking ref:\n%s", refs)
	}
	// An unreachable remote is an error, not a hang or a panic.
	if _, err := PushNotes(ctx, alice, filepath.Join(t.TempDir(), "missing.git")); err == nil {
		t.Fatal("want an error for an unreachable remote")
	}
}

func TestFetchKeepsLocalLinksAndListsTeammates(t *testing.T) {
	tm := newTeam(t)
	ctx := context.Background()
	alice, bob := tm.alice.open(), tm.bob.open()
	if _, err := AddLinks(ctx, alice.Git, tm.first, []Link{{"codex_cli", "alice"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := PushNotes(ctx, alice, "origin"); err != nil {
		t.Fatal(err)
	}
	// Bob has an unpushed link of his own on the same commit.
	if _, err := AddLinks(ctx, bob.Git, tm.first, []Link{{"cursor", "bob"}}); err != nil {
		t.Fatal(err)
	}
	if added, err := ConfigureFetch(ctx, bob, []string{"origin"}); err != nil || !reflect.DeepEqual(added, []string{"origin"}) {
		t.Fatalf("configure %v %v", added, err)
	}
	if added, _ := ConfigureFetch(ctx, bob, []string{"origin"}); len(added) != 0 {
		t.Fatal("configure is not idempotent")
	}
	// A plain `git fetch` never touches Bob's own links...
	tm.bob.git("fetch", "-q", "origin")
	if got := linksOn(t, tm.bob, NotesRef, tm.first); !reflect.DeepEqual(got, []string{"cursor/bob"}) {
		t.Fatalf("a fetch changed bob's local links: %v", got)
	}
	// ...and brings Alice's into the tracking ref.
	if got := linksOn(t, tm.bob, TrackingRef("origin"), tm.first); !reflect.DeepEqual(got, []string{"codex_cli/alice"}) {
		t.Fatalf("tracking links %v", got)
	}
	commits, err := ListLinked(ctx, bob.Git, "", 10)
	if err != nil || len(commits) != 1 {
		t.Fatalf("list %+v %v", commits, err)
	}
	got := keys(commits[0].Links)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"codex_cli/alice", "cursor/bob"}) {
		t.Fatalf("listed links %v", got)
	}
	// With no local ref at all, fetched links alone still list.
	carol := &testRepo{t: t, root: filepath.Join(filepath.Dir(tm.alice.root), "carol"), env: tm.alice.env}
	tm.alice.git("clone", "-q", tm.remote, carol.root)
	if found, err := FetchNotes(ctx, carol.open(), "origin"); err != nil || !found {
		t.Fatalf("carol fetch %v %v", found, err)
	}
	if commits, err := ListLinked(ctx, carol.gitClient(), "", 10); err != nil || len(commits) != 1 {
		t.Fatalf("carol list %+v %v", commits, err)
	}
	if removed, err := UnconfigureFetch(ctx, bob); err != nil || !reflect.DeepEqual(removed, []string{"origin"}) {
		t.Fatalf("unconfigure %v %v", removed, err)
	}
	if out := tm.bob.git("config", "--get-all", "remote.origin.fetch"); strings.Contains(out, "beacon") {
		t.Fatalf("refspec left behind: %s", out)
	}
}

func TestMergeNotesFromIntoEmptyAndUpToDate(t *testing.T) {
	tm := newTeam(t)
	ctx := context.Background()
	alice, bob := tm.alice.open(), tm.bob.open()
	if _, err := AddLinks(ctx, alice.Git, tm.first, []Link{{"codex_cli", "alice"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := PushNotes(ctx, alice, "origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchNotes(ctx, bob, "origin"); err != nil {
		t.Fatal(err)
	}
	// Bob has no local ref yet: the merge adopts the remote's.
	if changed, err := MergeNotesFrom(ctx, bob, TrackingRef("origin")); err != nil || !changed {
		t.Fatalf("merge into empty %v %v", changed, err)
	}
	if changed, err := MergeNotesFrom(ctx, bob, TrackingRef("origin")); err != nil || changed {
		t.Fatalf("merge when up to date %v %v", changed, err)
	}
	if changed, err := MergeNotesFrom(ctx, bob, TrackingRef("nowhere")); err != nil || changed {
		t.Fatalf("merge from a missing ref %v %v", changed, err)
	}
}

func TestShareNotesInstallAndRemove(t *testing.T) {
	tm := newTeam(t)
	ctx := context.Background()
	bin, calls := fakeBeacon(t, filepath.Join(t.TempDir(), "bin"))
	bob := tm.bob.open()
	on, off := true, false
	res, err := InstallHooks(ctx, bob, InstallOptions{BeaconPath: bin, ShareNotes: &on})
	if err != nil || len(res.Reports) != 3 || res.Reports[2].Hook != ShareHook || !reflect.DeepEqual(res.FetchAdded, []string{"origin"}) {
		t.Fatalf("install %+v %v", res, err)
	}
	st, _ := HookStatus(ctx, bob)
	if !st.Installed() || !st.Sharing || !reflect.DeepEqual(st.FetchRemotes, []string{"origin"}) {
		t.Fatalf("status %+v", st)
	}
	// The pre-push hook gets git's remote name and URL.
	tm.bob.write("g", "1")
	tm.bob.commitWithEnv("bob")
	tm.bob.git("push", "-q", "origin", "main")
	got := readCalls(t, calls)
	last := got[len(got)-1]
	if !strings.HasSuffix(last, "git hook pre-push origin "+tm.remote) {
		t.Fatalf("pre-push call %q", last)
	}
	// Re-running setup without the flag keeps sharing on.
	if _, err := InstallHooks(ctx, bob, InstallOptions{BeaconPath: bin}); err != nil {
		t.Fatal(err)
	}
	if st, _ := HookStatus(ctx, bob); !st.Sharing {
		t.Fatal("a plain re-run turned sharing off")
	}
	// --share-notes=false turns it off and leaves the rest installed.
	res, err = InstallHooks(ctx, bob, InstallOptions{BeaconPath: bin, ShareNotes: &off})
	if err != nil || !reflect.DeepEqual(res.FetchRemoved, []string{"origin"}) {
		t.Fatalf("turn off %+v %v", res, err)
	}
	if st, _ := HookStatus(ctx, bob); st.Sharing || !st.Installed() || len(st.FetchRemotes) != 0 {
		t.Fatalf("status after off %+v", st)
	}
	loc, _ := ResolveHooks(ctx, bob)
	if _, err := os.Stat(filepath.Join(loc.Dir, "pre-push")); !os.IsNotExist(err) {
		t.Fatal("pre-push hook left behind")
	}
	// Remove takes sharing out along with everything else.
	if _, err := InstallHooks(ctx, bob, InstallOptions{BeaconPath: bin, ShareNotes: &on}); err != nil {
		t.Fatal(err)
	}
	rm, err := RemoveHooks(ctx, bob)
	if err != nil || len(rm.Reports) != 3 || !reflect.DeepEqual(rm.FetchRemoved, []string{"origin"}) {
		t.Fatalf("remove %+v %v", rm, err)
	}
	if st, _ := HookStatus(ctx, bob); st.Sharing || len(st.States) != 2 || st.States[0].Installed {
		t.Fatalf("status after remove %+v", st)
	}
}

func TestErrorSummary(t *testing.T) {
	err := &GitError{Args: []string{"push"}, Message: "To /x/remote.git\n ! [rejected] refs/notes/beacon (fetch first)\nerror: failed to push some refs to '/x/remote.git'\nhint: Updates were rejected"}
	if got := ErrorSummary(err); got != "! [rejected] refs/notes/beacon (fetch first)" {
		t.Fatalf("got %q", got)
	}
	err = &GitError{Message: "fatal: '/gone.git' does not appear to be a git repository\nfatal: Could not read from remote repository."}
	if got := ErrorSummary(err); got != "'/gone.git' does not appear to be a git repository" {
		t.Fatalf("got %q", got)
	}
	if got := ErrorSummary(context.DeadlineExceeded); got != "context deadline exceeded" {
		t.Fatalf("got %q", got)
	}
}
