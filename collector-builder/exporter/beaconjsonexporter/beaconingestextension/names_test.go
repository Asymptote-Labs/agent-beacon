package beaconingestextension

import (
	"os"
	"os/user"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestNameCacheReusesAndExpires(t *testing.T) {
	now := time.Unix(0, 0)
	calls := 0
	c := newNameCache()
	c.now = func() time.Time { return now }
	c.lookup1 = func(uid string) string { calls++; return "alice" }

	for i := 0; i < 5; i++ {
		if got := c.lookup("501"); got != "alice" {
			t.Fatalf("lookup = %q", got)
		}
	}
	if calls != 1 {
		t.Fatalf("resolved %d times within the TTL, want 1", calls)
	}
	now = now.Add(nameCacheTTL + time.Second)
	_ = c.lookup("501")
	if calls != 2 {
		t.Fatalf("resolved %d times after the TTL, want 2", calls)
	}
}

func TestNameCacheRetriesAMissAfterTheTTL(t *testing.T) {
	now := time.Unix(0, 0)
	answer := ""
	c := newNameCache()
	c.now = func() time.Time { return now }
	c.lookup1 = func(string) string { return answer }
	if got := c.lookup("7"); got != "" {
		t.Fatalf("lookup = %q", got)
	}
	answer = "bob"
	if got := c.lookup("7"); got != "" {
		t.Fatalf("a miss was retried within the TTL: %q", got)
	}
	now = now.Add(nameCacheTTL + time.Second)
	if got := c.lookup("7"); got != "bob" {
		t.Fatalf("a miss was not retried after the TTL: %q", got)
	}
}

func TestResolveNameFindsTheCurrentAccount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uids are a Unix concept")
	}
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	if got := resolveName(strconv.Itoa(os.Geteuid())); got != me.Username {
		t.Fatalf("resolveName = %q, want %q", got, me.Username)
	}
}

func TestResolveNameOfAnUnknownUID(t *testing.T) {
	if got := resolveName("4294967000"); got != "" {
		t.Fatalf("resolveName of an unused uid = %q, want empty", got)
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"alice", "j.doe", "DOMAIN\\bob", "user@corp.example", "_svc"} {
		if !validName(ok) {
			t.Errorf("validName(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "a b", "tab\there", "nl\nx", "esc\x1b[31m", "del\x7f", string(make([]byte, 257))} {
		if validName(bad) {
			t.Errorf("validName(%q) = true", bad)
		}
	}
}

func TestFirstExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no execute bits")
	}
	dir := t.TempDir()
	plain := dir + "/plain"
	exe := dir + "/exe"
	_ = os.WriteFile(plain, nil, 0o644)
	_ = os.WriteFile(exe, nil, 0o755)
	_ = os.Chmod(exe, 0o755)
	if got := firstExecutable([]string{dir + "/missing", plain, exe}); got != exe {
		t.Fatalf("firstExecutable = %q, want %q", got, exe)
	}
	if got := firstExecutable([]string{dir + "/missing"}); got != "" {
		t.Fatalf("firstExecutable = %q, want none", got)
	}
}
