//go:build !windows

package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/launchpkg"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/osuser"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// macOS's setgroups takes at most 16 groups, and admin and directory-bound accounts often have
// more: the child must still start, carrying the primary group. Elsewhere every group is kept.
func TestCredentialGroupsFitSetgroups(t *testing.T) {
	gids := make([]uint32, 20)
	for i := range gids {
		gids[i] = uint32(100 + i)
	}
	if got := credentialGroups(gids, "darwin"); len(got) != darwinMaxGroups || got[0] != 100 {
		t.Errorf("darwin, 20 groups: %v, want the first %d, primary first", got, darwinMaxGroups)
	}
	if got := credentialGroups(gids[:darwinMaxGroups], "darwin"); len(got) != darwinMaxGroups {
		t.Errorf("darwin, %d groups: %v, want all of them", darwinMaxGroups, got)
	}
	if got := credentialGroups(gids, "linux"); len(got) != len(gids) {
		t.Errorf("linux, 20 groups: %v, want all of them", got)
	}
}

// The system job resolves packages as the console user, so the kernel decides what of the config
// they point at is read: a root-only npmrc that root would read as public is, for them, config
// Beacon cannot vouch for. Needs root and an unprivileged account, named by
// BEACON_TEST_RUN_AS_USER, that may run this test binary.
func TestPackagesAsUserReadsWithTheUsersRights(t *testing.T) {
	name := os.Getenv("BEACON_TEST_RUN_AS_USER")
	if os.Geteuid() != 0 || name == "" {
		t.Skip("needs root and BEACON_TEST_RUN_AS_USER")
	}
	testenv.RequireCommands(t, "id")
	account, err := osuser.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	prev := packageResolverCommand
	t.Cleanup(func() { packageResolverCommand = prev })
	packageResolverCommand = func(ctx context.Context, _ string) *exec.Cmd { return helperResolver(ctx, "resolve-packages") }

	npmrcIn := func(mode os.FileMode) string {
		dir, err := os.MkdirTemp("", "beacon-npmrc-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		path := filepath.Join(dir, "npmrc")
		if err := os.WriteFile(path, []byte("registry=https://registry.npmjs.org/\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	resolve, err := packagesAsUser(account.UID, account.HomeDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		npmrc   string
		private bool
	}{
		{npmrcIn(0o755), false},
		{npmrcIn(0o700), true},
	} {
		env := launchpkg.RegistryEnv{Home: account.HomeDir, Environ: []string{"npm_config_userconfig=" + tc.npmrc}}
		if root := launchpkg.ResolveAll(env, testQueries[:1]); root[0].Package.Private {
			t.Fatalf("root read %s as private: %+v", tc.npmrc, root)
		}
		answers, err := resolve(env, testQueries[:1])
		if err != nil {
			t.Fatal(err)
		}
		if answers[0].Package.Private != tc.private {
			t.Errorf("as %s, userconfig=%s: %+v, want private %v", name, tc.npmrc, answers[0], tc.private)
		}
	}
}
