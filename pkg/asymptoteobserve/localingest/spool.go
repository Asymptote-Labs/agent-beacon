package localingest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

const (
	// DefaultSpoolMaxBytes caps a spool. Events past it are dropped and counted, so a collector
	// that stays down cannot fill the user's disk.
	DefaultSpoolMaxBytes int64 = 8 * 1024 * 1024
	defaultSpoolLockWait       = 250 * time.Millisecond

	spoolFileName = "pending.jsonl"
	spoolLockName = "pending.lock"
	// spoolDrainLockName is held for a whole drain, so two writers never send the same events.
	spoolDrainLockName = "drain.lock"

	// SpoolDirMode and SpoolFileMode keep a spool private: it holds the same content as the log.
	SpoolDirMode  os.FileMode = 0o700
	SpoolFileMode os.FileMode = 0o600
)

// Spool holds events a writer could not hand to the collector, in a directory only the writer's
// own account can read, until a later writer drains them. It is the restricted mode's answer to a
// collector that is stopped or restarting: the event is late rather than lost, and the agent is
// never held up waiting.
type Spool struct {
	Dir string
	// MaxBytes caps the spool; zero means DefaultSpoolMaxBytes.
	MaxBytes int64
	// LockWait bounds how long Append waits for another writer's lock; zero means 250ms.
	LockWait time.Duration
}

func (s Spool) maxBytes() int64 {
	if s.MaxBytes <= 0 {
		return DefaultSpoolMaxBytes
	}
	return s.MaxBytes
}

func (s Spool) filePath() string { return filepath.Join(s.Dir, spoolFileName) }

func (s Spool) openLock() (*os.File, error) { return s.openNamedLock(spoolLockName) }

func (s Spool) openNamedLock(name string) (*os.File, error) {
	if s.Dir == "" {
		return nil, errors.New("localingest: spool directory is not set")
	}
	if err := os.MkdirAll(s.Dir, SpoolDirMode); err != nil {
		return nil, err
	}
	// MkdirAll leaves an existing directory's mode alone and is masked by the umask.
	_ = os.Chmod(s.Dir, SpoolDirMode)
	return os.OpenFile(filepath.Join(s.Dir, name), os.O_CREATE|os.O_RDWR, SpoolFileMode)
}

// Append adds lines to the spool and returns how many were dropped because the spool is full or
// a line could never be sent. On an error, assume none were spooled.
func (s Spool) Append(lines [][]byte) (dropped int, err error) {
	if len(lines) == 0 {
		return 0, nil
	}
	err = s.withLock(func() (err error) {
		f, err := os.OpenFile(s.filePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, SpoolFileMode)
		if err != nil {
			return err
		}
		// A close can report a write the kernel deferred; losing it would lose the line silently.
		defer func() {
			if cerr := f.Close(); cerr != nil && err == nil {
				err = cerr
			}
		}()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		_ = f.Chmod(SpoolFileMode)
		size := info.Size()
		var buf bytes.Buffer
		for _, line := range lines {
			line = bytes.TrimSuffix(line, []byte{'\n'})
			if len(line) == 0 || bytes.IndexByte(line, '\n') >= 0 || len(line) > MaxLineBytes {
				dropped++
				continue
			}
			if size+int64(buf.Len()+len(line)+1) > s.maxBytes() {
				dropped++
				continue
			}
			buf.Write(line)
			buf.WriteByte('\n')
		}
		if buf.Len() == 0 {
			return nil
		}
		_, err = f.Write(buf.Bytes())
		return err
	})
	if err != nil {
		return len(lines), err
	}
	return dropped, nil
}

// Drain sends up to maxLines of the oldest spooled events through send and removes the ones send
// reports processed. send returns how many lines, from the start, it handled for good (written or
// rejected), and an error if it stopped short.
//
// Only one writer drains at a time; another that finds a drain under way skips it and returns zero,
// so concurrent hooks never wait on each other here. The spool itself is locked only to read the
// batch and to remove what was sent, not while sending, so a hook spooling an event meanwhile is
// not held up by a slow collector: appends only ever add to the end, and only the drainer removes
// from the start.
func (s Spool) Drain(maxLines int, send func(lines [][]byte) (int, error)) (n int, err error) {
	if maxLines <= 0 {
		return 0, nil
	}
	if _, err := os.Stat(s.filePath()); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	drainFile, err := s.openNamedLock(spoolDrainLockName)
	if err != nil {
		return 0, err
	}
	defer closeLock(drainFile, &err)
	draining, err := filelock.TryExclusive(drainFile)
	if err != nil {
		return 0, nil
	}
	defer draining.Release()

	var batch [][]byte
	if err := s.withLock(func() error {
		data, err := os.ReadFile(s.filePath())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		batch = splitLines(data)
		if len(batch) > maxLines {
			batch = batch[:maxLines]
		}
		return nil
	}); err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		return 0, nil
	}
	processed, sendErr := send(batch)
	if processed < 0 {
		processed = 0
	}
	if processed > len(batch) {
		processed = len(batch)
	}
	if processed == 0 {
		return 0, sendErr
	}
	if err := s.withLock(func() error {
		data, err := os.ReadFile(s.filePath())
		if err != nil {
			return err
		}
		lines := splitLines(data)
		if processed > len(lines) {
			processed = len(lines)
		}
		return s.rewrite(lines[processed:])
	}); err != nil {
		return processed, err
	}
	return processed, sendErr
}

func (s Spool) withLock(fn func() error) (err error) {
	lockFile, err := s.openLock()
	if err != nil {
		return err
	}
	defer closeLock(lockFile, &err)
	wait := s.LockWait
	if wait <= 0 {
		wait = defaultSpoolLockWait
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	held, err := filelock.ExclusiveContext(ctx, lockFile)
	if err != nil {
		return fmt.Errorf("lock the spool: %w", err)
	}
	defer held.Release()
	return fn()
}

// Pending reports how many events are spooled and their size.
func (s Spool) Pending() (lines int, size int64, err error) {
	data, err := os.ReadFile(s.filePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	return len(splitLines(data)), int64(len(data)), nil
}

// rewrite replaces the spool with remaining, through a rename so a crash leaves either the old
// spool or the new one and never half of each. Called with the lock held.
func (s Spool) rewrite(remaining [][]byte) error {
	if len(remaining) == 0 {
		if err := os.Remove(s.filePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	tmp, err := os.CreateTemp(s.Dir, spoolFileName+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	_ = tmp.Chmod(SpoolFileMode)
	w := bufio.NewWriter(tmp)
	for _, line := range remaining {
		_, _ = w.Write(line)
		_ = w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.filePath())
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		out = append(out, line)
	}
	return out
}

// closeLock closes a lock file and reports a failure to close it when nothing else failed first.
// Lock files are never written, so a close error loses no data, but it is still not dropped.
func closeLock(f *os.File, err *error) {
	if cerr := f.Close(); cerr != nil && *err == nil {
		*err = cerr
	}
}
