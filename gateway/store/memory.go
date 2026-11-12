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
// keys; past that it forgets the keys least recently written or read, so it behaves as a bounded
// cache rather than growing with the number of sessions it has ever seen.
//
// A gateway fills it with a key per session, per piece of content the session reported and per
// turn, so it is sized for many small entries. A key is held as a 128-bit hash of it, which no
// two of even billions of keys share by chance, and an entry as one word: its expiry, and its
// value when that is at most a byte, as every mark the pipeline writes is. A longer value is kept
// beside it. Holding the key strings and a copy of each value took about 210 bytes an entry, and
// at the default million entries a fifth of a gigabyte that the collector traced on every cycle.
//
// Each shard keeps two generations. Keys are written to the current one, and a key read or added
// again from the previous one moves to the current one; when the current one holds half the
// shard's keys it becomes the previous one, and the previous one is dropped whole. Nothing is
// deleted one key at a time, which left a map full of tombstones and half-empty tables, and a
// full shard is never swept under its lock on the call path.
type Memory struct {
	seeds  [2]maphash.Seed
	half   int // keys per shard generation
	now    func() time.Time
	shards [shards]shard
}

type shard struct {
	mu        sync.Mutex
	cur, prev generation
}

// generation is one generation of a shard's keys, with the values longer than a byte beside them.
type generation struct {
	m      map[keyHash]entry
	values map[keyHash][]byte
}

// keyHash identifies a key by two independent 64-bit hashes of it.
type keyHash [2]uint64

// entry packs an entry into a word: its expiry in Unix milliseconds above valueBits, and below
// them how its value is held, and the value itself when it is a single byte.
type entry uint64

const (
	valueBits = 10
	// Value kinds, in bits 8 and 9.
	valueNone  = 0 << 8 // empty or nil: Get returns nil
	valueByte  = 1 << 8 // the low byte is the value
	valueLong  = 2 << 8 // the value is in generation.values
	valueKinds = 3 << 8
)

func (e entry) expires() int64 { return int64(e >> valueBits) }
func (e entry) kind() entry    { return e & valueKinds }

// NewMemory returns a Memory holding at most maxEntries keys.
func NewMemory(maxEntries int) *Memory {
	return &Memory{
		seeds: [2]maphash.Seed{maphash.MakeSeed(), maphash.MakeSeed()},
		half:  max(1, maxEntries/shards/2),
		now:   time.Now,
	}
}

func (m *Memory) hash(key string) (keyHash, *shard) {
	h := keyHash{maphash.String(m.seeds[0], key), maphash.String(m.seeds[1], key)}
	return h, &m.shards[h[0]%shards]
}

// Get implements Store.
func (m *Memory) Get(_ context.Context, keys ...string) ([][]byte, error) {
	now := m.now().UnixMilli()
	out := make([][]byte, len(keys))
	for i, k := range keys {
		h, s := m.hash(k)
		s.mu.Lock()
		if e, value, ok := s.live(h, now, m.half); ok && e.kind() != valueNone {
			if e.kind() == valueByte {
				out[i] = []byte{byte(e)}
			} else {
				out[i] = append([]byte(nil), value...)
			}
		}
		s.mu.Unlock()
	}
	return out, nil
}

// Set implements Store.
func (m *Memory) Set(_ context.Context, entries ...Entry) error {
	now := m.now().UnixMilli()
	for _, e := range entries {
		if e.TTL <= 0 {
			return fmt.Errorf("store: key %q: TTL must be positive", e.Key)
		}
		h, s := m.hash(e.Key)
		s.mu.Lock()
		s.prev.delete(h)
		s.put(h, pack(e, now), e.Value, m.half)
		s.mu.Unlock()
	}
	return nil
}

// Add implements Store.
func (m *Memory) Add(_ context.Context, entries ...Entry) ([]bool, error) {
	now := m.now().UnixMilli()
	added := make([]bool, len(entries))
	for i, e := range entries {
		if e.TTL <= 0 {
			return nil, fmt.Errorf("store: key %q: TTL must be positive", e.Key)
		}
		h, s := m.hash(e.Key)
		s.mu.Lock()
		if _, _, ok := s.live(h, now, m.half); !ok {
			s.put(h, pack(e, now), e.Value, m.half)
			added[i] = true
		}
		s.mu.Unlock()
	}
	return added, nil
}

// pack is e's entry, written at now. A TTL is rounded up to the millisecond, so a key never
// expires before it should.
func pack(e Entry, now int64) entry {
	ttl := int64(e.TTL / time.Millisecond)
	if e.TTL%time.Millisecond != 0 {
		ttl++
	}
	packed := entry(now+ttl) << valueBits
	switch len(e.Value) {
	case 0:
		return packed | valueNone
	case 1:
		return packed | valueByte | entry(e.Value[0])
	}
	return packed | valueLong
}

// live finds h's entry, unexpired at now, moving it to the current generation if it was in the
// previous one; an expired entry is dropped. value is a long value as the shard holds it.
func (s *shard) live(h keyHash, now int64, half int) (e entry, value []byte, ok bool) {
	if e, ok = s.cur.m[h]; ok {
		if e.expires() > now {
			return e, s.cur.values[h], true
		}
		s.cur.delete(h)
		return 0, nil, false
	}
	if e, ok = s.prev.m[h]; !ok {
		return 0, nil, false
	}
	value = s.prev.values[h]
	s.prev.delete(h)
	if e.expires() <= now {
		return 0, nil, false
	}
	s.put(h, e, value, half)
	return e, value, true
}

// put writes h's entry to the current generation, first turning the current generation into the
// previous one when it is full. value is copied when the entry holds it beside itself.
func (s *shard) put(h keyHash, e entry, value []byte, half int) {
	if _, exists := s.cur.m[h]; !exists && len(s.cur.m) >= half {
		s.prev, s.cur = s.cur, generation{}
	}
	if s.cur.m == nil {
		s.cur.m = make(map[keyHash]entry)
	}
	if e.kind() == valueLong {
		if s.cur.values == nil {
			s.cur.values = make(map[keyHash][]byte)
		}
		s.cur.values[h] = append([]byte(nil), value...)
	} else {
		delete(s.cur.values, h)
	}
	s.cur.m[h] = e
}

func (g *generation) delete(h keyHash) {
	delete(g.m, h)
	delete(g.values, h)
}

// len is how many keys the shard holds.
func (s *shard) len() int { return len(s.cur.m) + len(s.prev.m) }
