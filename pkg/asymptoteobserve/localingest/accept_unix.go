//go:build unix

package localingest

import (
	"errors"
	"syscall"
)

// temporaryAcceptError reports an accept failure that passes on its own: the process or system is
// out of descriptors or buffers for a moment, or a client gave up before it was accepted.
func temporaryAcceptError(err error) bool {
	for _, errno := range []syscall.Errno{syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM, syscall.ECONNABORTED, syscall.EINTR} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}
