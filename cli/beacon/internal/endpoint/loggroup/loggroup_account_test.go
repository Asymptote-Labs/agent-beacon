package loggroup

import (
	"errors"
	"strings"
	"testing"
)

func TestValidAccount(t *testing.T) {
	for _, account := range []string{"alice", "alice.smith", "svc_splunk", "build-01", "MACHINE$", "alice@corp.example"} {
		if err := validAccount(account); err != nil {
			t.Errorf("validAccount(%q) = %v, want accepted", account, err)
		}
	}
	for _, account := range []string{"", "-alice", " alice", "alice ", "a/b", "/add", "a;b", "a,b", "a\nb", "a\tb", "a\x00b", "$(id)", "`id`", `a"b`, "a\u202eb", strings.Repeat("x", 257)} {
		if err := validAccount(account); err == nil {
			t.Errorf("validAccount(%q) accepted", account)
		}
	}
	// A DOMAIN\name account exists only on Windows, with exactly one separator between two names.
	if got := validAccount(`CORP\alice`) == nil; got != accountWindowsNames {
		t.Errorf("validAccount(CORP\\alice) accepted = %v, want %v", got, accountWindowsNames)
	}
	// Windows makes a local account's name from what the person typed at setup, so it accepts spaces,
	// letters from any script and an apostrophe; Linux and macOS names never hold them.
	for _, account := range []string{"John Smith", "José", "naïve", "O'Brien", "Zoë Müller-Lee", "山田", `DESKTOP-AB12\John Smith`, `PC\José`} {
		if got := validAccount(account) == nil; got != accountWindowsNames {
			t.Errorf("validAccount(%q) accepted = %v, want %v", account, got, accountWindowsNames)
		}
	}
	for _, account := range []string{`\\alice`, `CORP\`, `A\B\c`, `CORP \alice`, `CORP\ alice`} {
		if err := validAccount(account); err == nil {
			t.Errorf("validAccount(%q) accepted", account)
		}
	}
}

// net.exe's error number is read only from what it printed, never from the command line or the
// account name, so `svc1378` failing with another error is never taken for "already a member".
func TestNetSystemError(t *testing.T) {
	failure := func(stderr string) error {
		return &toolError{command: `net.exe localgroup beacon-log svc1378 /add`, err: errors.New("exit status 2"), Stderr: stderr}
	}
	cases := []struct {
		err     error
		account string
		want    int
	}{
		{failure("System error 1378 has occurred.\r\n\r\nThe specified account name is already a member of the group."), "alice", 1378},
		{failure("Systemfehler 1378 aufgetreten."), "alice", 1378},
		{failure("System error 1332 has occurred."), "svc1378", 1332},
		{failure("There is no such global user or group: svc1378.\r\n\r\nMore help is available by typing NET HELPMSG 3783."), `CORP\svc1378`, 3783},
		{failure("The account app1378 could not be added."), "app1378", 0},
		{failure("System error 5 has occurred."), "alice", 5},
		{failure("error 113780"), "alice", 0},
		{failure(""), "svc1378", 0},
		{errors.New("net.exe localgroup beacon-log svc1378 /add: 1378"), "svc1378", 0},
	}
	for _, c := range cases {
		if got := netSystemError(c.err, c.account); got != c.want {
			t.Errorf("netSystemError(%q, %q) = %d, want %d", c.err, c.account, got, c.want)
		}
	}
}
