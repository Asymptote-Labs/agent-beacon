//go:build darwin

package localingest

import (
	"fmt"
	"net"
	"strconv"

	"golang.org/x/sys/unix"
)

// peerCredentials asks the kernel who opened the other end of conn. LOCAL_PEERCRED reports the
// effective credentials the peer had when it connected; LOCAL_PEERPID adds its pid, best effort.
func peerCredentials(conn *net.UnixConn) (Peer, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var cred *unix.Xucred
	var pid int
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if credErr == nil {
			pid, _ = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		}
	}); err != nil {
		return Peer{}, err
	}
	if credErr != nil {
		return Peer{}, fmt.Errorf("read peer credentials: %w", credErr)
	}
	peer := Peer{UID: strconv.FormatUint(uint64(cred.Uid), 10), PID: int32(pid)}
	if cred.Ngroups > 0 {
		peer.GID = cred.Groups[0]
	}
	return peer, nil
}

const peerCredentialsSupported = true
