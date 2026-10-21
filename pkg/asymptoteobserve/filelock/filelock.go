// Package filelock provides the advisory file locking Beacon uses to serialize appends to a
// shared log.
//
// It exists because four places had hand-rolled the same `syscall.Flock` sequence -- the endpoint
// writer, the inventory heartbeat, the hook adapter's logger, and the cloud shuttle -- and
// syscall.Flock does not exist on Windows. One implementation with a per-OS body is both the
// portability fix and the removal of three copies that had to be kept in step by hand.
//
// The contract is deliberately narrow: lock an already-open file, unlock it. Opening is left to
// the caller because the call sites differ in ways that matter -- the runtime log's lock file is
// created world-writable so hooks running as the console user can take it, while the others are
// owner-only -- and folding those differences in here would mean a mode argument that each caller
// has to get right anyway.
package filelock

import (
	"context"
	"os"
	"time"
)

// Lock is a held lock. Release unlocks; closing the file is the caller's business, since the
// caller opened it.
type Lock struct {
	file     *os.File
	released bool
}

// Exclusive blocks until it holds an exclusive lock on f.
//
// Blocking is the required behavior, not a convenience: these locks serialize appends to a log
// several processes write to, and a caller that gave up on contention would drop the event it was
// about to write.
func Exclusive(f *os.File) (*Lock, error) {
	if f == nil {
		return nil, os.ErrInvalid
	}
	if err := lockFile(f, true); err != nil {
		return nil, err
	}
	return &Lock{file: f}, nil
}

// Shared blocks until it holds a shared lock on f, so several readers can proceed together while
// excluding writers.
func Shared(f *os.File) (*Lock, error) {
	if f == nil {
		return nil, os.ErrInvalid
	}
	if err := lockFile(f, false); err != nil {
		return nil, err
	}
	return &Lock{file: f}, nil
}

// TryExclusive takes an exclusive lock or fails immediately if another holder has it.
//
// The opposite of Exclusive's behavior, and the difference is a design choice at each call site
// rather than a preference. Blocking is right for a log append, where the work must not be lost.
// Failing fast is right for the self-updater, where a second concurrent run has nothing useful to
// add and waiting would just pile up update attempts behind each other.
func TryExclusive(f *os.File) (*Lock, error) {
	if f == nil {
		return nil, os.ErrInvalid
	}
	if err := tryLockFile(f, true); err != nil {
		return nil, err
	}
	return &Lock{file: f}, nil
}

// ExclusiveContext waits for an exclusive lock until ctx is cancelled. Only actual lock
// contention is retried; invalid handles and other operating-system errors are returned.
func ExclusiveContext(ctx context.Context, f *os.File) (*Lock, error) {
	return lockContext(ctx, f, true)
}

// SharedContext is Shared with a cancellable contention wait.
func SharedContext(ctx context.Context, f *os.File) (*Lock, error) {
	return lockContext(ctx, f, false)
}

func lockContext(ctx context.Context, f *os.File, exclusive bool) (*Lock, error) {
	if f == nil {
		return nil, os.ErrInvalid
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := tryLockFile(f, exclusive)
		if err == nil {
			if err := ctx.Err(); err != nil {
				_ = unlockFile(f)
				return nil, err
			}
			return &Lock{file: f}, nil
		}
		if !isContention(err) {
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// Release unlocks. Safe to call more than once, so `defer l.Release()` alongside an explicit
// release on the success path cannot double-unlock -- on Windows that would return an error for
// a lock the process no longer holds, which reads as a failure where there is none.
func (l *Lock) Release() error {
	if l == nil || l.file == nil || l.released {
		return nil
	}
	l.released = true
	return unlockFile(l.file)
}
