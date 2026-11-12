//go:build !linux

package sink

// releasePages does nothing where the kernel offers no way to drop a file's written pages.
func releasePages(any, int64, int64, int64) {}
