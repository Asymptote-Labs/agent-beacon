//go:build windows

package service

import (
	"errors"
	"os"
)

// volumeID is only consulted for launchd, which does not exist on Windows.
func volumeID(string) (uint64, error) {
	return 0, errors.New("volume lookup is not implemented on Windows")
}

// fileOwner has no uid to report on Windows.
func fileOwner(os.FileInfo) (int, bool) { return 0, false }
