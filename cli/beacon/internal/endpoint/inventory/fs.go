package inventory

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
)

// maxInventoryFileBytes caps one config or skill manifest read. Real ones are a few KiB; the cap
// keeps a planted /dev/zero-sized file from exhausting memory.
const maxInventoryFileBytes = 8 << 20

// The system inventory job runs as root over the console user's home. The cmd layer confines it
// with sessionfs for that run, so every read of a path inside the home refuses symlinks out of it,
// FIFOs, devices and files the user does not own (they read as absent). Paths outside the home
// come in two kinds:
//
//   - fixed system locations (managed settings only an administrator can write), read directly
//     by statPath, readDirPath and readFilePath;
//   - paths the user chose by following a link or naming a file (the skills CLI's lock beside a
//     linked skill), read by readChosenPath through sessionfs.OpenOutside, which admits only a
//     regular file the user could read without root.
//
// Unconfined (user mode), these are the plain os calls.

func statPath(path string) (fs.FileInfo, error) {
	if sessionfs.Confined(path) {
		return sessionfs.Stat(path)
	}
	return os.Stat(path)
}

func readDirPath(path string) ([]fs.DirEntry, error) {
	if sessionfs.Confined(path) {
		return sessionfs.ReadDir(path)
	}
	return os.ReadDir(path)
}

func readFilePath(path string) ([]byte, error) {
	var (
		f   *os.File
		err error
	)
	if sessionfs.Confined(path) {
		f, err = sessionfs.Open(path)
	} else {
		f, err = os.Open(path)
	}
	if err != nil {
		return nil, err
	}
	return readLimited(f, path)
}

// readChosenPath reads a file at a path the console user chose, in or out of the home.
func readChosenPath(path string) ([]byte, error) {
	f, err := sessionfs.OpenOutside(path)
	if err != nil {
		return nil, err
	}
	return readLimited(f, path)
}

// readLimited reads and closes a regular file of at most maxInventoryFileBytes.
func readLimited(f *os.File, path string) ([]byte, error) {
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &fs.PathError{Op: "read", Path: path, Err: errors.New("not a regular file")}
	}
	data, err := io.ReadAll(io.LimitReader(f, maxInventoryFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxInventoryFileBytes {
		return nil, &fs.PathError{Op: "read", Path: path, Err: fmt.Errorf("larger than %d bytes", maxInventoryFileBytes)}
	}
	return data, nil
}
