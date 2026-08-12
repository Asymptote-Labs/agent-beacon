package osuser

import (
	"context"
	"errors"
	"os/user"
	"strconv"
	"strings"
	"testing"
)

// The parser is the part that runs against output this process did not produce, so it is the part
// worth pinning. A directory service answers in the same seven-field format as /etc/passwd.
func TestParsePasswdLineReadsAnNSSRecord(t *testing.T) {
	got, err := parsePasswdLine("alice:x:10001:10002::/home/alice:/bin/bash\n", "alice")
	if err != nil {
		t.Fatal(err)
	}
	want := Info{Username: "alice", UID: 10001, GID: 10002, HomeDir: "/home/alice"}
	if got != want {
		t.Errorf("parsePasswdLine = %+v, want %+v", got, want)
	}
}

// A record whose uid or home is unusable must not resolve to a half-populated Info: the caller
// would chown to uid 0 or write settings into "", both of which are worse than reporting that the
// user could not be resolved.
func TestParsePasswdLineRejectsUnusableRecords(t *testing.T) {
	for name, out := range map[string]string{
		"too few fields":  "alice:x:10001:10002:/home/alice\n",
		"non-numeric uid": "alice:x:notanumber:10002::/home/alice:/bin/bash\n",
		"non-numeric gid": "alice:x:10001:nope::/home/alice:/bin/bash\n",
		"empty home":      "alice:x:10001:10002:::/bin/bash\n",
		"empty name":      ":x:10001:10002::/home/alice:/bin/bash\n",
		"no output":       "",
		"banner only":     "getent: something went sideways\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := parsePasswdLine(out, "alice"); err == nil {
				t.Errorf("parsePasswdLine(%q) = %+v, want an error", out, got)
			}
		})
	}
}

// A directory service may answer a differently-cased query with the account's canonical name.
// Ownership and re-execution should use what the directory says, not what the caller typed.
func TestParsePasswdLinePrefersTheCanonicalName(t *testing.T) {
	got, err := parsePasswdLine("alice:x:10001:10002::/home/alice:/bin/bash\n", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "alice" {
		t.Errorf("Username = %q, want the record's own spelling %q", got.Username, "alice")
	}
}

// The whole point of the package: an account the local database cannot see, which NSS can. This is
// the Tower Research case, and before this package it resolved to "no such user".
func TestLookupFallsBackToNSS(t *testing.T) {
	restore := runGetent
	t.Cleanup(func() { runGetent = restore })
	runGetent = func(_ context.Context, name string) (string, error) {
		if name != "ldapuser" {
			t.Errorf("getent asked for %q, want %q", name, "ldapuser")
		}
		return "ldapuser:x:20001:20002::/home/ldapuser:/bin/bash\n", nil
	}

	got, err := Lookup("ldapuser")
	if err != nil {
		t.Fatalf("Lookup fell through to an error for an NSS-resolvable account: %v", err)
	}
	if got.UID != 20001 || got.HomeDir != "/home/ldapuser" {
		t.Errorf("Lookup = %+v, want uid 20001 and home /home/ldapuser", got)
	}
}

// A stripped image may not ship getent. Degrading to an error is correct -- the callers already
// handle an unresolvable user -- but the error has to say which of the two things went wrong, or
// the operator cannot tell "no such account" from "this host cannot answer the question".
func TestLookupReportsWhyWhenNSSIsUnavailable(t *testing.T) {
	restore := runGetent
	t.Cleanup(func() { runGetent = restore })
	runGetent = func(context.Context, string) (string, error) {
		return "", errors.New("exec: \"getent\": executable file not found in $PATH")
	}

	_, err := Lookup("ldapuser")
	if err == nil {
		t.Fatal("Lookup succeeded with no local record and no NSS")
	}
	for _, want := range []string{"ldapuser", "NSS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q so the failure is attributable", err, want)
		}
	}
}

// The fast path must still work, and must not consult NSS for an account the local database can
// already answer -- that would put a subprocess on the hot path of every ordinary host.
func TestLookupUsesTheLocalDatabaseFirst(t *testing.T) {
	me, err := user.Current()
	if err != nil || me.Username == "" {
		t.Skip("no resolvable current user on this host")
	}
	restore := runGetent
	t.Cleanup(func() { runGetent = restore })
	runGetent = func(context.Context, string) (string, error) {
		t.Error("NSS was consulted for an account the local database can resolve")
		return "", errors.New("should not be reached")
	}

	got, err := Lookup(me.Username)
	if err != nil {
		t.Fatal(err)
	}
	if strconv.Itoa(got.UID) != me.Uid {
		t.Errorf("UID = %d, want %s", got.UID, me.Uid)
	}
}

func TestLookupRejectsAnEmptyName(t *testing.T) {
	if _, err := Lookup("   "); err == nil {
		t.Error("an empty username must not resolve")
	}
}
