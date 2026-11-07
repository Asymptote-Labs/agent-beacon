package loggroup

import (
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
