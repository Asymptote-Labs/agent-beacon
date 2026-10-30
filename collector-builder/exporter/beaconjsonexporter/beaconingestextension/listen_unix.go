//go:build !windows

package beaconingestextension

import "time"

// listenRetries is false on Unix: the socket's directory belongs to root, so a failed listen is a
// real conflict to surface (see Start), not a name someone else took first.
const listenRetries = false

var listenRetryInterval = 5 * time.Second
