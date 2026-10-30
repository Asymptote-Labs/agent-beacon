package beaconingestextension

import (
	"sync"
	"time"
)

const (
	// nameLookupTimeout bounds one id(1) call on Unix. The name is resolved once per connection,
	// inside the server's budget for it, so a directory service that does not answer costs one
	// connection half a second and leaves the event named by its account id only.
	nameLookupTimeout = 500 * time.Millisecond
	// nameCacheTTL bounds how long a uid's name is reused. Accounts are renamed rarely; a miss is
	// retried after the same interval, so a directory service that was briefly unreachable does
	// not leave an account unnamed until the collector restarts.
	nameCacheTTL = 10 * time.Minute
)

// nameCache resolves the account name stamped on each event from the peer's account: a uid on
// Unix, a SID on Windows (see resolveName for each).
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
