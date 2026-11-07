//go:build linux

package peercred

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The real thing: the server end of a loopback connection sees the client's address as remote, and
// the socket at that address belongs to this process's account.
func TestUIDNamesTheOwnerOfARealLoopbackConnection(t *testing.T) {
	for _, network := range []string{"tcp4", "tcp6"} {
		t.Run(network, func(t *testing.T) {
			addr := "127.0.0.1:0"
			if network == "tcp6" {
				addr = "[::1]:0"
			}
			ln, err := net.Listen(network, addr)
			if err != nil {
				t.Skipf("no %s loopback: %v", network, err)
			}
			defer ln.Close()
			accepted := make(chan net.Conn, 1)
			go func() {
				c, _ := ln.Accept()
				accepted <- c
			}()
			client, err := net.Dial(network, ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			server := <-accepted
			defer server.Close()

			remote, local := server.RemoteAddr().(*net.TCPAddr), server.LocalAddr().(*net.TCPAddr)
			want := strconv.Itoa(os.Geteuid())
			if uid, err := sockDiagUID(remote, local); err != nil || uid != want {
				if errors.Is(err, errDiagUnavailable) {
					t.Logf("sock_diag unavailable here: %v", err)
				} else {
					t.Fatalf("sock_diag UID(%s -> %s) = %q, %v; want %s", remote, local, uid, err, want)
				}
			}
			if uid, err := procLookup(remote, local); err != nil || uid != want {
				t.Fatalf("/proc UID(%s -> %s) = %q, %v; want %s", remote, local, uid, err, want)
			}
			if uid, err := UID(remote, local); err != nil || uid != want {
				t.Fatalf("UID = %q, %v; want this process's %s", uid, err, want)
			}
			// The same client address paired with another destination is another connection.
			other := &net.TCPAddr{IP: local.IP, Port: local.Port + 1}
			if uid, err := UID(remote, other); err == nil {
				t.Fatalf("UID matched a connection to %s that does not exist: %s", other, uid)
			}
		})
	}
}

// Once the client has closed and the socket is gone or in TIME_WAIT, there is nobody to name.
func TestUIDOfAClosedClientIsUnknown(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	remote, local := server.RemoteAddr().(*net.TCPAddr), server.LocalAddr().(*net.TCPAddr)
	// The server closes first, so the server's end takes TIME_WAIT; then the client's end closes
	// with a reset so it leaves the table entirely.
	_ = server.Close()
	_ = client.(*net.TCPConn).SetLinger(0)
	_ = client.Close()
	if uid, err := UID(remote, local); err == nil {
		t.Fatalf("UID of a closed client = %s, want unknown", uid)
	}
}

func TestUIDRefusesWhatItCannotSee(t *testing.T) {
	collector := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4318}
	if _, err := UID(&net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 5000}, collector); !errors.Is(err, ErrNotLoopback) {
		t.Fatalf("remote address err = %v, want ErrNotLoopback", err)
	}
	for _, addr := range []*net.TCPAddr{nil, {IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("127.0.0.1"), Port: 70000}} {
		if _, err := UID(addr, collector); !errors.Is(err, ErrNotFound) {
			t.Fatalf("UID(%v) err = %v, want ErrNotFound", addr, err)
		}
	}
	if _, err := UID(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5000}, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no server address err = %v, want ErrNotFound", err)
	}
}

// The fallback is used only when sock_diag itself is unavailable, not when it answers "no such
// socket".
func TestLookupFallsBackOnlyWhenSockDiagIsUnavailable(t *testing.T) {
	old := diagLookup
	t.Cleanup(func() { diagLookup = old })
	oldState := diag.Load()
	t.Cleanup(func() { diag.Store(oldState) })
	dir := t.TempDir()
	oldProc := procNet
	procNet = dir
	t.Cleanup(func() { procNet = oldProc })
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	row := "   2: 0100007F:B5D2 0100007F:10E6 01 00000000:00000000 00:00000000 00000000  1001        0 102 1 0 20 4 30 10 -1\n"
	writeTable := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "tcp"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTable(header + row)
	client := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0xB5D2}
	server := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0x10E6}
	if procAddr4(client.IP, client.Port) != "0100007F:B5D2" {
		t.Skip("fixtures are little-endian")
	}
	diagCalls := 0
	answer := func(err error) {
		diagCalls = 0
		diagLookup = func(_, _ *net.TCPAddr) (string, error) { diagCalls++; return "", err }
	}

	diag.Store(diagUnknown)
	answer(errDiagUnavailable)
	if uid, err := lookupUID(client, server); err != nil || uid != "1001" {
		t.Fatalf("with sock_diag unavailable: %q, %v; want the /proc answer", uid, err)
	}

	// Once sock_diag has shown it can see sockets, its ENOENT is final.
	diag.Store(diagSeesSockets)
	answer(ErrNotFound)
	if uid, err := lookupUID(client, server); !errors.Is(err, ErrNotFound) {
		t.Fatalf("with a working sock_diag saying no such socket: %q, %v; want ErrNotFound without a second guess", uid, err)
	}

	// Before that, ENOENT may mean no tcp_diag handler: /proc is asked, and a client /proc can see
	// but sock_diag cannot settles it, so later lookups go straight to /proc.
	diag.Store(diagUnknown)
	answer(ErrNotFound)
	writeTable(header)
	if uid, err := lookupUID(client, server); !errors.Is(err, ErrNotFound) {
		t.Fatalf("with neither finding the socket: %q, %v; want ErrNotFound", uid, err)
	}
	if got := diag.Load(); got != diagUnknown {
		t.Fatalf("state after agreeing ErrNotFound = %d, want still unknown", got)
	}
	writeTable(header + row)
	if uid, err := lookupUID(client, server); err != nil || uid != "1001" {
		t.Fatalf("with sock_diag blind to a socket /proc has: %q, %v; want the /proc answer", uid, err)
	}
	if got := diag.Load(); got != diagBlind {
		t.Fatalf("state = %d, want sock_diag marked blind", got)
	}
	answer(ErrNotFound)
	if uid, err := lookupUID(client, server); err != nil || uid != "1001" || diagCalls != 0 {
		t.Fatalf("after marking sock_diag blind: %q, %v, %d sock_diag calls; want /proc alone", uid, err, diagCalls)
	}
}

