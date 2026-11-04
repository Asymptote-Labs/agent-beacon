//go:build windows

package asymptoteobserve

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// runtimeLogRenamable reports whether path could be renamed or removed now, by opening it for DELETE
// while sharing everything. Windows refuses a rename or delete of a file another process holds open
// without FILE_SHARE_DELETE, and it refuses a DELETE open by the same sharing check, so this gives
// the rename's own answer without moving anything. A missing file has nothing to refuse.
//
// The link itself is opened, not its target, because that is what a rename moves.
func runtimeLogRenamable(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil
		}
		return &os.PathError{Op: "open for rename", Path: path, Err: err}
	}
	return windows.CloseHandle(h)
}
