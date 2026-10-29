//go:build unix

package localingest

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// SocketDirMode is the mode of the directory the socket lives in: root may create entries, nobody
// else may.
const SocketDirMode os.FileMode = 0o755

// SocketMode lets every account connect. Connecting to a Unix socket needs write permission on it;
// what a connection may do is decided by the server, from the peer's kernel credentials.
const SocketMode os.FileMode = 0o666

// checkSocket decides whether a writer may send to path. The socket must be owned by owner, and so
// must its directory, which nobody else may write; every directory above it must belong to root or
// owner and be writable by nobody else unless it is sticky (as /tmp is), so the directory holding
// the socket cannot be swapped for another.
func checkSocket(path string, owner int) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNoSocket
		}
		return fmt.Errorf("%w: %v", ErrUntrustedSocket, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%w: %s is not a socket", ErrUntrustedSocket, path)
	}
	if uid, ok := fileOwner(info); !ok || uid != owner {
		return fmt.Errorf("%w: %s is not owned by uid %d", ErrUntrustedSocket, path, owner)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUntrustedSocket, err)
	}
	for first := true; ; first = false {
		info, err := os.Lstat(dir)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrUntrustedSocket, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: %s is not a directory", ErrUntrustedSocket, dir)
		}
		uid, ok := fileOwner(info)
		if !ok {
			return fmt.Errorf("%w: cannot read the owner of %s", ErrUntrustedSocket, dir)
		}
		writableByOthers := info.Mode().Perm()&0o022 != 0
		if first {
			if uid != owner || writableByOthers {
				return fmt.Errorf("%w: %s must be owned by uid %d and writable by nobody else", ErrUntrustedSocket, dir, owner)
			}
		} else if (uid != 0 && uid != owner) || !ancestorWriteSafe(info) {
			return fmt.Errorf("%w: %s is writable by other accounts", ErrUntrustedSocket, dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

// ancestorWriteSafe reports whether a directory above the socket's own is closed to ordinary
// accounts. Writable by others is safe only with the sticky bit (as /tmp is), which stops them
// renaming or removing what root put there. Writable by its group is safe when that group is a
// system group no person is in: macOS ships /private/var/run, where /var/run points, as
// root:daemon 0775, so refusing every group-writable ancestor would refuse the default socket path
// on every Mac.
func ancestorWriteSafe(info os.FileInfo) bool {
	perm := info.Mode().Perm()
	if perm&0o002 != 0 && info.Mode()&os.ModeSticky == 0 {
		return false
	}
	if perm&0o020 != 0 && info.Mode()&os.ModeSticky == 0 {
		gid, ok := fileGroup(info)
		if !ok || !systemGroups[gid] {
			return false
		}
	}
	return true
}

// systemGroups are the groups trusted to write an ancestor of the socket directory: root's own
// (root on Linux, wheel on macOS) and daemon, which owns /private/var/run on macOS. Neither has
// people in it on a managed machine; admins on macOS are in admin (80), which is not trusted.
var systemGroups = map[int]bool{0: true, 1: true}

func fileGroup(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Gid), true
}

func fileOwner(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}

// RemoveStale removes a socket at path that nothing answers on: one a collector left behind when it
// was killed. It leaves a live socket, and anything that is not a socket, alone.
func RemoveStale(path string) error {
	if path == "" {
		path = DefaultSocketPath
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return nil
	}
	if conn, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
		_ = conn.Close()
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Listen creates the socket at path for a Server to serve, replacing a stale one a previous
// collector left behind, and makes it connectable by every account. The directory is created if it
// is missing and must end up owned by this process and writable by nobody else; a non-socket at
// path, or a socket another server still answers on, is refused rather than removed.
func Listen(path string) (*net.UnixListener, error) {
	if path == "" {
		path = DefaultSocketPath
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, SocketDirMode); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	if uid, ok := fileOwner(info); !ok || uid != os.Geteuid() {
		return nil, fmt.Errorf("%s is not owned by this process (uid %d)", dir, os.Geteuid())
	}
	// MkdirAll's mode is masked by the umask, and an existing directory keeps whatever it had.
	if err := os.Chmod(dir, SocketDirMode); err != nil {
		return nil, fmt.Errorf("set the mode of %s: %w", dir, err)
	}
	if existing, err := os.Lstat(path); err == nil {
		if existing.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if conn, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("another collector is already listening on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove the stale socket %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	l.SetUnlinkOnClose(true)
	if err := os.Chmod(path, SocketMode); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("set the mode of %s: %w", path, err)
	}
	return l, nil
}
