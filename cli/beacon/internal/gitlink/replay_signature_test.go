package gitlink

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReplayDetectedWithLogShowSignature(t *testing.T) {
	r := newTestRepo(t)
	key := filepath.Join(t.TempDir(), "key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	r.git("config", "gpg.format", "ssh")
	r.git("config", "user.signingkey", key)
	r.git("config", "commit.gpgsign", "true")
	ctx := context.Background()
	repo := r.open()
	r.write("f", "base\n")
	r.commitAt("base", t0)
	r.git("checkout", "-q", "-b", "side")
	r.write("s1", "1")
	r.commitAt("side1", t0)
	r.git("checkout", "-q", "main")
	r.git("config", "log.showSignature", "true")
	r.git("cherry-pick", "side")
	if !ReplayInProgress(ctx, repo) {
		t.Errorf("clean signed cherry-pick with log.showSignature: replay=false; reflog %q", r.git("reflog", "-1", "--format=%gs", "HEAD"))
	}
}
