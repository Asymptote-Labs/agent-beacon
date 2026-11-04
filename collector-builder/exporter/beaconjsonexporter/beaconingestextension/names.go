package beaconingestextension

import (
	"sync"
	"time"
)

const (
	// nameLookupTimeout bounds how long a connection waits for its account's name. The name is
	// resolved inside the server's budget for the connection, so a directory service that does not
	// answer costs one connection half a second and leaves the event named by its account id only.
	// The lookup itself carries on, and its answer fills the cache when it comes.
	nameLookupTimeout = 500 * time.Millisecond
	// nameCacheTTL bounds how long a uid's name is reused. Accounts are renamed rarely; a miss is
	// retried after the same interval, so a directory service that was briefly unreachable does
	// not leave an account unnamed until the collector restarts.
	nameCacheTTL = 10 * time.Minute
)

// nameCache resolves the account name stamped on each event from the peer's account: a uid on
// Unix, a SID on Windows (see resolveName for each). At most one lookup per account runs at a time:
// connections that arrive while it runs wait on that one, and one that outlasts the wait still
// fills the cache when it returns, so a slow directory service leaves the account unnamed only
// until it answers rather than for nameCacheTTL.
type nameCache struct {
	mu      sync.Mutex
	entries map[string]nameEntry
	pending map[string]chan struct{}
	now     func() time.Time
	wait    time.Duration
	lookup1 func(uid string) string
}

type nameEntry struct {
	name    string
	expires time.Time
}

func newNameCache() *nameCache {
	return &nameCache{
		entries: map[string]nameEntry{},
		pending: map[string]chan struct{}{},
		now:     time.Now,
		wait:    nameLookupTimeout,
		lookup1: resolveName,
	}
}

func (c *nameCache) lookup(uid string) string {
	c.mu.Lock()
	if entry, ok := c.entries[uid]; ok && c.now().Before(entry.expires) {
		c.mu.Unlock()
		return entry.name
	}
	done, running := c.pending[uid]
	if !running {
		done = make(chan struct{})
		c.pending[uid] = done
		go func() {
			name := c.lookup1(uid)
			c.mu.Lock()
			c.entries[uid] = nameEntry{name: name, expires: c.now().Add(nameCacheTTL)}
			delete(c.pending, uid)
			c.mu.Unlock()
			close(done)
		}()
	}
	c.mu.Unlock()

	timer := time.NewTimer(c.wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[uid].name
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
