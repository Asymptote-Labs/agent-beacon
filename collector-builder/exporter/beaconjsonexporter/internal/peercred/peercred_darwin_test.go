//go:build darwin

package peercred

import (
	"errors"
	"net"
	"os"
	"strconv"
	"testing"
)

// so_uid is the effective uid of the credential that created the socket.
func currentOwner(t *testing.T) string {
	t.Helper()
	return strconv.Itoa(os.Geteuid())
}

// The live table parses as a whole: whatever the running kernel writes, the parser reaches the
// closing record without calling it malformed.
func TestLivePCBListParses(t *testing.T) {
	buf, err := readPCBList()
	if err != nil {
		t.Fatalf("read %s: %v", pcbListSysctl, err)
	}
	nowhere := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
	if _, err := pcbListUID(buf, nowhere, nowhere); err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatalf("live table: %v", err)
	}
}
