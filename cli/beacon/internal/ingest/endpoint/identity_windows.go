//go:build windows

package endpoint

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// fileIdentity returns a stable identifier for the file behind an open handle.
//
// See the POSIX implementation for why identity rather than path: it is what lets a saved byte
// offset follow a log's content across the rename that rotation performs.
//
// The Windows equivalent of device-and-inode is the volume serial number plus the 64-bit file
// index, and unlike POSIX it is not reachable from a FileInfo -- os.Stat's Win32FileAttributeData
// carries no index at all, which is why this takes the open handle. The handle is the one the
// caller already opened, so no second open is needed and there is no chance of blocking the
// collector that is writing the file.
//
// An empty result means "unknown", which callers already handle by falling back to path-based
// offsets. That fallback is why this must not be left unimplemented on Windows: it would work,
// quietly, while re-reading or skipping events on every rotation.
func fileIdentity(f *os.File, _ os.FileInfo) string {
	if f == nil {
		return ""
	}
	var d windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &d); err != nil {
		return ""
	}
	index := uint64(d.FileIndexHigh)<<32 | uint64(d.FileIndexLow)
	return fmt.Sprintf("%d:%d", d.VolumeSerialNumber, index)
}
