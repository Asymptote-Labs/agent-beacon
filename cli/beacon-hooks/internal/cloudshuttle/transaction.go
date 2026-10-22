package cloudshuttle

import (
	"context"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

// lockUpload serializes state reads, snapshots, provider acknowledgment and checkpoint
// replacement across hook processes. The file remains in place even when state is reset.
// Lock order is transaction -> runtime log; no network work holds the runtime-log lock.
func lockUpload(ctx context.Context, statePath string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(statePath), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(statePath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	held, err := filelock.ExclusiveContext(ctx, f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = held.Release()
		_ = f.Close()
	}, nil
}
