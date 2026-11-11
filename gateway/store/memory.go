package store

import (
	"context"
	"fmt"
	"hash/maphash"
	"sync"
	"time"
)

const shards = 64

// Memory is a Store held in process memory: the default, for a single replica or for replicas
// that tolerate each other reporting the same content once more. It holds at most maxEntries
// keys; past that it drops expired keys first, then arbitrary ones, so it behaves as a bounded
// cache rather than growing with the number of sessions it has ever seen.
type Memory struct {
	seed   maphash.Seed
	limit  int // per shard
	now    func() time.Time
	shards [shards]shard
}

type shard struct {
	mu sync.Mutex
	m  map[string]item
}

type item struct {
	value   []byte
	expires int64 // unix nanoseconds
}

// NewMemory returns a Memory holding at most maxEntries keys.
func NewMemory(maxEntries int) *Memory {
	m := &Memory{seed: maphash.MakeSeed(), limit: max(1, maxEntries/shards), now: time.Now}
	for i := range m.shards {
		m.shards[i].m = make(map[string]item)
	}
	return m
}

func (m *Memory) shard(key string) *shard {
	return &m.shards[maphash.String(m.seed, key)%shards]
}

// Get implements Store.
func (m *Memory) Get(_ context.Context, keys ...string) ([][]byte, error) {
	now := m.now().UnixNano()
	out := make([][]byte, len(keys))
	for i, k := range keys {
		s := m.shard(k)
		s.mu.Lock()
		if it, ok := s.m[k]; ok {
			if it.expires > now {
				out[i] = append([]byte(nil), it.value...)
			} else {
				delete(s.m, k)
			}
		}
		s.mu.Unlock()
	}
	return out, nil
}

// Set implements Store.
func (m *Memory) Set(_ context.Context, entries ...Entry) error {
	now := m.now().UnixNano()
	for _, e := range entries {
		if e.TTL <= 0 {
			return fmt.Errorf("store: key %q: TTL must be positive", e.Key)
		}
		s := m.shard(e.Key)
		s.mu.Lock()
		s.put(e, now, m.limit)
		s.mu.Unlock()
	}
	return nil
}

// Add implements Store.
func (m *Memory) Add(_ context.Context, entries ...Entry) ([]bool, error) {
	now := m.now().UnixNano()
	added := make([]bool, len(entries))
	for i, e := range entries {
		if e.TTL <= 0 {
			return nil, fmt.Errorf("store: key %q: TTL must be positive", e.Key)
		}
		s := m.shard(e.Key)
		s.mu.Lock()
		if it, ok := s.m[e.Key]; !ok || it.expires <= now {
			s.put(e, now, m.limit)
			added[i] = true
		}
		s.mu.Unlock()
	}
	return added, nil
}

// put stores e, making room first when the shard is full: expired keys go, then arbitrary keys
// down to 90% of the limit, so the cost of a full sweep is spread over many inserts.
func (s *shard) put(e Entry, now int64, limit int) {
	if _, exists := s.m[e.Key]; !exists && len(s.m) >= limit {
		for k, it := range s.m {
			if it.expires <= now {
				delete(s.m, k)
			}
		}
		for k := range s.m {
			if len(s.m) < limit*9/10 {
				break
			}
			delete(s.m, k)
		}
	}
	s.m[e.Key] = item{value: append([]byte(nil), e.Value...), expires: now + int64(e.TTL)}
}
