// Package peercred names the local account that owns the client end of a loopback TCP connection.
//
// The collector's OTLP receivers listen on 127.0.0.1, and every account on the machine exports to
// them. OTLP itself carries no trustworthy identity: a resource attribute is whatever the sender
// wrote. The kernel, though, records which account created each socket, so for a connection that
// is still open the account on its client end is known without asking the client. This is the
// same answer an ident server gives, read locally.
//
// It is only an answer for connections the collector can see from its own network namespace, over
// loopback, while they are open. Anything else (another host, a connection already closed, a
// platform without a lookup) is reported as unknown, never guessed.
//
// Each platform reads the kernel's own record of exactly this connection's client socket:
//
//   - Linux: one NETLINK_SOCK_DIAG lookup of the four-tuple, or /proc/net/tcp{,6} (peercred_linux.go,
//     diag_linux.go). The answer is the socket's uid.
//   - macOS: the net.inet.tcp.pcblist_n table netstat reads, which carries each socket's so_uid
//     (peercred_darwin.go, pcblist.go). The answer is the uid.
//   - Windows: GetExtendedTcpTable's owning PID for the four-tuple, then that process's token user
//     (peercred_windows.go, tcptable.go). The answer is the SID.
//
// The answer is a string: a decimal uid on Unix, a SID on Windows, as the ingest extension's peer
// identity is.
package peercred

import (
	"errors"
	"net"
)

var (
	// ErrUnsupported is returned where the platform offers no lookup this package implements.
	ErrUnsupported = errors.New("peer account lookup is not supported on this platform")
	// ErrNotFound is returned when no open socket matches the address, which includes a client
	// that has already closed its end.
	ErrNotFound = errors.New("no open local socket for this address")
	// ErrAmbiguous is returned when more than one open socket matches with different owners, so no
	// single account can be named.
	ErrAmbiguous = errors.New("more than one local socket with different owners for this address")
	// ErrNotLoopback is returned for an address that is not a loopback one: only a connection made
	// on this machine has its client socket here to read.
	ErrNotLoopback = errors.New("not a loopback address")
)

// UID returns the account that owns the client end of the connection from client to server (the
// remote and local addresses of a connection the collector accepted): a decimal uid on Linux and
// macOS, a SID on Windows.
//
// Both ends are matched, never the client's address alone. Linux (and any stack with SO_REUSEADDR
// or SO_REUSEPORT) lets connections to different destinations share one source port, so 127.0.0.1:P can at the same time be this client and an
// unrelated socket another account opened to some other service; matching only P would then name
// that account, or give up on a stray TIME_WAIT entry, instead of finding this connection.
func UID(client, server *net.TCPAddr) (string, error) {
	if client == nil || client.Port <= 0 || client.Port > 65535 {
		return "", ErrNotFound
	}
	if server == nil || server.Port <= 0 || server.Port > 65535 {
		return "", ErrNotFound
	}
	if !client.IP.IsLoopback() {
		return "", ErrNotLoopback
	}
	return lookupUID(client, server)
}

// mapped is the IPv4-mapped IPv6 form of an IPv4 address, as a dual-stack socket records it.
func mapped(ip4 net.IP) net.IP {
	return append(net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff}, ip4...)
}
