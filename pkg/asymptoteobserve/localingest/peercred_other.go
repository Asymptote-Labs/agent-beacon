//go:build unix && !linux && !darwin

package localingest

import (
	"errors"
	"net"
)

// peerCredentials is unsupported here. The server refuses to start rather than accept events it
// cannot attribute, because attributing them is the point of the channel.
func peerCredentials(*net.UnixConn) (Peer, error) {
	return Peer{}, errors.ErrUnsupported
}

const peerCredentialsSupported = false
