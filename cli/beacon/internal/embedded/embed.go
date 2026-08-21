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

// BinaryStem is the hook binary's name without any platform extension.
//
// Detection matches on this rather than on GetBinaryName. A command containing
// "beacon-hooks.exe" also contains "beacon-hooks", so the stem recognizes both spellings, while
// the platform-specific name recognizes only one -- which meant a Windows Beacon could not see a
// hook command written without the extension, and would neither repair nor remove it. Settings
// files are synced between machines and written by older builds, so both spellings genuinely occur.
//
// Writing still uses GetBinaryName: what we install must be the name this platform can execute.
const BinaryStem = "beacon-hooks"

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
