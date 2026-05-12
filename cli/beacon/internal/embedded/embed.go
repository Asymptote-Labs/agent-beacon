package embedded

import (
	_ "embed"
	"runtime"
)

// HooksBinary is the compressed hooks binary for the current platform.
// This file is replaced at build time with the platform-specific binary.
//
//go:embed hooks.bin
var HooksBinary []byte

// GetBinaryName returns the appropriate binary name for the current platform
func GetBinaryName() string {
	if runtime.GOOS == "windows" {
		return "beacon-hooks.exe"
	}
	return "beacon-hooks"
}

// HasEmbeddedBinary returns true if a real binary is embedded (not just placeholder)
func HasEmbeddedBinary() bool {
	// Placeholder file contains "PLACEHOLDER" text
	// Real binary will be much larger and start with ELF/Mach-O/PE headers
	return len(HooksBinary) > 100
}
