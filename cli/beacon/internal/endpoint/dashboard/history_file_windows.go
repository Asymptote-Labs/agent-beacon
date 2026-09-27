//go:build windows

package dashboard

import (
	"os"

	"golang.org/x/sys/windows"
)

// openLogFile opens a runtime log file for reading with FILE_SHARE_DELETE. os.Open leaves it out,
// and on Windows a rename fails while any handle lacks it: a catch-up holding a log file open
// would make the writer's rotation fail and the event it was writing would be lost.
func openLogFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// logFileIdentity is the volume serial number and file index, which survive a rename.
func logFileIdentity(f *os.File) (uint64, uint64, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return 0, 0, err
	}
	return uint64(info.VolumeSerialNumber), uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow), nil
}
