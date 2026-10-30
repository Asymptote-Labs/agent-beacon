package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContextLocksCancelContentionAndRelease(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "exclusive"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "transaction.lock")
			first, err := Exclusive(openLockFile(t, path))
			if err != nil {
				t.Fatal(err)
			}
			defer first.Release()
			second := openLockFile(t, path)
			acquire := ExclusiveContext
			if shared {
				acquire = SharedContext
			}
			// Two retry intervals (10ms each) is enough to prove contention is retried, then cancelled.
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			held, err := acquire(ctx, second)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) || held != nil {
				t.Fatalf("contended lock = %v, %v; want deadline without ownership", held, err)
			}
			if err := first.Release(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			held, err = acquire(ctx, second)
			if err != nil {
				t.Fatalf("lock unavailable after cancellation: %v", err)
			}
			if err := held.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestContextLocksFailClosedOnRealErrors(t *testing.T) {
	for name, acquire := range map[string]func(context.Context, *os.File) (*Lock, error){
		"exclusive": ExclusiveContext, "shared": SharedContext,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := acquire(context.Background(), nil); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("nil file: %v", err)
			}
			f := openLockFile(t, filepath.Join(t.TempDir(), "closed.lock"))
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := acquire(ctx, f)
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("real lock error retried/ignored: %v", err)
			}
		})
	}
}

func TestContextLocksAlreadyCancelledDoNotAcquire(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := openLockFile(t, filepath.Join(t.TempDir(), "free.lock"))
	for _, acquire := range []func(context.Context, *os.File) (*Lock, error){ExclusiveContext, SharedContext} {
		held, err := acquire(ctx, f)
		if !errors.Is(err, context.Canceled) || held != nil {
			t.Fatalf("cancelled lock = %v, %v", held, err)
		}
	}
	other, err := TryExclusive(openLockFile(t, f.Name()))
	if err != nil {
		t.Fatalf("cancelled acquisition leaked ownership: %v", err)
	}
	defer other.Release()
}

func TestSharedContextLocksCoexist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.lock")
	first, err := SharedContext(context.Background(), openLockFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	second, err := SharedContext(ctx, openLockFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
}
