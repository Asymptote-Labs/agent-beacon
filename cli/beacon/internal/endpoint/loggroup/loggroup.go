// Package loggroup manages the group that may read a restricted system runtime log.
//
// In the restricted log access mode the system log is 0640 root:beacon-log: root writes it, members
// of the group read it, nobody else has access. Install creates the group when it is missing and
// puts nobody in it; an administrator adds the accounts that should read every user's sessions,
// such as a log shipper's service account. Uninstall leaves the group, since it may have members an
// administrator chose.
//
// Accounts and groups on both platforms can come from a directory service (SSSD, LDAP, AD, macOS
// Directory Services) that Go's CGO_ENABLED=0 os/user never consults, so lookups go through the
// system's own tools -- getent on Linux, dscl on macOS -- the way package osuser resolves accounts.
package loggroup

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Name is the readers group.
const Name = asymptoteobserve.RuntimeLogReadersGroup

const commandTimeout = 10 * time.Second

// ErrUnsupported is returned where Beacon cannot manage groups.
var ErrUnsupported = errors.New("managing the log readers group is not supported on this platform")

// run executes a system tool by absolute path and returns its standard output. A variable so tests
// can stand in for the account database, which a test cannot change portably.
var run = func(path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return string(out), fmt.Errorf("%s %s: %w: %s", path, strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return string(out), fmt.Errorf("%s %s: %w", path, strings.Join(args, " "), err)
	}
	return string(out), nil
}

// lookPath finds the first of candidates that exists. A variable for tests.
var lookPath = func(candidates ...string) (string, bool) {
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, true
		}
	}
	return "", false
}

// Ensure returns the group's gid, creating it as a system group when it does not exist.
func Ensure(name string) (gid int, created bool, err error) {
	if err := validName(name); err != nil {
		return 0, false, err
	}
	gid, found, err := Lookup(name)
	if err != nil {
		return 0, false, err
	}
	if found {
		return gid, false, nil
	}
	if err := create(name); err != nil {
		return 0, false, fmt.Errorf("create group %s: %w", name, err)
	}
	gid, found, err = Lookup(name)
	if err != nil {
		return 0, false, err
	}
	if !found {
		return 0, false, fmt.Errorf("group %s was created but cannot be found", name)
	}
	return gid, true, nil
}

// Lookup returns the group's gid and whether it exists.
func Lookup(name string) (gid int, found bool, err error) {
	if err := validName(name); err != nil {
		return 0, false, err
	}
	return lookup(name)
}

// validName accepts only what both platforms accept as a group name, so a name is never read as an
// option or a path by the tools it is passed to.
func validName(name string) error {
	if name == "" || len(name) > 32 || strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid group name %q", name)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return fmt.Errorf("invalid group name %q", name)
		}
	}
	return nil
}

func parseGID(field string) (int, error) {
	gid, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil || gid < 0 {
		return 0, fmt.Errorf("unexpected gid %q", field)
	}
	return gid, nil
}
