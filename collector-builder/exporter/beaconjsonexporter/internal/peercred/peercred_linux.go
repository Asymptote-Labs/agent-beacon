//go:build linux

package peercred

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// procNet is where the fallback reads the socket tables. A var so tests can point it at fixtures.
var procNet = "/proc/net"

// diagLookup is the kernel's own exact lookup (see diag_linux.go); a var so tests can force the
// fallback.
var diagLookup = sockDiagUID

// lookupUID asks the kernel who owns the client socket of the connection client -> server.
//
// The first answer is NETLINK_SOCK_DIAG, the interface ss uses: given the connection's four-tuple
// the kernel looks that one socket up in its connection hash, so the cost does not grow with the
// number of sockets on the machine, which matters because this runs for every export. Where the
// kernel offers no sock_diag (a module left out, a sandbox that refuses netlink), the socket tables
// in /proc/net are read instead, matching the same four-tuple.
//
// A missing diag handler is not always a refused channel: with tcp_diag absent or unloadable
// (CONFIG_INET_TCP_DIAG=m on a host with kernel.modules_disabled=1, a blacklist, a container host
// whose kernel ships no module) the kernel answers ENOENT, the same answer as for a socket that does
// not exist. Until sock_diag has answered once with a socket, which proves the handler is there, its
// "no such socket" is checked against /proc; if /proc finds the socket, sock_diag is taken to be
// unable to see it and /proc answers from then on.
//
// Either way an answer is refused rather than guessed when it cannot be the sender: a socket in
// TIME_WAIT or otherwise orphaned reports uid 0 and inode 0 whoever created it, which would name
// root for a client that has gone; and no socket at all means the client closed before the lookup,
// which a sender could do on purpose.
func lookupUID(client, server *net.TCPAddr) (string, error) {
	if diag.Load() == diagBlind {
		return procLookup(client, server)
	}
	uid, err := diagLookup(client, server)
	switch {
	case err == nil:
		return uid, nil
	case !errors.Is(err, ErrNotFound):
		return procLookup(client, server)
	case diag.Load() == diagSeesSockets:
		return "", ErrNotFound
	}
	uid, err = procLookup(client, server)
	if err == nil {
		diag.CompareAndSwap(diagUnknown, diagBlind)
	}
	return uid, err
}

// What sock_diag has shown it can do on this kernel. It starts unknown; one reply carrying a socket
// proves the TCP handler is registered (diagSeesSockets), and a socket /proc found where sock_diag
// said ENOENT proves it is not (diagBlind). Neither changes afterwards: a kernel does not drop a
// registered diag handler, and the lookup it costs to re-check would run on every export.
const (
	diagUnknown int32 = iota
	diagSeesSockets
	diagBlind
)

var diag atomic.Int32

// procLookup reads /proc/net/tcp and tcp6, where each socket is one line:
//
//	sl  local_address rem_address   st tx_queue:rx_queue tr:tm->when retrnsmt   uid  timeout inode
//	 0: 0100007F:B5D2 0100007F:10E6 01 00000000:00000000 00:00000000 00000000  1001        0 81234
//
// The client's end of the connection is the line whose local address is the client and whose
// remote address is the collector's listener.
func procLookup(client, server *net.TCPAddr) (string, error) {
	want := map[[2]string]bool{}
	if c4, s4 := client.IP.To4(), server.IP.To4(); c4 != nil && s4 != nil {
		want[[2]string{procAddr4(c4, client.Port), procAddr4(s4, server.Port)}] = true
		// A dual-stack listener sees an IPv4 client as ::ffff:a.b.c.d in tcp6.
		want[[2]string{procAddr6(mapped(c4), client.Port), procAddr6(mapped(s4), server.Port)}] = true
	} else {
		want[[2]string{procAddr6(client.IP.To16(), client.Port), procAddr6(server.IP.To16(), server.Port)}] = true
	}
	found := ""
	for _, table := range []string{"tcp", "tcp6"} {
		f, err := os.Open(filepath.Join(procNet, table))
		if err != nil {
			continue
		}
		uid, err := scanTable(f, want)
		_ = f.Close()
		switch {
		case errors.Is(err, ErrNotFound):
			continue
		case err != nil:
			return "", err
		case found != "" && found != uid:
			return "", ErrAmbiguous
		}
		found = uid
	}
	if found == "" {
		return "", ErrNotFound
	}
	return found, nil
}

func scanTable(r io.Reader, want map[[2]string]bool) (string, error) {
	scanner := bufio.NewScanner(r)
	found := ""
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// sl, local, remote, st, queues, timer, retransmits, uid, timeout, inode
		if len(fields) < 10 || !want[[2]string{strings.ToUpper(fields[1]), strings.ToUpper(fields[2])}] {
			continue
		}
		if fields[9] == "0" {
			// TIME_WAIT and orphans: the uid column is 0 for them whoever made the socket. Not an
			// answer, and not a reason to stop looking either.
			continue
		}
		uid := fields[7]
		if !isDecimal(uid) {
			continue
		}
		if found != "" && found != uid {
			return "", ErrAmbiguous
		}
		found = uid
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if found == "" {
		return "", ErrNotFound
	}
	return found, nil
}

// procAddr4 is how the kernel prints an IPv4 socket address: the address as one 32-bit word in host
// byte order, then the port, both in upper-case hex.
func procAddr4(ip net.IP, port int) string {
	return fmt.Sprintf("%08X:%04X", binary.NativeEndian.Uint32(ip.To4()), port)
}

// procAddr6 is the IPv6 form: four 32-bit words, each in host byte order.
func procAddr6(ip net.IP, port int) string {
	ip = ip.To16()
	var b strings.Builder
	for i := 0; i < 16; i += 4 {
		fmt.Fprintf(&b, "%08X", binary.NativeEndian.Uint32(ip[i:i+4]))
	}
	return fmt.Sprintf("%s:%04X", b.String(), port)
}

func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
