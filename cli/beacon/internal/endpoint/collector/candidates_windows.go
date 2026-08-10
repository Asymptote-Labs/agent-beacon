//go:build windows

package collector

import (
	"os"
	"path/filepath"
)

// binaryFileName is what the collector is actually called on disk here.
//
// BinaryName has no extension because that is the name callers ask exec.LookPath for, and LookPath
// applies PATHEXT to resolve it. Every other lookup names the file directly, where the extension is
// not optional: os.Stat of "beacon-otelcol" does not find "beacon-otelcol.exe".
//
// That distinction is exactly what the release archive walks into. It ships beacon.exe and
// beacon-otelcol.exe side by side, and the first thing a user does is extract it somewhere that is
// not on PATH and run `beacon endpoint install` -- which searched for an extensionless sibling,
// found nothing, and reported that no collector was installed.
func binaryFileName() string {
	return BinaryName + ".exe"
}

// packagedBinaryPaths are the machine-wide install locations to look in.
//
// The POSIX constant is /opt/beacon/bin/beacon-otelcol, which is not a path on this platform. Read
// from the environment rather than hardcoded, with a fallback, for the same reason SystemBaseDir
// does: a machine with a relocated or localized %ProgramFiles% must not be told its collector is
// missing.
//
// Both program-files roots are checked. A 64-bit Beacon installs under %ProgramFiles%, but an
// installer running from a 32-bit context sees that variable redirected, and %ProgramW6432% is the
// one that always names the 64-bit directory -- so looking in only one of them finds nothing on a
// machine where the other was used.
func packagedBinaryPaths() []string {
	var roots []string
	for _, key := range []string{"ProgramW6432", "ProgramFiles"} {
		if value := os.Getenv(key); value != "" {
			roots = append(roots, value)
		}
	}
	if len(roots) == 0 {
		roots = append(roots, `C:\Program Files`)
	}

	seen := make(map[string]bool, len(roots))
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		path := filepath.Join(root, "Beacon", "bin", binaryFileName())
		if seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}
