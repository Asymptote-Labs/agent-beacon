//go:build !windows

package cmd

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"syscall"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/launchpkg"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/osuser"
)

// packagesAsUser returns a resolver that runs resolve-packages as the account uid, whose home is
// home, with its primary and supplementary groups. The kernel then decides what that account may
// read, so no answer depends on root's view: a lookup the child cannot make, it treats as config
// it cannot vouch for. A group the child does not carry (one id(1) did not report, or one past
// macOS's limit) only takes rights away, which errs the same way.
func packagesAsUser(uid int, home string) (packageResolver, error) {
	gids, err := osuser.GroupIDs(uid)
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find the beacon executable: %w", err)
	}
	groups := credentialGroups(gids, runtime.GOOS)
	return func(env launchpkg.RegistryEnv, queries []launchpkg.Query) ([]launchpkg.Answer, error) {
		ctx, cancel := context.WithTimeout(context.Background(), packageResolveTimeout)
		defer cancel()
		c := packageResolverCommand(ctx, exe)
		c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
			Uid: uint32(uid), Gid: groups[0], Groups: groups,
		}}
		// None of root's environment: the request carries everything the child reads.
		c.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
		c.Dir = "/"
		return resolvePackagesWith(c, env, queries)
	}, nil
}

// darwinMaxGroups is macOS's NGROUPS: its setgroups(2) refuses a longer list with EINVAL, so the
// child would never start.
const darwinMaxGroups = 16

// credentialGroups are the groups the child carries: gids, primary first. On macOS, admin and
// directory-bound accounts often belong to more groups than setgroups takes, so the list is cut at
// the limit, keeping the primary group.
func credentialGroups(gids []uint32, goos string) []uint32 {
	if goos == "darwin" && len(gids) > darwinMaxGroups {
		return gids[:darwinMaxGroups]
	}
	return gids
}
