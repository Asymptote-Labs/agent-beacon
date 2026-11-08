//go:build darwin || windows

package peercred

import (
	"net"
	"testing"
)

// The real thing on macOS and Windows, as peercred_linux_test.go does on Linux: the server end of a
// loopback connection sees the client's address as remote, and the socket at that address belongs
// to this process's account. currentOwner is per platform.
func TestUIDNamesTheOwnerOfARealLoopbackConnection(t *testing.T) {
	want := currentOwner(t)
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
			if uid, err := UID(remote, local); err != nil || uid != want {
				t.Fatalf("UID(%s -> %s) = %q, %v; want this process's %s", remote, local, uid, err, want)
			}
			// The same client address paired with another destination is another connection.
			other := &net.TCPAddr{IP: local.IP, Port: local.Port + 1}
			if uid, err := UID(remote, other); err == nil {
				t.Fatalf("UID matched a connection to %s that does not exist: %s", other, uid)
			}
		})
	}
}

// Once the client has closed and its socket is gone (or left in TIME_WAIT), there is nobody to name.
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
	defer server.Close()
	remote, local := server.RemoteAddr().(*net.TCPAddr), server.LocalAddr().(*net.TCPAddr)
	if uid, err := UID(remote, local); err != nil {
		t.Fatalf("UID while open = %q, %v", uid, err)
	}
	// Reset the client so its end leaves the table rather than lingering in FIN_WAIT.
	_ = client.(*net.TCPConn).SetLinger(0)
	_ = client.Close()
	if uid, err := UID(remote, local); err == nil {
		t.Fatalf("UID of a closed client = %s, want unknown", uid)
	}
}
