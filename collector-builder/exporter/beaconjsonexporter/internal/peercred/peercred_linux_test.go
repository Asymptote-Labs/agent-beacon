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

			uid, err := UID(server.RemoteAddr().(*net.TCPAddr))
			if err != nil {
				t.Fatalf("UID(%s) = %v", server.RemoteAddr(), err)
			}
			if want := strconv.Itoa(os.Geteuid()); uid != want {
				t.Fatalf("UID = %s, want this process's %s", uid, want)
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
	remote := server.RemoteAddr().(*net.TCPAddr)
	// The server closes first, so the server's end takes TIME_WAIT; then the client's end closes
	// with a reset so it leaves the table entirely.
	_ = server.Close()
	_ = client.(*net.TCPConn).SetLinger(0)
	_ = client.Close()
	if uid, err := UID(remote); err == nil {
		t.Fatalf("UID of a closed client = %s, want unknown", uid)
	}
}

func TestUIDRefusesWhatItCannotSee(t *testing.T) {
	if _, err := UID(&net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 5000}); !errors.Is(err, ErrNotLoopback) {
		t.Fatalf("remote address err = %v, want ErrNotLoopback", err)
	}
	for _, addr := range []*net.TCPAddr{nil, {IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("127.0.0.1"), Port: 70000}} {
		if _, err := UID(addr); !errors.Is(err, ErrNotFound) {
			t.Fatalf("UID(%v) err = %v, want ErrNotFound", addr, err)
		}
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
			name: "two owners is ambiguous",
			tcp: line(2, "0100007F:B5D2", "0100007F:10E6", "01", "1001", "102") +
				line(3, "0100007F:B5D2", "0100007F:1F90", "01", "1002", "103"),
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			write(tc.tcp, tc.tcp6)
			uid, err := UID(client)
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
