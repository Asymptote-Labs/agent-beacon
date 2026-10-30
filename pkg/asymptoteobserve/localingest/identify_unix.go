//go:build unix

package localingest

import (
	"errors"
	"net"
)

// connectingAccount is the account that opened conn, before anything is read from it. On Unix the
// kernel reports it at any time, so it is identify's own answer.
func connectingAccount(conn net.Conn) (string, error) {
	peer, err := identify(conn)
	return peer.UID, err
}

// identify asks the kernel who opened the other end of conn.
func identify(conn net.Conn) (Peer, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return Peer{}, errors.New("not a Unix socket")
	}
	return peerCredentials(unixConn)
}
