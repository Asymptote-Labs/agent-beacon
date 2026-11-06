//go:build linux

package peercred

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// procNet is where the socket tables are read. A var so tests can point it at fixtures.
var procNet = "/proc/net"

// lookupUID finds the client socket in the kernel's TCP tables.
//
// /proc/net/tcp and tcp6 list every socket in this network namespace, one per line:
//
//	sl  local_address rem_address   st tx_queue:rx_queue tr:tm->when retrnsmt   uid  timeout inode
//	 0: 0100007F:B5D2 0100007F:10E6 01 00000000:00000000 00:00000000 00000000  1001        0 81234
//
// The client end of a loopback connection is the line whose local address is the address the
// collector saw as remote, and its uid column is the account that created that socket (sk_uid,
// fixed at creation). The table is world-readable, so this needs no privilege beyond the
// collector's own, and it reads the namespace the collector listens in, which is the one a
// loopback client must share.
//
// Three cases are refused rather than guessed. A line with inode 0 is a socket in TIME_WAIT or
// otherwise orphaned: the kernel prints uid 0 for those, which would name root for a client that
// has gone. Two matching lines with different uids cannot both be the client. And no match at all
// means the client closed before the lookup, which a sender could do deliberately; the event is
// then left unattributed, never attributed to whoever else is connected.
func lookupUID(client *net.TCPAddr) (string, error) {
	want := map[string]bool{}
	if ip4 := client.IP.To4(); ip4 != nil {
		want[procAddr4(ip4, client.Port)] = true
		// A dual-stack listener sees an IPv4 client as ::ffff:a.b.c.d in tcp6.
		want[procAddr6(net.IP(append(net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff}, ip4...)), client.Port)] = true
	} else {
		want[procAddr6(client.IP.To16(), client.Port)] = true
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
		case err == ErrNotFound:
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

func scanTable(r io.Reader, want map[string]bool) (string, error) {
	scanner := bufio.NewScanner(r)
	found := ""
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// sl, local, remote, st, queues, timer, retransmits, uid, timeout, inode
		if len(fields) < 10 || !want[strings.ToUpper(fields[1])] {
			continue
		}
		if fields[9] == "0" {
			// TIME_WAIT and orphans: the uid column is 0 for them whoever made the socket.
			return "", ErrNotFound
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
