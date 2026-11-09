//go:build windows

package accountname

import (
	"os/user"
	"testing"

	"golang.org/x/sys/windows"
)

func currentSID(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid.String()
}

func TestResolveNameNamesTheCurrentAccount(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	if got := Resolve(currentSID(t)); got != me.Username {
		t.Fatalf("resolveName = %q, want %q", got, me.Username)
	}
	if got := Resolve("S-1-5-21-1-2-3-999999"); got != "" {
		t.Fatalf("resolveName of an unknown SID = %q", got)
	}
}

// Accounts that are not SidTypeUser are named too. os/user refused them, which would have left
// events from SYSTEM, a service's virtual account or an Entra ID account without a name.
func TestResolveNameNamesAccountsThatAreNotLocalUsers(t *testing.T) {
	if got := Resolve("S-1-5-18"); got != `NT AUTHORITY\SYSTEM` {
		t.Fatalf("Resolve(SYSTEM) = %q", got)
	}
	if got := Resolve("not a sid"); got != "" {
		t.Fatalf("resolveName of a malformed SID = %q", got)
	}
}

func TestValidWindowsName(t *testing.T) {
	for _, ok := range []string{`CONTOSO\Jane Doe`, `DESKTOP-1\runneradmin`, `NT AUTHORITY\SYSTEM`} {
		if !validWindowsName(ok) {
			t.Errorf("validWindowsName(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", " lead", "trail ", "nl\nx", "esc\x1b[31m", "del\x7f", string(make([]byte, 257))} {
		if validWindowsName(bad) {
			t.Errorf("validWindowsName(%q) = true", bad)
		}
	}
}
