//go:build darwin

package loggroup

import (
	"fmt"
	"strings"
)

const (
	dscl        = "/usr/bin/dscl"
	dseditgroup = "/usr/sbin/dseditgroup"
)

// lookup reads the group from the local directory node, where dseditgroup creates it.
func lookup(name string) (int, bool, error) {
	out, err := run(dscl, ".", "-read", "/Groups/"+name, "PrimaryGroupID")
	if err != nil {
		// dscl reports a missing record on stderr and exits non-zero; anything else it says on
		// stdout is output we cannot use.
		if strings.Contains(err.Error(), "eDSRecordNotFound") || strings.Contains(err.Error(), "does not exist") {
			return 0, false, nil
		}
		return 0, false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "PrimaryGroupID:"); ok {
			gid, err := parseGID(value)
			if err != nil {
				return 0, false, err
			}
			return gid, true, nil
		}
	}
	return 0, false, fmt.Errorf("no PrimaryGroupID in %q", strings.TrimSpace(out))
}

func create(name string) error {
	if _, err := run(dseditgroup, "-o", "create", "-r", "Beacon log readers", name); err != nil {
		return err
	}
	return nil
}
