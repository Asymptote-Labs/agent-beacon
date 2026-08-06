//go:build !windows

package filelock

import (
	"os"
	"syscall"
)

// lockFile takes a BSD advisory lock, blocking until it is granted.
func lockFile(f *os.File, exclusive bool) error {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	return syscall.Flock(int(f.Fd()), how)
}

// tryLockFile takes an exclusive lock without waiting, reporting EWOULDBLOCK when another holder
// has it.
func tryLockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
