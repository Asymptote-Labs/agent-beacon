//go:build windows

package peercred

import (
	"testing"

	"golang.org/x/sys/windows"
)

// TokenUser of this process's own token.
func currentOwner(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String()
}

// The live tables parse: whatever the running system returns fits the row layouts.
func TestLiveTCPTablesParse(t *testing.T) {
	buf, err := extendedTCPTable(windows.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tcpTable4Rows(buf); err != nil {
		t.Fatalf("IPv4 table: %v", err)
	}
	if buf, err := extendedTCPTable(windows.AF_INET6); err == nil {
		if _, err := tcpTable6Rows(buf); err != nil {
			t.Fatalf("IPv6 table: %v", err)
		}
	}
}
