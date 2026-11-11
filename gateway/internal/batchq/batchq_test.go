package batchq

import (
	"slices"
	"sync"
	"testing"
	"time"
)

// Items arrive in order, a full batch is flushed without waiting for the timer, and Close
// delivers what is left.
func TestOrderBatchAndClose(t *testing.T) {
	var mu sync.Mutex
	var got []int
	flushed := make(chan struct{}, 16)
	q := New(100, 4, time.Hour, func(items []int) {
		mu.Lock()
		got = append(got, items...)
		mu.Unlock()
		flushed <- struct{}{}
	})
	q.Put(1, 2, 3, 4) // fills a batch: flushed without the hour-long timer
	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("a full batch was not flushed")
	}
	q.Put(5)
	q.Close()
	if !slices.Equal(got, []int{1, 2, 3, 4, 5}) {
		t.Errorf("got %v", got)
	}
}

// A full queue drops and counts rather than blocking the producer.
func TestDropsWhenFull(t *testing.T) {
	block := make(chan struct{})
	q := New(3, 100, time.Hour, func([]int) { <-block })
	if d := q.Put(1, 2, 3, 4, 5); d != 2 {
		t.Errorf("dropped %d, want 2", d)
	}
	if q.Dropped() != 2 {
		t.Errorf("Dropped() = %d", q.Dropped())
	}
	close(block)
	q.Close()
}
