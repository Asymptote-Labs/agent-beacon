//go:build !windows

package onboarding

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// Root saving a profile it does not own (sudo on macOS keeps the person's HOME) keeps the person
// as its owner, so they can still read it. Run as root, the seam is real; otherwise the chown to
// the test's own account is what root would do with the person's.
func TestSaveAsRootKeepsTheProfileOwner(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	if err := Save(Profile{InstallID: "id", Onboarding: Onboarding{CompletedAt: "2026-11-01T00:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ProfilePath)
	before := owner(t, path)

	old := geteuid
	geteuid = func() int { return 0 }
	t.Cleanup(func() { geteuid = old })
	p := Load()
	p.History = &History{DeclinedAt: "2026-11-02T00:00:00Z"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if after := owner(t, path); after != before {
		t.Fatalf("owner after a root save = %v, want %v", after, before)
	}
	if got := Load(); !got.Prompted() || !got.HistoryDeclined() {
		t.Fatalf("profile after a root save = %+v", got)
	}
}

func owner(t *testing.T, path string) [2]uint32 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	return [2]uint32{st.Uid, st.Gid}
}
