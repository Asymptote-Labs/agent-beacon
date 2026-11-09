//go:build !unix && !windows

package localingest

import (
	"errors"
	"net"
	"time"
)

// DefaultSocketPath names no socket here: the restricted log access mode is not available on this
// platform, so writers keep appending to the log as before.
const DefaultSocketPath = ""

// Listener is what Listen would return.
type Listener = net.Listener

// connect always reports no socket here.
func (c Client) connect(time.Duration) (net.Conn, error) { return nil, ErrNoSocket }

// RemoveStale has nothing to remove here.
func RemoveStale(string) error { return nil }

// Listen is unsupported here.
func Listen(string) (Listener, error) { return nil, errors.ErrUnsupported }

// connectingAccount is unsupported here.
func connectingAccount(net.Conn) (string, error) { return "", errors.ErrUnsupported }

// identify is unsupported here.
func identify(net.Conn) (Peer, error) { return Peer{}, errors.ErrUnsupported }

const peerCredentialsSupported = false

func temporaryAcceptError(error) bool { return false }

// processPrivileged is never true here, where there is no ingest channel to bypass.
func processPrivileged() bool { return false }
