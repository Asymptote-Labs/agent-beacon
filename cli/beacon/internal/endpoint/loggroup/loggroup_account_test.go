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
	for _, account := range []string{"", "-alice", "a b", "a/b", "a;b", "a,b", "a\nb", "naïve", "$(id)", "`id`", strings.Repeat("x", 257)} {
		if err := validAccount(account); err == nil {
			t.Errorf("validAccount(%q) accepted", account)
		}
	}
	// A DOMAIN\name account exists only on Windows, with exactly one separator between two names.
	if got := validAccount(`CORP\alice`) == nil; got != accountDomainSeparator {
		t.Errorf("validAccount(CORP\\alice) accepted = %v, want %v", got, accountDomainSeparator)
	}
	for _, account := range []string{`\\alice`, `CORP\`, `A\B\c`} {
		if err := validAccount(account); err == nil {
			t.Errorf("validAccount(%q) accepted", account)
		}
	}
}
