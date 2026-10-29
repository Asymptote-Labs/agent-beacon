package localingest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// spoolDirInHome is where a writer's spools live, under its account's home directory.
const spoolDirInHome = ".beacon/endpoint/spool"

// DefaultSpoolDir is the current account's spool directory, or "" when it has no home.
func DefaultSpoolDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, spoolDirInHome)
}

// defaultFailureBackoff is how long a process skips the socket after a failed send.
const defaultFailureBackoff = 10 * time.Second

// lastSendFailure is when this process last failed to send, in Unix nanoseconds; zero when the last
// attempt succeeded or none has failed.
var lastSendFailure atomic.Int64

// Deliverer is how a writer that is not root gets a line into the system runtime log: through the
// collector's ingest socket when one answers, by appending directly when the log lets it, and into
// its private spool when neither works -- a restricted log whose collector is stopped or
// restarting. A spooled line is late, never lost: the next line that gets through, either way,
// takes the spool with it.
type Deliverer struct {
	Client Client
	// Spool holds lines that could not be written. An empty Dir means DefaultSpoolDir. Each log
	// has its own spool beneath it, so a line is only ever written to the log it was meant for.
	Spool Spool
	// Euid reports the process's effective uid; nil means os.Geteuid. Tests replace it.
	Euid func() int
	// FailureBackoff is how long, after a failed send, this process goes straight to the direct
	// append without trying the socket. Zero means 10s; negative turns it off.
	FailureBackoff time.Duration
}

// Applies reports whether a write to logPath goes through Write's socket path at all. Root writes
// the log directly: it owns the file in every mode, and the events it writes -- a backfill of the
// console user's sessions, the collector's own -- name their account themselves, which the ingest
// server would replace with root. A per-user log is private to its account and always written
// directly. Windows has no ingest socket.
func (d Deliverer) Applies(logPath string) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	euid := os.Geteuid()
	if d.Euid != nil {
		euid = d.Euid()
	}
	return euid != 0 && !asymptoteobserve.RuntimeLogIsPrivate(logPath)
}

// Write gets one line, meant for logPath, into that log. appendLine appends one line directly, the
// way the writer always has.
//
//   - Where Applies is false, it is appendLine and nothing else.
//   - Otherwise the line goes to the collector's ingest socket when there is one, the collector
//     writes logPath, and it takes the line. A rejected line is reported, never retried.
//   - If there is no socket, or the send fails -- a socket a crashed collector left behind, a
//     collector that is hung -- the line is appended directly. On a shared log that simply works.
//   - Only when the direct append is refused for permission, as a restricted log refuses it, is the
//     line spooled.
//
// Every line that gets through takes up to a batch of this account's spool for logPath with it, by
// the same route. After a failed send the process skips the socket for FailureBackoff, so a hook
// writing several events waits out a hung collector once.
func (d Deliverer) Write(logPath string, line []byte, appendLine func([]byte) error) error {
	if !d.Applies(logPath) {
		return appendLine(line)
	}
	if !d.inBackoff() {
		c := d.Client
		c.LogPath = logPath
		resp, err := c.Send([][]byte{line})
		switch {
		case err == nil && resp.Rejected > 0:
			return fmt.Errorf("the collector rejected the event: %w", ErrRejected)
		case err == nil:
			lastSendFailure.Store(0)
			d.drain(logPath, func(lines [][]byte) (int, error) {
				resp, err := c.Send(lines)
				return resp.Processed, err
			})
			return nil
		case errors.Is(err, ErrNoSocket), errors.Is(err, ErrUntrustedSocket), errors.Is(err, ErrLogMismatch):
			// Not a collector this line can go through; the endpoint is shared, or the line is for
			// some other log.
		default:
			lastSendFailure.Store(time.Now().UnixNano())
		}
	}
	err := appendLine(line)
	if err == nil {
		d.drain(logPath, func(lines [][]byte) (int, error) {
			for i, line := range lines {
				if err := appendLine(line); err != nil {
					return i, err
				}
			}
			return len(lines), nil
		})
		return nil
	}
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if spoolErr := d.spoolLine(logPath, line); spoolErr != nil {
		return fmt.Errorf("%w; spool: %v", err, spoolErr)
	}
	return nil
}

func (d Deliverer) inBackoff() bool {
	backoff := d.FailureBackoff
	if backoff == 0 {
		backoff = defaultFailureBackoff
	}
	last := lastSendFailure.Load()
	return backoff > 0 && last != 0 && time.Since(time.Unix(0, last)) < backoff
}

// spoolFor is the spool for logPath: its own directory under the account's spool, named by a
// digest of the log's path, so a drain can only ever deliver a line to the log it was meant for.
func (d Deliverer) spoolFor(logPath string) Spool {
	s := d.Spool
	if s.Dir == "" {
		s.Dir = DefaultSpoolDir()
	}
	if s.Dir == "" {
		return s
	}
	sum := sha256.Sum256([]byte(filepath.Clean(logPath)))
	s.Dir = filepath.Join(s.Dir, hex.EncodeToString(sum[:8]))
	return s
}

func (d Deliverer) spoolLine(logPath string, line []byte) error {
	s := d.spoolFor(logPath)
	if s.Dir == "" {
		return errors.New("no home directory to spool in")
	}
	dropped, err := s.Append([][]byte{line})
	if err != nil {
		return err
	}
	if dropped > 0 {
		return fmt.Errorf("the spool in %s is full", s.Dir)
	}
	return nil
}

// drain sends one batch of logPath's spool through send. Best effort: whatever is left goes with a
// later line.
func (d Deliverer) drain(logPath string, send func([][]byte) (int, error)) {
	s := d.spoolFor(logPath)
	if s.Dir == "" {
		return
	}
	_, _ = s.Drain(MaxLinesPerConn, send)
}

// Pending reports how many events this account has spooled for logPath, for doctor.
func (d Deliverer) Pending(logPath string) (int, error) {
	s := d.spoolFor(logPath)
	if s.Dir == "" {
		return 0, nil
	}
	n, _, err := s.Pending()
	return n, err
}
