package store

import (
	"context"
	"fmt"
	"hash/maphash"
	"math"
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
// Each shard keeps its keys in a ring of up to generations generations, each of an eighth of the
// shard's keys. Keys are written to the newest, and a key read or added again moves to it; when
// it is full a new one begins. The oldest is dropped whole: as soon as every key in it has
// expired, and otherwise only when the shard is full, so a key is forgotten before it expires
// only when the shard's live keys fill all but the one generation the expired ones may still
// occupy (seven eighths of its keys). Keys are not deleted one at a time to make room, which left
// a map full of tombstones and half-empty tables, and a full shard is never swept under its lock.
type Memory struct {
	seeds  [2]maphash.Seed
	limit  int // keys per shard
	genCap int // keys per generation
	now    func() time.Time
	shards [shards]shard
}

// generations is how many generations a shard keeps at most. The expired keys of one generation
// can hold room live keys need, so a shard keeps its live keys up to all but 1/generations of its
// limit; looking a key up asks each generation it might be in.
const generations = 8

type shard struct {
	mu sync.Mutex
	// ring holds the generations from ring[head] (the oldest) to ring[(head+count-1)%generations]
	// (the newest).
	ring        [generations]generation
	head, count int
	n           int // keys held
}

// generation is one generation of a shard's keys, with the values longer than a byte beside them,
// and the latest expiry of any key written to it (Unix milliseconds).
type generation struct {
	m      map[keyHash]entry
	values map[keyHash][]byte
	latest int64
}

// keyHash identifies a key by two independent 64-bit hashes of it.
type keyHash [2]uint64

// entry packs an entry into a word: its expiry in Unix milliseconds, rounded up, above valueBits,
// and below them how its value is held, and the value itself when it is a single byte.
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

// NewMemory returns a Memory holding at most maxEntries keys, and never fewer than one a shard
// (64).
func NewMemory(maxEntries int) *Memory {
	limit := max(1, maxEntries/shards)
	return &Memory{
		seeds:  [2]maphash.Seed{maphash.MakeSeed(), maphash.MakeSeed()},
		limit:  limit,
		genCap: max(1, limit/generations),
		now:    time.Now,
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
		if e, value, ok := m.live(s, h, now); ok && e.kind() != valueNone {
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
	t := m.now()
	now, nowMillis := t.UnixNano(), t.UnixMilli()
	for _, e := range entries {
		if e.TTL <= 0 {
			return fmt.Errorf("store: key %q: TTL must be positive", e.Key)
		}
		h, s := m.hash(e.Key)
		s.mu.Lock()
		if g, _, ok := s.find(h); ok && g != s.newest() {
			s.delete(g, h)
		}
		m.put(s, h, pack(e, now), e.Value, nowMillis)
		s.mu.Unlock()
	}
	return nil
}

// Add implements Store.
func (m *Memory) Add(_ context.Context, entries ...Entry) ([]bool, error) {
	t := m.now()
	now, nowMillis := t.UnixNano(), t.UnixMilli()
	added := make([]bool, len(entries))
	for i, e := range entries {
		if e.TTL <= 0 {
			return nil, fmt.Errorf("store: key %q: TTL must be positive", e.Key)
		}
		h, s := m.hash(e.Key)
		s.mu.Lock()
		if _, _, ok := m.live(s, h, nowMillis); !ok {
			m.put(s, h, pack(e, now), e.Value, nowMillis)
			added[i] = true
		}
		s.mu.Unlock()
	}
	return added, nil
}

// pack is e's entry, written at now (Unix nanoseconds). Its expiry is rounded up to the
// millisecond, so a key never expires before it should, and outlives its TTL by less than one.
func pack(e Entry, now int64) entry {
	expires := now + int64(e.TTL)
	if expires < now {
		expires = math.MaxInt64
	}
	millis := expires / int64(time.Millisecond)
	if expires%int64(time.Millisecond) != 0 {
		millis++
	}
	packed := entry(millis) << valueBits
	switch len(e.Value) {
	case 0:
		return packed | valueNone
	case 1:
		return packed | valueByte | entry(e.Value[0])
	}
	return packed | valueLong
}

// live finds h's entry, unexpired at now (Unix milliseconds), moving it to the newest generation
// if it was in an older one; an expired entry is dropped. value is a long value as the shard
// holds it.
func (m *Memory) live(s *shard, h keyHash, now int64) (entry, []byte, bool) {
	g, e, ok := s.find(h)
	if !ok {
		return 0, nil, false
	}
	value := g.values[h]
	if e.expires() <= now {
		s.delete(g, h)
		return 0, nil, false
	}
	if g != s.newest() {
		s.delete(g, h)
		m.put(s, h, e, value, now)
	}
	return e, value, true
}

// put writes h's entry to the newest generation, which must not hold h in an older one. First it
// drops the oldest generations whose keys have all expired, and makes room: a new generation when
// the newest is full, and, when the shard is full, the oldest generation, live or not. value is
// copied when the entry holds it beside itself.
func (m *Memory) put(s *shard, h keyHash, e entry, value []byte, now int64) {
	for s.count > 0 && (s.ring[s.head].latest <= now || len(s.ring[s.head].m) == 0) {
		s.drop()
	}
	g := s.newest()
	if g == nil || !g.has(h) {
		for s.n >= m.limit {
			s.drop()
		}
		if g = s.newest(); g == nil || len(g.m) >= m.genCap {
			if s.count == generations {
				s.drop()
			}
			g = &s.ring[(s.head+s.count)%generations]
			*g = generation{m: make(map[keyHash]entry)}
			s.count++
		}
		s.n++
	}
	if e.kind() == valueLong {
		if g.values == nil {
			g.values = make(map[keyHash][]byte)
		}
		g.values[h] = append([]byte(nil), value...)
	} else {
		delete(g.values, h)
	}
	g.m[h] = e
	g.latest = max(g.latest, e.expires())
}

func (g *generation) has(h keyHash) bool {
	_, ok := g.m[h]
	return ok
}

// find returns the generation holding h, newest first, and h's entry there.
func (s *shard) find(h keyHash) (*generation, entry, bool) {
	for i := s.count - 1; i >= 0; i-- {
		g := &s.ring[(s.head+i)%generations]
		if e, ok := g.m[h]; ok {
			return g, e, true
		}
	}
	return nil, 0, false
}

// newest is the newest generation, nil when there is none.
func (s *shard) newest() *generation {
	if s.count == 0 {
		return nil
	}
	return &s.ring[(s.head+s.count-1)%generations]
}

// delete removes h from generation g.
func (s *shard) delete(g *generation, h keyHash) {
	delete(g.m, h)
	delete(g.values, h)
	s.n--
}

// drop drops the oldest generation whole.
func (s *shard) drop() {
	g := &s.ring[s.head]
	s.n -= len(g.m)
	*g = generation{}
	s.head = (s.head + 1) % generations
	s.count--
}

// len is how many keys the shard holds.
func (s *shard) len() int { return s.n }
