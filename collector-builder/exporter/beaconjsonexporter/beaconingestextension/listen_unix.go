//go:build !windows

package beaconingestextension

import "time"

// retryableListenError is always false on Unix: the socket's directory belongs to root, so a failed
// listen is a real conflict to surface (see Start), not a name someone else took first.
func retryableListenError(error) bool { return false }

var listenRetryInterval = 5 * time.Second
