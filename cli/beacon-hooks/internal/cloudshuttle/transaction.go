package cloudshuttle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

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

// finishPreservation reacquires the writer lock only for validation and the final
// unlink/rename. Appends or replacement during the provider wait stay available for retry.
func finishPreservation(ctx context.Context, cfg Config, snapshot logSnapshot, uploaded bool) error {
	f, err := os.OpenFile(cfg.LogPath+".lock", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	held, err := filelock.ExclusiveContext(ctx, f)
	if err != nil {
		return err
	}
	defer held.Release()
	current, err := os.Stat(cfg.LogPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	sameGeneration := err == nil && os.SameFile(snapshot.info, current)
	// Metadata can survive an in-place rewrite. Bytes must still belong to the snapshot
	// before either successful deletion or the failed-upload archive fallback.
	validPrefix := false
	var prefixErr error
	if sameGeneration && current.Size() >= snapshot.info.Size() {
		validPrefix, prefixErr = snapshotMatchesPrefix(cfg.LogPath, snapshot)
	}
	unchanged := sameGeneration && current.Size() == snapshot.info.Size() && validPrefix
	if !unchanged {
		if cfg.Upload == uploadAsymptote {
			// Empty snapshots and failed providers can leave an older checkpoint.
			// Matching live bytes cannot acknowledge beyond the captured snapshot.
			validCursor := false
			if validPrefix {
				st, err := readState(cfg.StatePath)
				validCursor = err == nil && st.LastSize >= 0 && st.LastSize <= snapshot.info.Size()
			}
			if !validCursor {
				if err := os.Remove(cfg.StatePath); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		}
		if prefixErr != nil {
			return prefixErr
		}
		return fmt.Errorf("runtime log changed during preservation; retry before resetting %s", cfg.LogPath)
	}
	if uploaded {
		return os.Remove(cfg.LogPath)
	}
	preservedPath := fmt.Sprintf("%s.previous-%d", cfg.LogPath, time.Now().UTC().UnixNano())
	return os.Rename(cfg.LogPath, preservedPath)
}

// The runtime writer lock must stay held while validating the live prefix.
func snapshotMatchesPrefix(logPath string, snapshot logSnapshot) (bool, error) {
	previous, err := os.Open(snapshot.path)
	if err != nil {
		return false, err
	}
	defer previous.Close()
	expected, err := hashOpenFile(previous)
	if err != nil {
		return false, err
	}
	current, err := os.Open(logPath)
	if err != nil {
		return false, err
	}
	defer current.Close()
	hash := sha256.New()
	if _, err := io.CopyN(hash, current, snapshot.info.Size()); err != nil {
		return false, err
	}
	return expected == hex.EncodeToString(hash.Sum(nil)), nil
}
