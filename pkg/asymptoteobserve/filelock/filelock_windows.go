//go:build windows

package filelock

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockRange is the byte range every lock covers: the whole theoretically-addressable file.
//
// The whole range rather than one byte, because LockFileEx locks ranges rather than files and two
// locks only conflict where they overlap. A one-byte lock would let a caller that picked a
// different offset proceed concurrently, which is the opposite of what these locks are for. The
// lock files themselves are empty -- they are pure mutexes, never read or written -- so locking
// past the end of the file is both legal and free.
const (
	lockRangeLow  = ^uint32(0)
	lockRangeHigh = ^uint32(0)
)

// lockFile takes a byte-range lock, blocking until it is granted.
//
// Blocking is the default: without LOCKFILE_FAIL_IMMEDIATELY, LockFileEx waits, which matches
// flock's behavior on the other platforms. Getting that backwards would turn contention into a
// dropped event rather than a short wait.
func lockFile(f *os.File, exclusive bool) error {
	var flags uint32
	if exclusive {
		flags = windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	// A zeroed OVERLAPPED means offset 0, which is what the full-range lock above assumes.
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), flags,
		0, lockRangeLow, lockRangeHigh, &overlapped)
}

// tryLockFile takes an exclusive lock without waiting, reporting ERROR_LOCK_VIOLATION when another
// holder has it.
func tryLockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, lockRangeLow, lockRangeHigh, &overlapped)
}

func unlockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()),
		0, lockRangeLow, lockRangeHigh, &overlapped)
}
