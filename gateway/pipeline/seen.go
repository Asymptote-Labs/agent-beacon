package pipeline

import (
	"hash/maphash"
	"sync"
	"time"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// seenMemo remembers, per session, a fingerprint of the system prompt and tool catalog this
// process last confirmed against the store. Every request resends them, and asking the store
// whether each was reported before meant a SHA-256 of tens of KB and a store round trip on every
// call, to learn that nothing changed; a request whose set matches the memo skips both.
//
// The store stays the source of truth: the memo only holds what a store round trip established,
// for at most the seen TTL after it. The one difference it makes is timing: content whose seen
// key the store drops early (evicted, or expired sooner because another replica added it first)
// is reported again only once the memo entry lapses.
type seenMemo struct {
	seed   maphash.Seed
	shards [seenShards]seenShard
}

type seenShard struct {
	mu sync.Mutex
	m  map[string]seenMark
}

type seenMark struct {
	fingerprint uint64
	until       time.Time
}

const (
	seenShards   = 64
	seenPerShard = 8192 // a full shard is cleared: a miss costs one store round trip
)

func newSeenMemo() *seenMemo {
	s := &seenMemo{seed: maphash.MakeSeed()}
	for i := range s.shards {
		s.shards[i].m = map[string]seenMark{}
	}
	return s
}

// fingerprint identifies a request's system messages and tools.
func (s *seenMemo) fingerprint(r *llm.Request) uint64 {
	var h maphash.Hash
	h.SetSeed(s.seed)
	field := func(v string) {
		maphash.WriteComparable(&h, len(v))
		h.WriteString(v)
	}
	for _, m := range r.Messages {
		if m.Role != llm.RoleSystem {
			continue
		}
		h.WriteByte('s')
		for _, p := range m.Parts {
			if p.Kind == llm.PartText {
				field(p.Text)
			}
		}
	}
	for _, t := range r.Tools {
		h.WriteByte('t')
		field(t.Name)
		field(t.Description)
		maphash.WriteComparable(&h, len(t.Parameters))
		h.Write(t.Parameters)
	}
	return h.Sum64()
}

func (s *seenMemo) shard(session string) *seenShard {
	return &s.shards[maphash.String(s.seed, session)%seenShards]
}

// known reports whether the session's set had this fingerprint when last confirmed, still valid at now.
func (s *seenMemo) known(session string, fp uint64, now time.Time) bool {
	sh := s.shard(session)
	sh.mu.Lock()
	mark, ok := sh.m[session]
	sh.mu.Unlock()
	return ok && mark.fingerprint == fp && now.Before(mark.until)
}

// confirm records that the store now holds every seen key of the set with this fingerprint.
func (s *seenMemo) confirm(session string, fp uint64, until time.Time) {
	sh := s.shard(session)
	sh.mu.Lock()
	if len(sh.m) >= seenPerShard {
		clear(sh.m)
	}
	sh.m[session] = seenMark{fingerprint: fp, until: until}
	sh.mu.Unlock()
}
