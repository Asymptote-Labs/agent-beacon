//go:build !windows

package sysacl

import "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/atomicfile"

// WriteSecretFile replaces path with data as a 0600 file. POSIX carries the guarantee in the mode.
func WriteSecretFile(path string, data []byte) error {
	return atomicfile.Write(path, data, 0600)
}

// IsSecretFile is false off Windows: POSIX keeps the guarantee in the mode, which a snapshot
// records and a restore puts back itself.
func IsSecretFile(string) bool { return false }

// SecureSecretDir is a no-op off Windows, where a root-owned 0700 directory is already private.
func SecureSecretDir(string) error { return nil }
