package asymptoteobserve

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// File and directory modes for the endpoint runtime log, its lock file and its rotated archives.
//
// The three writers of that log -- the beacon CLI, the beacon-hooks adapter and the beaconjson
// collector exporter -- all take their modes from RuntimeLogModes so they cannot disagree. A writer
// that chose differently would undo the others: each one chmods the file it opens, so a hook that
// still believed in 0666 would re-widen a log the CLI had just made private.
const (
	// SharedRuntimeLogFileMode is the machine-wide (system-mode) log's mode. It is deliberately
	// world-writable: the collector runs elevated and creates it, while hooks running as each
	// logged-in user append to it. Narrowing it needs a design change (a group, or hooks sending
	// through the collector) rather than a mode change.
	SharedRuntimeLogFileMode os.FileMode = 0o666
	// SharedRuntimeLogDirMode keeps the shared log reachable by those hooks.
	SharedRuntimeLogDirMode os.FileMode = 0o755
	// PrivateRuntimeLogFileMode is a per-user (user-mode) log's mode. Everything that writes it --
	// the user's own hooks, plugins, CLI and user-level collector -- runs as that user, and the log
	// holds their prompts, command output and diffs, so nobody else has a reason to read or write it.
	PrivateRuntimeLogFileMode os.FileMode = 0o600
	// PrivateRuntimeLogDirMode is the mode of the log directories a writer creates for a per-user log.
	PrivateRuntimeLogDirMode os.FileMode = 0o700
)

// unixSystemLogDir is the system-mode log directory on Linux and macOS. It is never private,
// whatever the process's home directory claims, so a service account whose HOME happens to sit
// above it cannot narrow the shared log out from under every other user's hooks.
const unixSystemLogDir = "/var/log/beacon-agent"

// RuntimeLogModes returns the file and directory modes a writer uses for the runtime log at path,
// and whether that log is private to the current user.
//
// The writers cannot all be told which mode the endpoint is in -- a hook only receives a log path --
// so the decision is made from the path, the same way everywhere: a log inside the current user's
// home directory is that user's own log and is private; anything else (the system log under
// /var/log/beacon-agent or %ProgramData%, or a custom path elsewhere) keeps the shared mode it has
// always had.
//
// One exception keeps root from breaking a user's log: when running as root on Unix inside a home
// directory root does not own (sudo preserving HOME), the log keeps the shared mode, because a
// private root-owned file there is one the user's own hooks could never append to again.
func RuntimeLogModes(path string) (file, dir os.FileMode, private bool) {
	if RuntimeLogIsPrivate(path) {
		return PrivateRuntimeLogFileMode, PrivateRuntimeLogDirMode, true
	}
	return SharedRuntimeLogFileMode, SharedRuntimeLogDirMode, false
}

// RuntimeLogIsPrivate reports whether the runtime log at path is the current user's own log. See
// RuntimeLogModes.
func RuntimeLogIsPrivate(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	if runtime.GOOS != "windows" && pathWithin(abs, unixSystemLogDir) {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return false
	}
	home, err = filepath.Abs(home)
	if err != nil || isFilesystemRoot(home) {
		return false
	}
	within := pathWithin(abs, home)
	if !within {
		// Compare resolved forms too, so a log reached through a symlinked home (macOS /var ->
		// /private/var, or a home on another volume) is still recognized as the user's own.
		resolvedHome, homeErr := filepath.EvalSymlinks(home)
		resolvedDir, dirErr := filepath.EvalSymlinks(filepath.Dir(abs))
		if homeErr == nil && dirErr == nil && !isFilesystemRoot(resolvedHome) {
			within = pathWithin(filepath.Join(resolvedDir, filepath.Base(abs)), resolvedHome)
		}
	}
	if !within {
		return false
	}
	return processOwnsHome(home)
}

// PrivateACLRuntimeLogFileMode is the mode a newly created private log takes when the directory
// it was created in hands it a POSIX ACL (a default ACL, `setfacl -d`). On Linux an ACL's mask is
// the file's group bits, so creating the file 0600 would set the mask to nothing and cancel the
// grant an administrator made on purpose -- the narrow read access a log shipper running as its
// own account is documented to need. Read for the mask keeps that grant working and still gives
// nobody else anything.
const PrivateACLRuntimeLogFileMode os.FileMode = 0o640

