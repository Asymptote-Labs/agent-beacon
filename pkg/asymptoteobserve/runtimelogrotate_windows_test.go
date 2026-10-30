//go:build windows

package asymptoteobserve

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

// holdWithoutDeleteSharing opens path the way PowerShell's Get-Content and [IO.File]::OpenRead do:
// sharing reads and writes but not deletes, which is what makes Windows refuse a rename.
func holdWithoutDeleteSharing(t *testing.T, path string) windows.Handle {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// The real thing the seam stands in for: an archive held open by another reader defers the
// rotation with every file where it was, and the rotation goes through once the reader lets go.
func TestRotateRuntimeLogWaitsForAReaderWithoutDeleteSharing(t *testing.T) {
	for _, held := range []string{"", ".2"} {
		t.Run("held"+held, func(t *testing.T) {
			path := rotationFixture(t, 5)
			before := rotationState(t, path, 5)
			h := holdWithoutDeleteSharing(t, path+held)
			err := RotateRuntimeLog(path, 4, 5, 1)
			if !errors.Is(err, ErrRuntimeLogRotationDeferred) {
				_ = windows.CloseHandle(h)
				t.Fatalf("RotateRuntimeLog = %v, want ErrRuntimeLogRotationDeferred", err)
			}
			assertRotationState(t, rotationState(t, path, 5), before)

			if err := windows.CloseHandle(h); err != nil {
				t.Fatal(err)
			}
			if err := RotateRuntimeLog(path, 4, 5, 1); err != nil {
				t.Fatal(err)
			}
			assertRotationState(t, rotationState(t, path, 5), map[string]string{
				"1": "live", "2": "1", "3": "2", "4": "3", "5": "4",
			})
		})
	}
}

// A reader that does share deletes, as Vector and Beacon's own dashboard do, never stands in the way.
func TestRotateRuntimeLogIgnoresAReaderThatSharesDeletes(t *testing.T) {
	path := rotationFixture(t, 1)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if err := RotateRuntimeLog(path, 4, 5, 1); err != nil {
		t.Fatal(err)
	}
	assertRotationState(t, rotationState(t, path, 5), map[string]string{"1": "live", "2": "1"})
}
