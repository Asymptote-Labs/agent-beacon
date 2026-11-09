// Package store is the state the pipeline shares across calls. Everything a call needs to know
// about earlier calls goes through a Store: which content a session has already reported, and
// whatever analyzers keep about a session. Nothing else keeps per-session state, so any replica
// can serve any call and the in-memory store can be swapped for a shared one.
//
// The interface is batch-first so a networked implementation costs one round trip per operation
// a call makes, however many keys it touches: Get is a multi-key read, Set a batch of writes with
// expiry, Add a batch of set-if-absent writes with expiry.
package store

import (
	"context"
	"time"
)

// Store is a key-value store with expiry. Set and Add copy what they keep: callers may share and
// reuse entry values.
type Store interface {
	// Get returns each key's value, nil where the key is absent or expired.
	Get(ctx context.Context, keys ...string) ([][]byte, error)
	// Set stores every entry, replacing any existing value.
	Set(ctx context.Context, entries ...Entry) error
	// Add stores each entry whose key is absent and reports, per entry, whether it was stored.
	Add(ctx context.Context, entries ...Entry) ([]bool, error)
}

// Entry is one key to write. TTL must be positive: every key the pipeline writes describes a
// session, and sessions end.
type Entry struct {
	Key   string
	Value []byte
	TTL   time.Duration
}
