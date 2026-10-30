package cmd

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDebugStackTimerIsOffUnlessAPositiveDurationIsSet(t *testing.T) {
	for _, value := range []string{"", "soon", "0s", "-5s", "120"} {
		t.Setenv(debugStacksAfterEnv, value)
		if timer := startDebugStackTimer(&bytes.Buffer{}); timer != nil {
			timer.Stop()
			t.Fatalf("%s=%q started a timer; only a positive duration should", debugStacksAfterEnv, value)
		}
	}
}

// syncBuffer is written by the timer's goroutine and read by the test's.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestDebugStackTimerDumpsEveryGoroutineAndCarriesOn(t *testing.T) {
	t.Setenv(debugStacksAfterEnv, "10ms")
	release := make(chan struct{})
	parked := make(chan struct{})
	go func() {
		close(parked)
		parkedForDebugStackTest(release)
	}()
	<-parked
	defer close(release)

	out := &syncBuffer{}
	timer := startDebugStackTimer(out)
	if timer == nil {
		t.Fatal("a positive duration started no timer")
	}
	defer timer.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "parkedForDebugStackTest") {
		if time.Now().After(deadline) {
			t.Fatalf("no dump naming the parked goroutine within 5s; got:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "still running after 10ms ("+debugStacksAfterEnv+")") {
		t.Fatalf("the dump does not say why it was written:\n%s", out.String())
	}
}

//go:noinline
func parkedForDebugStackTest(release <-chan struct{}) { <-release }