// A real answer from sock_diag proves its handler is there, so a later ENOENT is final.
func TestSockDiagAnswerMarksItWorking(t *testing.T) {
	oldState := diag.Load()
	t.Cleanup(func() { diag.Store(oldState) })
	diag.Store(diagUnknown)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err := sockDiagUID(server.RemoteAddr().(*net.TCPAddr), server.LocalAddr().(*net.TCPAddr)); err != nil {
		t.Skipf("sock_diag cannot see this socket here: %v", err)
	}
	if got := diag.Load(); got != diagSeesSockets {
		t.Fatalf("state = %d after a sock_diag answer, want diagSeesSockets", got)
	}
}

// Fixtures in the kernel's own format: what systemd-era kernels print, with the address words in
// host byte order (little-endian here, as on every platform Beacon packages).
func TestScanTableFixtures(t *testing.T) {
	dir := t.TempDir()
	old := procNet
	procNet = dir
	t.Cleanup(func() { procNet = old })
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	line := func(sl int, local, remote, st, uid, inode string) string {
		return fmt.Sprintf("%4d: %s %s %s 00000000:00000000 00:00000000 00000000 %5s        0 %s 1 0000000000000000 20 4 30 10 -1\n", sl, local, remote, st, uid, inode)
	}
	client := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0xB5D2}
	server := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0x10E6}
	write := func(tcp, tcp6 string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "tcp"), []byte(header+tcp), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tcp6"), []byte(header+tcp6), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if procAddr4(client.IP, client.Port) != "0100007F:B5D2" {
		t.Skipf("host byte order is not little-endian (%s); fixtures are written for it", procAddr4(client.IP, client.Port))
	}

	cases := []struct {
		name, tcp, tcp6, want string
		err                   error
	}{
		{
			name: "client and server ends",
			tcp: line(0, "0100007F:10E6", "00000000:0000", "0A", "0", "100") + // the collector listening
				line(1, "0100007F:10E6", "0100007F:B5D2", "01", "0", "101") + // its end of the connection
				line(2, "0100007F:B5D2", "0100007F:10E6", "01", "1001", "102"), // the client's end
			want: "1001",
		},
		{
			name: "lower-case hex still matches",
			tcp:  line(2, "0100007f:b5d2", "0100007f:10e6", "01", "1002", "102"),
			want: "1002",
		},
		{
			name: "time wait reads as gone, never as root",
			tcp:  line(2, "0100007F:B5D2", "0100007F:10E6", "06", "0", "0"),
			err:  ErrNotFound,
		},
		{
			// Linux lets connections to different destinations share a source port.
			name: "another account's socket on the same port to another service is not this client",
			tcp: line(2, "0100007F:B5D2", "0100007F:10E6", "01", "1001", "102") +
				line(3, "0100007F:B5D2", "0100007F:1F90", "01", "1002", "103"),
			want: "1001",
		},
		{
			name: "and when this client has gone, that socket is still not named",
			tcp:  line(3, "0100007F:B5D2", "0100007F:1F90", "01", "1002", "103"),
			err:  ErrNotFound,
		},
		{
			name: "an unrelated time wait on the same port does not stop the lookup",
			tcp: line(1, "0100007F:B5D2", "0100007F:1538", "06", "0", "0") +
				line(2, "0100007F:B5D2", "0100007F:10E6", "01", "1001", "102"),
			want: "1001",
		},
		{
			name: "two owners of the same connection is ambiguous",
			tcp: line(2, "0100007F:B5D2", "0100007F:10E6", "01", "1001", "102") +
				line(3, "0100007F:B5D2", "0100007F:10E6", "01", "1002", "103"),
			err: ErrAmbiguous,
		},
		{
			name: "dual-stack listener sees the client in tcp6",
			tcp6: line(0, "0000000000000000FFFF00000100007F:B5D2", "0000000000000000FFFF00000100007F:10E6", "01", "1003", "104"),
			want: "1003",
		},
		{
			name: "disagreeing tables are ambiguous",
			tcp:  line(2, "0100007F:B5D2", "0100007F:10E6", "01", "1001", "102"),
			tcp6: line(0, "0000000000000000FFFF00000100007F:B5D2", "0000000000000000FFFF00000100007F:10E6", "01", "1003", "104"),
			err:  ErrAmbiguous,
		},
		{
			name: "another port is not this client",
			tcp:  line(2, "0100007F:B5D3", "0100007F:10E6", "01", "1001", "102"),
			err:  ErrNotFound,
		},
		{
			name: "the client end, not the collector's end of the same connection",
			tcp:  line(1, "0100007F:10E6", "0100007F:B5D2", "01", "0", "101"),
			err:  ErrNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			write(tc.tcp, tc.tcp6)
			uid, err := procLookup(client, server)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("UID = %q, %v; want %v", uid, err, tc.err)
				}
				return
			}
			if err != nil || uid != tc.want {
				t.Fatalf("UID = %q, %v; want %s", uid, err, tc.want)
			}
		})
	}
}
