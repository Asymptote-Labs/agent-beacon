//go:build windows

package beaconingestextension

import (
	"errors"
	"io/fs"
	"time"

	"golang.org/x/sys/windows"
)

// retryableListenError reports whether a failed listen is the pipe's name being held by someone
// else, which any account can do on Windows (see Start) and which clears when they let go. Anything
// else -- a socket_path that is not a pipe path, a bad security descriptor -- never clears, so it
// fails the start as before.
func retryableListenError(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PIPE_BUSY) || errors.Is(err, fs.ErrExist)
}

// listenRetryInterval is how often a collector whose pipe name is taken tries again; tests shorten
// it.
var listenRetryInterval = 5 * time.Second
