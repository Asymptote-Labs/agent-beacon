package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestAddIsSetIfAbsent(t *testing.T) {
	m := NewMemory(1000)
	ctx := context.Background()
	got, _ := m.Add(ctx, Entry{"a", []byte("1"), time.Hour}, Entry{"b", []byte("1"), time.Hour})
	if !slices.Equal(got, []bool{true, true}) {
		t.Fatalf("first Add = %v", got)
	}
	got, _ = m.Add(ctx, Entry{"a", []byte("2"), time.Hour}, Entry{"c", []byte("2"), time.Hour})
	if !slices.Equal(got, []bool{false, true}) {
		t.Errorf("second Add = %v, want only the new key stored", got)
	}
	vals, _ := m.Get(ctx, "a", "c", "missing")
	if string(vals[0]) != "1" || string(vals[1]) != "2" || vals[2] != nil {
		t.Errorf("Get = %q", vals)
	}
}

func TestExpiry(t *testing.T) {
	m := NewMemory(1000)
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }
	ctx := context.Background()
	_ = m.Set(ctx, Entry{"k", []byte("v"), time.Minute})
	now = now.Add(2 * time.Minute)
	if vals, _ := m.Get(ctx, "k"); vals[0] != nil {
		t.Errorf("expired key read back: %q", vals[0])
	}
	if added, _ := m.Add(ctx, Entry{"k", []byte("v2"), time.Minute}); !added[0] {
		t.Error("Add refused an expired key")
	}
	if err := m.Set(ctx, Entry{"forever", nil, 0}); err == nil {
		t.Error("a key without TTL was accepted")
	}
}

// The store is bounded: writing far more keys than the limit keeps it within the limit (and its
// floor of one key a shard), and the newest key written is always readable.
func TestBounded(t *testing.T) {
	ctx := context.Background()
	for _, limit := range []int{shards * 100, 1000, 127, 100, 64, 10} {
		m := NewMemory(limit)
		for i := range 20 * max(limit, shards) {
			key := fmt.Sprint("k", i)
			_ = m.Set(ctx, Entry{key, []byte("v"), time.Hour})
			if vals, _ := m.Get(ctx, key); vals[0] == nil {
				t.Fatalf("limit %d: key %s missing right after Set", limit, key)
			}
		}
		total := 0
		for i := range m.shards {
			total += m.shards[i].len()
		}
		if bound := max(limit, shards); total > bound {
			t.Errorf("limit %d: store holds %d keys, more than %d", limit, total, bound)
		}
	}
}

// A full store forgets the keys least recently used: a key read between writes of many times the
// limit is kept, and a key written once and never read again is not.
func TestKeepsKeysInUse(t *testing.T) {
	m := NewMemory(shards * 10)
	ctx := context.Background()
	_ = m.Set(ctx, Entry{"hot", []byte("v"), time.Hour}, Entry{"cold", []byte("v"), time.Hour})
	for i := range shards * 100 {
		_ = m.Set(ctx, Entry{fmt.Sprint("k", i), []byte("v"), time.Hour})
		if vals, _ := m.Get(ctx, "hot"); vals[0] == nil {
			t.Fatalf("a key read after every write was evicted after %d writes", i+1)
		}
	}
	if vals, _ := m.Get(ctx, "cold"); vals[0] != nil {
		t.Error("a key never read again outlived a hundred times the limit in writes")
	}
}

// Values of every length read back as written, as copies, and replacing a long value with a short
// one leaves nothing of it behind.
func TestValues(t *testing.T) {
	m := NewMemory(1000)
	ctx := context.Background()
	long := []byte("a value longer than a byte")
	_ = m.Set(ctx, Entry{"empty", nil, time.Hour}, Entry{"byte", []byte{0xFF}, time.Hour}, Entry{"long", long, time.Hour})
	long[0] = 'X' // the store copied it
	vals, _ := m.Get(ctx, "empty", "byte", "long")
	if vals[0] != nil || !slices.Equal(vals[1], []byte{0xFF}) || string(vals[2]) != "a value longer than a byte" {
		t.Fatalf("Get = %q", vals)
	}
	vals[2][0] = 'Y' // and Get returned a copy
	if again, _ := m.Get(ctx, "long"); string(again[0]) != "a value longer than a byte" {
		t.Errorf("value changed through Get's result: %q", again[0])
	}
	_ = m.Set(ctx, Entry{"long", []byte{1}, time.Hour})
	if vals, _ := m.Get(ctx, "long"); !slices.Equal(vals[0], []byte{1}) {
		t.Errorf("replaced value = %q", vals[0])
	}
	held := 0
	for i := range m.shards {
		held += len(m.shards[i].cur.values) + len(m.shards[i].prev.values)
	}
	if held != 0 {
		t.Errorf("%d long values held after the only one was replaced", held)
	}
}

// A key's expiry is kept to the millisecond, rounded up, so a key outlives its TTL by less than a
// millisecond and never expires before it, wherever in a millisecond it was written.
func TestShortTTL(t *testing.T) {
	m := NewMemory(1000)
	written := time.Unix(1000, 900_000) // 0.9 ms into a millisecond
	now := written
	m.now = func() time.Time { return now }
	ctx := context.Background()
	_ = m.Set(ctx, Entry{"k", []byte("v"), time.Millisecond}) // expires 1.9 ms into it
	now = written.Add(950 * time.Microsecond)
	if vals, _ := m.Get(ctx, "k"); vals[0] == nil {
		t.Error("key expired before its TTL")
	}
	now = written.Add(2 * time.Millisecond)
	if vals, _ := m.Get(ctx, "k"); vals[0] != nil {
		t.Error("key outlived its TTL by a millisecond or more")
	}
}
