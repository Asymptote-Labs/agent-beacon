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

// The store is bounded: writing far more keys than the limit keeps it near the limit, and the
// newest key written is always readable.
func TestBounded(t *testing.T) {
	m := NewMemory(shards * 100)
	ctx := context.Background()
	for i := range 50_000 {
		key := fmt.Sprint("k", i)
		_ = m.Set(ctx, Entry{key, []byte("v"), time.Hour})
		if vals, _ := m.Get(ctx, key); vals[0] == nil {
			t.Fatalf("key %s missing right after Set", key)
		}
	}
	total := 0
	for i := range m.shards {
		total += len(m.shards[i].m)
	}
	if total > shards*100 {
		t.Errorf("store holds %d keys, limit %d", total, shards*100)
	}
}
