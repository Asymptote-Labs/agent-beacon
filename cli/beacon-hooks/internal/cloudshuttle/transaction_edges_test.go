package cloudshuttle

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

func TestUploadCancellationWhileWaitingForWriterLock(t *testing.T) {
	cfg := transactionConfig(t)
	transactionWrite(t, cfg.LogPath, transactionFirst)
	f, err := os.OpenFile(cfg.LogPath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	held, err := filelock.Exclusive(f)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	var calls atomic.Int32
	transactionClient(t, func(r *http.Request) (*http.Response, error) { calls.Add(1); return transactionOK(), nil })
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Upload(ctx, cfg, true) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("writer contention = %v", err)
		}
	case <-time.After(time.Second):
		t.Error("writer contention ignored context deadline")
		if err := held.Release(); err != nil {
			t.Fatal(err)
		}
		transactionAwait(t, done)
	}
	if calls.Load() != 0 {
		t.Fatal("cancelled snapshot touched provider")
	}
}

func TestUploadTransactionLockErrorsFailClosed(t *testing.T) {
	cfg := transactionConfig(t)
	transactionWrite(t, cfg.LogPath, transactionFirst)
	if err := os.Mkdir(cfg.StatePath+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	transactionClient(t, func(r *http.Request) (*http.Response, error) { calls.Add(1); return transactionOK(), nil })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := Upload(ctx, cfg, true)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("real lock-open error ignored/retried: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("lock-open error touched provider")
	}
	if err := os.Remove(cfg.StatePath + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := Upload(ctx, cfg, true); err != nil {
		t.Fatalf("retry after lock-open repair: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfg.StatePath), "state.json")); err != nil {
		t.Fatal(err)
	}
}
