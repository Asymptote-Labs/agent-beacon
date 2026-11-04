//go:build linux

package loggroup

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// lookup asks getent, which consults NSS. getent exits 2 when the key is not found.
func lookup(name string) (int, bool, error) {
	getent, ok := lookPath("getent", "/usr/bin/getent", "/bin/getent")
	if !ok {
		return 0, false, errors.New("getent is not available to look up groups")
	}
	out, err := run(getent, "group", name)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
			return 0, false, nil
		}
		return 0, false, err
	}
	// name:password:gid:members
	fields := strings.Split(strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]), ":")
	if len(fields) < 3 || fields[0] != name {
		return 0, false, fmt.Errorf("unexpected getent output %q", strings.TrimSpace(out))
	}
	gid, err := parseGID(fields[2])
	if err != nil {
		return 0, false, err
	}
	return gid, true, nil
}

// create adds a local system group with groupadd, or busybox addgroup where that is all there is.
func create(name string) error {
	if groupadd, ok := lookPath("groupadd", "/usr/sbin/groupadd", "/sbin/groupadd"); ok {
		_, err := run(groupadd, "--system", name)
		return err
	}
	if addgroup, ok := lookPath("addgroup", "/usr/sbin/addgroup", "/sbin/addgroup"); ok {
		_, err := run(addgroup, "-S", name)
		return err
	}
	return errors.New("neither groupadd nor addgroup is available")
}
