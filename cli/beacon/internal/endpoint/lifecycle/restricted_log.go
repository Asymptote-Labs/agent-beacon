package lifecycle

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Restricted log mode (install --system --restricted-log) is the shape of a system-mode endpoint in
// which ordinary users can neither read nor write the runtime log. Hooks send each line to the
// root collector over asymptoteobserve.DefaultRuntimeLogSocketPath, the collector is the only
// writer, and the log directory is 0750 root:beacon-log with every file in it 0640. A log shipper
// reads it by joining the beacon-log group. See asymptoteobserve.RestrictedLog.

// RestrictedLogSupported reports whether this platform has restricted log mode.
func RestrictedLogSupported() bool {
	return runtime.GOOS == "linux" || runtime.GOOS == "darwin"
}

// runGroupCommand runs a group-administration command and returns its output. A variable for tests.
var runGroupCommand = func(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.Bytes(), err
}

// ensureLogGroup returns the beacon-log group's id, creating the group when it does not exist.
func ensureLogGroup() (int, error) {
	if gid, ok := lookupLogGroup(); ok {
		return gid, nil
	}
	group := asymptoteobserve.RuntimeLogGroup
	var out []byte
	var err error
	switch runtime.GOOS {
	case "darwin":
		out, err = runGroupCommand("dseditgroup", "-o", "create", "-r", "Beacon runtime log readers", group)
	case "linux":
		out, err = runGroupCommand("groupadd", "--system", group)
		if err != nil {
			// BusyBox and some minimal images have only addgroup.
			if more, err2 := runGroupCommand("addgroup", "-S", group); err2 == nil {
				out, err = more, nil
			}
		}
	default:
		return 0, fmt.Errorf("restricted log mode is not supported on %s", runtime.GOOS)
	}
	if err != nil {
		return 0, fmt.Errorf("create the %s group: %v: %s", group, err, strings.TrimSpace(string(out)))
	}
	gid, ok := lookupLogGroup()
	if !ok {
		return 0, fmt.Errorf("created the %s group but cannot read its id", group)
	}
	return gid, nil
}

// lookupLogGroup finds the beacon-log group through the system's own resolver: getent on Linux
// (which also sees LDAP and sssd groups), the directory service on macOS, where /etc/group does
// not list groups dseditgroup creates.
func lookupLogGroup() (int, bool) {
	group := asymptoteobserve.RuntimeLogGroup
	switch runtime.GOOS {
	case "darwin":
		out, err := runGroupCommand("dscl", ".", "-read", "/Groups/"+group, "PrimaryGroupID")
		if err != nil {
			return 0, false
		}
		fields := strings.Fields(string(out))
		if len(fields) == 0 {
			return 0, false
		}
		gid, err := strconv.Atoi(fields[len(fields)-1])
		return gid, err == nil && gid > 0
	case "linux":
		out, err := runGroupCommand("getent", "group", group)
		if err != nil {
			return 0, false
		}
		parts := strings.Split(strings.TrimSpace(string(out)), ":")
		if len(parts) < 3 {
			return 0, false
		}
		gid, err := strconv.Atoi(parts[2])
		return gid, err == nil && gid > 0
	}
	return 0, false
}

// prepareRestrictedLog puts the system log into restricted mode: the group, the marker every writer
// reads, and the directory and files it covers narrowed to root and the group.
func prepareRestrictedLog(cfg endpointconfig.Config) error {
	gid, err := ensureLogGroup()
	if err != nil {
		return err
	}
	dir := filepath.Dir(cfg.LogPath)
	if err := os.MkdirAll(dir, asymptoteobserve.RestrictedRuntimeLogDirMode); err != nil {
		return fmt.Errorf("create the log directory %s: %w", dir, err)
	}
	if err := os.Lchown(dir, 0, gid); err != nil {
		return fmt.Errorf("give the log directory %s to root:%s: %w", dir, asymptoteobserve.RuntimeLogGroup, err)
	}
	if err := os.Chmod(dir, asymptoteobserve.RestrictedRuntimeLogDirMode); err != nil {
		return fmt.Errorf("close the log directory %s: %w", dir, err)
	}
	if err := asymptoteobserve.WriteRestrictedLogMarker(asymptoteobserve.RestrictedLog{
		LogPath: cfg.LogPath,
		Socket:  asymptoteobserve.DefaultRuntimeLogSocketPath,
		Group:   asymptoteobserve.RuntimeLogGroup,
		GID:     gid,
	}); err != nil {
		return fmt.Errorf("record restricted log mode: %w", err)
	}
	// Everything already in the directory was written under the shared 0666 contract: the log, its
	// lock and archives, and whatever else Beacon keeps beside it. Narrow all of it now rather than
	// waiting for each to be rewritten. Only regular files root owns; a link is never followed.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read the log directory %s: %w", dir, err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || !ownedByRoot(info) {
			continue
		}
		if err := os.Lchown(path, 0, gid); err != nil {
			return fmt.Errorf("give %s to root:%s: %w", path, asymptoteobserve.RuntimeLogGroup, err)
		}
		if perm := info.Mode().Perm(); perm&^asymptoteobserve.RestrictedRuntimeLogFileMode != 0 {
			if err := os.Chmod(path, perm&asymptoteobserve.RestrictedRuntimeLogFileMode); err != nil {
				return fmt.Errorf("narrow %s: %w", path, err)
			}
		}
	}
	return nil
}

// releaseRestrictedLog returns a system log to the shared shape: the marker goes, so hooks append
// to the file again, and the log and its lock are reopened to them now rather than at the
// collector's next write. The beacon-log group is left in place, with whatever members an
// administrator gave it.
func releaseRestrictedLog(cfg endpointconfig.Config) error {
	marker, wasRestricted := asymptoteobserve.LoadRestrictedLog()
	if err := asymptoteobserve.RemoveRestrictedLogMarker(); err != nil {
		return fmt.Errorf("leave restricted log mode: %w", err)
	}
	if !wasRestricted || !marker.Covers(cfg.LogPath) {
		return nil
	}
	for _, path := range []string{cfg.LogPath, cfg.LogPath + ".lock"} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || !ownedByRoot(info) {
			continue
		}
		if err := os.Chmod(path, asymptoteobserve.SharedRuntimeLogFileMode); err != nil {
			return fmt.Errorf("reopen %s to hooks: %w", path, err)
		}
	}
	return nil
}

// prepareSystemLog is the system-mode log setup Install runs before anything writes the log.
func prepareSystemLog(cfg endpointconfig.Config) error {
	if cfg.RestrictedLog() {
		return prepareRestrictedLog(cfg)
	}
	if RestrictedLogSupported() {
		if err := releaseRestrictedLog(cfg); err != nil {
			return err
		}
	}
	if err := writer.EnsureSystemLogWritable(cfg.LogPath); err != nil {
		return fmt.Errorf("prepare the log directory: %w", err)
	}
	return nil
}
