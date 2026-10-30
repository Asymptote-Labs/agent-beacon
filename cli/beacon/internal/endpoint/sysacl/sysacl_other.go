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
