package accountname

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
	c := NewCache()
	c.now = func() time.Time { return now }
	c.lookup1 = func(uid string) string { calls++; return "alice" }

	for i := 0; i < 5; i++ {
		if got := c.Lookup("501"); got != "alice" {
			t.Fatalf("lookup = %q", got)
		}
	}
	if calls != 1 {
		t.Fatalf("resolved %d times within the TTL, want 1", calls)
	}
	now = now.Add(nameCacheTTL + time.Second)
	_ = c.Lookup("501")
	if calls != 2 {
		t.Fatalf("resolved %d times after the TTL, want 2", calls)
	}
}

func TestNameCacheRetriesAMissAfterTheTTL(t *testing.T) {
	now := time.Unix(0, 0)
	answer := ""
	c := NewCache()
	c.now = func() time.Time { return now }
	c.lookup1 = func(string) string { return answer }
	if got := c.Lookup("7"); got != "" {
		t.Fatalf("lookup = %q", got)
	}
	answer = "bob"
	if got := c.Lookup("7"); got != "" {
		t.Fatalf("a miss was retried within the TTL: %q", got)
	}
	now = now.Add(nameCacheTTL + time.Second)
	if got := c.Lookup("7"); got != "bob" {
		t.Fatalf("a miss was not retried after the TTL: %q", got)
	}
}

// A lookup that outlasts the wait leaves that connection unnamed, and its late answer names the
// next one rather than a cached miss for nameCacheTTL. Connections that arrive meanwhile share the
// one lookup.
func TestNameCacheKeepsALateAnswer(t *testing.T) {
	release := make(chan struct{})
	calls := 0
	c := NewCache()
	c.wait = 20 * time.Millisecond
	c.lookup1 = func(string) string { calls++; <-release; return `CONTOSO\late` }
	for i := 0; i < 3; i++ {
		if got := c.Lookup("S-1-5-21-1"); got != "" {
			t.Fatalf("lookup %d = %q before the directory answered", i, got)
		}
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := c.Lookup("S-1-5-21-1"); got == `CONTOSO\late` {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the late answer never reached the cache")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if calls != 1 {
		t.Fatalf("resolved %d times, want one lookup shared by every connection", calls)
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
	if got := Resolve(strconv.Itoa(os.Geteuid())); got != me.Username {
		t.Fatalf("Resolve = %q, want %q", got, me.Username)
	}
}

func TestResolveNameOfAnUnknownUID(t *testing.T) {
	if got := Resolve("4294967000"); got != "" {
		t.Fatalf("Resolve of an unused uid = %q, want empty", got)
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"alice", "j.doe", "DOMAIN\\bob", "user@corp.example", "_svc"} {
		if !ValidName(ok) {
			t.Errorf("ValidName(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "a b", "tab\there", "nl\nx", "esc\x1b[31m", "del\x7f", string(make([]byte, 257))} {
		if ValidName(bad) {
			t.Errorf("ValidName(%q) = true", bad)
		}
	}
}
