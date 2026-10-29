//go:build !unix

package localingest

import (
	"errors"
	"net"
)

// checkSocket always reports no socket off Unix: the restricted log access mode is not available
// there, so writers keep appending to the log as before.
func checkSocket(string, int) error { return ErrNoSocket }

// RemoveStale has nothing to remove off Unix.
func RemoveStale(string) error { return nil }

// Listen is unsupported off Unix.
func Listen(string) (*net.UnixListener, error) { return nil, errors.ErrUnsupported }