// SettleRuntimeLogFileMode sets the mode of a runtime log, its lock or an archive after it was
// opened. logPath is the log the file belongs to, which decides private or shared (see
// RuntimeLogModes); created says whether this open made the file.
//
// A file this open created gets the mode the log calls for, so a umask cannot narrow a shared log
// or widen a private one; an error there is returned.
//
// An existing file is changed only by a process that owns it, and only toward what the log calls
// for. Two cases this protects:
//   - root running in a user's home (sudo keeping HOME) decides "shared" for that user's log, and
//     must not chmod the user's private log back to world-writable;
//   - a private log another account reads through a POSIX ACL keeps that grant: only the "other"
//     bits are removed, never the group bits that carry the ACL mask, so the tightening that runs
//     beside every append does not reset the mask and cut the reader off each time.
//
// Errors on an existing file are ignored: the append it runs beside must not fail over a mode.
//
// The path is never followed: a symlink is left alone, so root cannot be steered into chmodding
// whatever a link in a directory someone else can write points at.
func SettleRuntimeLogFileMode(path, logPath string, created bool) error {
	return settleRuntimeLogFileMode(path, logPath, created, os.Lstat, func(mode os.FileMode) error {
		return os.Chmod(path, mode)
	})
}

// OpenRuntimeLogFile opens path (the runtime log at logPath, or its lock) with the mode logPath
// calls for and settles that mode the way SettleRuntimeLogFileMode does, on the open file rather
// than by path. It is the one way the CLI, the hook adapter and the collector exporter open the log.
//
// Running as root it refuses a symlink in the last component, opens without blocking, and refuses
// anything that is not a regular file or that has another hard link. The shared log's directory is
// root-owned, but a root writer
// must still not be steerable -- by a link or FIFO someone planted where it appends -- into
// appending to, chmodding or hanging on a file of the planter's choosing.
func OpenRuntimeLogFile(path, logPath string, flag int) (*os.File, error) {
	fileMode, _, _ := RuntimeLogModes(logPath)
	existed := true
	if _, err := os.Lstat(path); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		existed = false
	}
	f, err := os.OpenFile(path, flag|elevatedOpenFlags(), fileMode)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("runtime log %s is not a regular file", path)
	}
	if linkedElsewhere(info) {
		_ = f.Close()
		return nil, fmt.Errorf("runtime log %s has another hard link", path)
	}
	if err := settleRuntimeLogFileMode(path, logPath, !existed, func(string) (os.FileInfo, error) { return info, nil }, f.Chmod); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func settleRuntimeLogFileMode(path, logPath string, created bool, stat func(string) (os.FileInfo, error), chmod func(os.FileMode) error) error {
	fileMode, _, private := RuntimeLogModes(logPath)
	if created {
		if private && hasPOSIXACL(path) {
			fileMode = PrivateACLRuntimeLogFileMode
		}
		return chmod(fileMode)
	}
	info, err := stat(path)
	if err != nil || !info.Mode().IsRegular() || !ownedByProcess(info) {
		return nil
	}
	current := info.Mode().Perm()
	want := fileMode
	if private {
		want = current &^ 0o007
		if !hasPOSIXACL(path) {
			want &^= 0o070
		}
	}
	if want != current {
		_ = chmod(want)
	}
	return nil
}

// TightenPrivateRuntimeLogArchives narrows the rotated archives of a private log (path.1 ..
// path.archives) the way SettleRuntimeLogFileMode narrows the live file. Archives rotated before
// logs were private still carry the old shared mode, and they hold the same content as the live
// file. Best effort, like SettleRuntimeLogFileMode on an existing file.
func TightenPrivateRuntimeLogArchives(path string, archives int) {
	if !RuntimeLogIsPrivate(path) {
		return
	}
	for i := 1; i <= archives; i++ {
		archive := fmt.Sprintf("%s.%d", path, i)
		info, err := os.Lstat(archive)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		_ = SettleRuntimeLogFileMode(archive, path, false)
	}
}

func pathWithin(path, dir string) bool {
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
		dir = strings.ToLower(dir)
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func isFilesystemRoot(path string) bool {
	clean := filepath.Clean(path)
	return clean == filepath.Dir(clean)
}
