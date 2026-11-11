//go:build !windows

package sessionfs

import (
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// maxTopLinks bounds how many of root's top-level links one walk follows.
const maxTopLinks = 8

// openPublic opens the regular file at path, an absolute and clean path outside the home, if every
// local user could: see walkPublic. The entry is checked before it is opened, since opening a device
// can act on it (opening /dev/watchdog arms the timer), then opened non-blocking, and the opened
// file must be the one checked.
func openPublic(path string) (*os.File, error) {
	dir, name, checked, _, err := walkPublic(path)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dir)
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	opened, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(opened.Dev) != uint64(checked.Dev) || uint64(opened.Ino) != uint64(checked.Ino) {
		_ = f.Close()
		return nil, &fs.PathError{Op: "open", Path: path, Err: fmt.Errorf("replaced while it was opened")}
	}
	return f, nil
}

// resolvePublic is the path walkPublic reached for path: path with root's top-level links resolved.
func resolvePublic(path string) (string, error) {
	dir, _, _, real, err := walkPublic(path)
	if err != nil {
		return "", err
	}
	_ = unix.Close(dir)
	return real, nil
}

// walkPublic looks path up the way any local user could, deciding only from what every one of them
// can see, so its answer never tells the console user what only root may know. From /, one name at
// a time and by descriptor, so nothing can be swapped in behind a check, it
//
//   - looks a name up only in a directory others may enter, and stops with errNotPublic at one they
//     may not, before looking inside it;
//   - follows a symlink only when root owns it and it sits directly in / (macOS's /etc, /tmp and
//     /var, a merged-/usr Linux's /bin and /lib), which no user can plant or change; any other link
//     stops the walk with errNotPublic, without being read;
//   - ends at a regular file others may read, or fails with errNotRegular or errNotPublic.
//
// A name that is not there is fs.ErrNotExist, and one that runs through a file errNotADirectory, as
// they would be for anyone. It returns the directory holding the file, open, the file's name and
// lstat in it, and the path it reached.
func walkPublic(path string) (dir int, name string, st unix.Stat_t, real string, err error) {
	fail := func(err error) (int, string, unix.Stat_t, string, error) {
		return -1, "", unix.Stat_t{}, "", &fs.PathError{Op: "open", Path: path, Err: err}
	}
	if !strings.HasPrefix(path, "/") {
		return fail(errNotPublic)
	}
	pending := names(path)
	dir, err = unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail(err)
	}
	walked, links := []string{}, 0
	for len(pending) > 0 {
		name, pending = pending[0], pending[1:]
		var here unix.Stat_t
		if err := unix.Fstat(dir, &here); err != nil {
			_ = unix.Close(dir)
			return fail(err)
		}
		if here.Mode&unix.S_IXOTH == 0 {
			_ = unix.Close(dir)
			return fail(errNotPublic)
		}
		if name == ".." {
			// A clean path has none; a top-level link's target might, and nothing needs one.
			_ = unix.Close(dir)
			return fail(errNotPublic)
		}
		if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			_ = unix.Close(dir)
			return fail(err)
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFLNK:
			if len(walked) > 0 || st.Uid != 0 || links >= maxTopLinks {
				_ = unix.Close(dir)
				return fail(errNotPublic)
			}
			links++
			target, err := readlinkat(dir, name)
			if err != nil {
				_ = unix.Close(dir)
				return fail(err)
			}
			// Relative or absolute, a link in / resolves from /, where the walk already is.
			pending = append(names(target), pending...)
		case unix.S_IFDIR:
			if len(pending) == 0 {
				_ = unix.Close(dir)
				return fail(errNotRegular)
			}
			next, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			_ = unix.Close(dir)
			if err != nil {
				return fail(err)
			}
			dir = next
			walked = append(walked, name)
		case unix.S_IFREG:
			if len(pending) > 0 {
				_ = unix.Close(dir)
				return fail(errNotADirectory)
			}
			if st.Mode&unix.S_IROTH == 0 {
				_ = unix.Close(dir)
				return fail(errNotPublic)
			}
			return dir, name, st, "/" + strings.Join(append(walked, name), "/"), nil
		default:
			// A FIFO, device or socket, never opened.
			_ = unix.Close(dir)
			return fail(errNotRegular)
		}
	}
	// path is / itself, or a link resolved to it.
	_ = unix.Close(dir)
	return fail(errNotRegular)
}

// names splits a path into the names to look up, without empty names or ".".
func names(path string) []string {
	var out []string
	for _, name := range strings.Split(path, "/") {
		if name != "" && name != "." {
			out = append(out, name)
		}
	}
	return out
}

func readlinkat(dir int, name string) (string, error) {
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(dir, name, buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}
