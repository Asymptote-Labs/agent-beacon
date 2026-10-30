//go:build !windows

package sysacl

// POSIX carries these guarantees in root ownership and file modes, which the install and the
// packaging scripts already set, so there is nothing to apply and nothing to inspect.

func SecureSystemTree(root string, skip ...string) error { return nil }

func SecureLogDir(dir, base string) error { return nil }

func InspectLogDir(dir, base string) (LogDirState, error) {
	return LogDirState{HooksCanAppend: true}, nil
}

func SecureRestrictedLogDir(dir, base, readersSID string) error { return nil }

func SecureRestrictedLock(path string) error { return nil }

func InspectRestrictedLogDir(dir, base, readersSID string) ([]string, error) { return nil, nil }

// ResetLogDirToShared is a no-op off Windows.
func ResetLogDirToShared(string) error { return nil }

// LooksRestricted is false off Windows.
func LooksRestricted(string, string) bool { return false }

// SecurePrivateDir is a no-op off Windows, where a root-owned 0700 directory is already private.
func SecurePrivateDir(string) error { return nil }

// InspectPrivateFile is a no-op off Windows, where file modes say who may read a file.
func InspectPrivateFile(string) ([]string, error) { return nil, nil }

// IsPrivateFile is false off Windows: file modes carry the answer there, and callers keep the mode
// they read.
func IsPrivateFile(string) bool { return false }
