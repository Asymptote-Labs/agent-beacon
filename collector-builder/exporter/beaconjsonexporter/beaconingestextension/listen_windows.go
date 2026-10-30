//go:build windows

package beaconingestextension

import "time"

// listenRetries is true on Windows, where any account can create the pipe's name first (see Start).
const listenRetries = true

// listenRetryInterval is how often a collector that could not create its pipe tries again; tests
// shorten it.
var listenRetryInterval = 5 * time.Second
