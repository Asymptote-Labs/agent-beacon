//go:build darwin

package peercred

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// pcbListSysctl is the TCP socket table netstat(1) reads. It covers IPv4 and IPv6 pcbs alike (xnu
// keeps one tcbinfo for both), carries the owning uid of each socket (so_uid, from the credential
// that created it), and is readable without privileges, so it serves the root collector and a
// user-mode one the same way. It needs no cgo, which the collector is built without.
const pcbListSysctl = "net.inet.tcp.pcblist_n"

// readPCBList is the sysctl read; a var so tests could stand in for it.
var readPCBList = func() ([]byte, error) {
	var err error
	// The kernel sizes the first call with room for an eighth more pcbs; a table that grew past
	// that between the two calls answers ENOMEM, and asking again sizes it afresh.
	for attempt := 0; attempt < 3; attempt++ {
		var buf []byte
		buf, err = unix.SysctlRaw(pcbListSysctl)
		if err == nil {
			return buf, nil
		}
		if !errors.Is(err, unix.ENOMEM) {
			break
		}
	}
	return nil, err
}

// lookupUID reads the whole TCP table and picks out exactly the connection client -> server.
//
// macOS has no exact lookup by four-tuple like Linux's sock_diag, so this costs one table read per
// export, proportional to the number of TCP sockets on the machine. The table is a snapshot taken
// under the kernel's pcb lock, and the connection this export arrived on is open while the
// processor runs, so it is in it.
func lookupUID(client, server *net.TCPAddr) (string, error) {
	buf, err := readPCBList()
	if err != nil {
		return "", fmt.Errorf("read %s: %w", pcbListSysctl, err)
	}
	return pcbListUID(buf, client, server)
}
