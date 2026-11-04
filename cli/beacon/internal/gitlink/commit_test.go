package gitlink

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A user with log.showSignature set gets git's signature check printed ahead of --format output
// for every signed commit. Signing with an SSH key and no allowed-signers file makes git print
// "No signature" there, which is enough to show the output is not shifted.
func TestCommitReadsIgnoreLogShowSignature(t *testing.T) {
	r := newTestRepo(t)
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	key := filepath.Join(t.TempDir(), "key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen: %v\n%s", err, out)
	}
	r.git("config", "gpg.format", "ssh")
	r.git("config", "user.signingkey", key)
	r.git("config", "commit.gpgsign", "true")

	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	r.write("a.txt", "one\n")
	first := r.commitAt("first", at)
	r.write("a.txt", "two\n")
	second := r.commitAt("second", at.Add(time.Hour))
	r.git("config", "log.showSignature", "true")

	ctx := context.Background()
	repo := r.open()
	commit, err := ResolveCommit(ctx, repo.Git, "")
	if err != nil {
		t.Fatal(err)
	}
	if commit.SHA != second || commit.Subject != "second" || len(commit.Parents) != 1 || commit.Parents[0] != first {
		t.Fatalf("ResolveCommit = %+v, want sha %s subject second parent %s", commit, second, first)
	}
	if !commit.ParentTime.Equal(at) {
		t.Fatalf("ParentTime = %v, want %v", commit.ParentTime, at)
	}

	if changed, err := AddLinks(ctx, repo.Git, second, []Link{{Harness: "claude_code", SessionID: "s1"}}); err != nil || !changed {
		t.Fatalf("AddLinks = %v, %v", changed, err)
	}
	linked, err := ListLinked(ctx, repo.Git, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(linked) != 1 || linked[0].SHA != second || linked[0].Subject != "second" {
		t.Fatalf("ListLinked = %+v, want one commit %s", linked, second)
	}
}
