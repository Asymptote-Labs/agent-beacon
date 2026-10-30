package beaconingestextension

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"sync"
	"time"
)

const (
	// nameLookupTimeout bounds one id(1) call. The name is resolved once per connection, inside the
	// server's budget for it, so a directory service that does not answer costs one connection
	// half a second and leaves the event named by uid only.
	nameLookupTimeout = 500 * time.Millisecond
	// nameCacheTTL bounds how long a uid's name is reused. Accounts are renamed rarely; a miss is
	// retried after the same interval, so a directory service that was briefly unreachable does
	// not leave an account unnamed until the collector restarts.
	nameCacheTTL = 10 * time.Minute
)

// nameCache resolves the account name stamped on each event from the peer's uid.
//
// The collector is built with CGO_ENABLED=0, whose os/user reads /etc/passwd and never asks NSS
// or Directory Services, so accounts from LDAP, SSSD, AD or macOS's own directory would come back
// unnamed. `id -nu <uid>` asks the system's own resolver on both Linux and macOS and is the
// fallback.
type nameCache struct {
	mu      sync.Mutex
	entries map[string]nameEntry
	now     func() time.Time
	lookup1 func(uid string) string
}

type nameEntry struct {
	name    string
	expires time.Time
}

func newNameCache() *nameCache {
	return &nameCache{entries: map[string]nameEntry{}, now: time.Now, lookup1: resolveName}
}

func (c *nameCache) lookup(uid string) string {
	now := c.now()
	c.mu.Lock()
	entry, ok := c.entries[uid]
	c.mu.Unlock()
	if ok && now.Before(entry.expires) {
		return entry.name
	}
	name := c.lookup1(uid)
	c.mu.Lock()
	c.entries[uid] = nameEntry{name: name, expires: now.Add(nameCacheTTL)}
	c.mu.Unlock()
	return name
}

// idCommands are the absolute paths tried for id(1). The collector runs as root, so PATH is not
// consulted.
var idCommands = []string{"/usr/bin/id", "/bin/id"}

// firstExecutable is the first of paths that exists as an executable file.
func firstExecutable(paths []string) string {
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return path
		}
	}
	return ""
}

func resolveName(id string) string {
	if u, err := user.LookupId(id); err == nil && validName(u.Username) {
		return u.Username
	}
	// id(1) is tried once: on merged-/usr systems both paths are the same binary, and running it
	// again behind a directory service that did not answer would only double the wait.
	path := firstExecutable(idCommands)
	if path == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), nameLookupTimeout)
	out, err := exec.CommandContext(ctx, path, "-nu", id).Output()
	cancel()
	if err != nil {
		return ""
	}
	if name := strings.TrimSpace(string(out)); validName(name) {
		return name
	}
	return ""
}

// validName accepts what an account name can be and nothing that could disturb a log line or a
// reader: no control characters or whitespace, bounded length.
func validName(name string) bool {
	if name == "" || len(name) > 256 {
		return false
	}
	for _, r := range name {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}
