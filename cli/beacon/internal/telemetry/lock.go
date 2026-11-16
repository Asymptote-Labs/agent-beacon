package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

// lockWait bounds how long an update of a telemetry file waits for another process's. Telemetry
// must never cost a command anything, so an update that cannot get the lock is skipped.
var lockWait = time.Second

// lockHomeFile takes the cross-process lock at rel under home, waiting at most lockWait. ok is
// false when the lock could not be taken; the caller then leaves the file it guards alone.
func lockHomeFile(home, rel string) (unlock func(), ok bool) {
	path := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockWait)
	defer cancel()
	lock, err := filelock.ExclusiveContext(ctx, f)
	if err != nil {
		_ = f.Close()
		return nil, false
	}
	return func() {
		_ = lock.Release()
		_ = f.Close()
	}, true
}
