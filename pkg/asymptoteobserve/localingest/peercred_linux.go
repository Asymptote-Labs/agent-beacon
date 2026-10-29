//go:build linux

package localingest

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// peerCredentials asks the kernel who opened the other end of conn. SO_PEERCRED reports the
// credentials the peer had when it called connect, which is what matters: a process cannot change
// them afterwards by dropping or gaining privilege.
func peerCredentials(conn *net.UnixConn) (Peer, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	if credErr != nil {
		return Peer{}, fmt.Errorf("read peer credentials: %w", credErr)
	}
	return Peer{UID: cred.Uid, GID: cred.Gid, PID: cred.Pid}, nil
}

const peerCredentialsSupported = true
