//go:build !windows

package sysacl

import "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/atomicfile"

// WriteSecretFile replaces path with data as a 0600 file. POSIX carries the guarantee in the mode.
func WriteSecretFile(path string, data []byte) error {
	return atomicfile.Write(path, data, 0600)
}
