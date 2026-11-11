//go:build !windows

package cmd

import (
	"context"
	"fmt"
	"os"
	"syscall"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/launchpkg"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/osuser"
)

// packagesAsUser returns a resolver that runs resolve-packages as the account uid, whose home is
// home, with its primary and supplementary groups. The kernel then decides what that account may
// read, so no answer depends on root's view: a lookup the child cannot make, it treats as config
// it cannot vouch for. A group id(1) did not report only takes rights away, which errs the same way.
func packagesAsUser(uid int, home string) (packageResolver, error) {
	groups, err := osuser.GroupIDs(uid)
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find the beacon executable: %w", err)
	}
	supplementary := make([]uint32, len(groups))
	for i, gid := range groups {
		supplementary[i] = uint32(gid)
	}
	return func(env launchpkg.RegistryEnv, queries []launchpkg.Query) ([]launchpkg.Answer, error) {
		ctx, cancel := context.WithTimeout(context.Background(), packageResolveTimeout)
		defer cancel()
		c := packageResolverCommand(ctx, exe)
		c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
			Uid: uint32(uid), Gid: supplementary[0], Groups: supplementary,
		}}
		// None of root's environment: the request carries everything the child reads.
		c.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
		c.Dir = "/"
		return resolvePackagesWith(c, env, queries)
	}, nil
}
