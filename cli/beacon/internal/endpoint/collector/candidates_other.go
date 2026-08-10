//go:build !windows

package collector

// binaryFileName is the on-disk name of the collector.
//
// The same as BinaryName here: POSIX executables carry no extension, so the name used for a PATH
// lookup and the name used for a direct stat are the same string.
func binaryFileName() string { return BinaryName }

// packagedBinaryPaths is the machine-wide install location.
//
// /opt rather than an FHS bindir, deliberately: it is the correct home for a self-contained vendor
// bundle, it matches the macOS package layout, and selfupdate's install classification keys off it.
func packagedBinaryPaths() []string { return []string{PackagedBinaryPath} }
