package filelock

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openLockFile(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// The property every call site depends on: two holders cannot be in the critical section at once.
// Beacon uses these locks to serialize appends to one log from several processes, so an exclusive
// lock that did not actually exclude would interleave two events on one line -- which the check
// layer reports as a corrupt log.
func TestExclusiveLocksSerializeWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl.lock")

	first := openLockFile(t, path)
	lock, err := Exclusive(first)
	if err != nil {
		t.Fatalf("first exclusive lock: %v", err)
	}

	// A second handle to the same file, from this process. Both flock and LockFileEx arbitrate
	// per-handle, so this contends exactly as another process would.
	second := openLockFile(t, path)
	acquired := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		l, err := Exclusive(second)
		if err != nil {
			t.Errorf("second exclusive lock: %v", err)
			return
		}
		close(acquired)
		_ = l.Release()
	}()

	select {
	case <-acquired:
		t.Fatal("a second exclusive lock was granted while the first was held")
	case <-time.After(150 * time.Millisecond):
		// Still blocked, which is correct.
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("the second lock was never granted after the first was released")
	}
	wg.Wait()
}

// Shared locks are what the cloud shuttle takes to read a log without blocking other readers, so
// two of them must be able to coexist. If Shared silently behaved exclusively, snapshotting a log
// would serialize against every other reader.
func TestSharedLocksCoexist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl.lock")

	a, err := Shared(openLockFile(t, path))
	if err != nil {
		t.Fatalf("first shared lock: %v", err)
	}
	defer a.Release()

	done := make(chan error, 1)
	go func() {
		l, err := Shared(openLockFile(t, path))
		if err == nil {
			_ = l.Release()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a second shared lock must be granted: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second shared lock blocked, so Shared is behaving exclusively")
	}
}

// Release has to tolerate being called twice. Call sites defer it and also release on the success
// path, and on Windows unlocking a range the process no longer holds returns an error -- which
// would surface as a failure where nothing is wrong.
func TestReleaseIsIdempotent(t *testing.T) {
	lock, err := Exclusive(openLockFile(t, filepath.Join(t.TempDir(), "x.lock")))
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("a second release must be a no-op, got %v", err)
	}
}

// A nil Lock must be safe, so `defer l.Release()` is writable before checking the error from
// Exclusive -- otherwise every call site needs a nil guard and one of them will forget.
func TestNilLockRelease(t *testing.T) {
	var l *Lock
	if err := l.Release(); err != nil {
		t.Errorf("releasing a nil lock must be a no-op, got %v", err)
	}
}

// A nil file is a programming error, and it must be reported rather than producing a Lock that
// unlocks nothing -- a lock that silently holds nothing is worse than no lock.
func TestNilFileIsRejected(t *testing.T) {
	if _, err := Exclusive(nil); err == nil {
		t.Error("Exclusive(nil) must fail")
	}
	if _, err := Shared(nil); err == nil {
		t.Error("Shared(nil) must fail")
	}
}
