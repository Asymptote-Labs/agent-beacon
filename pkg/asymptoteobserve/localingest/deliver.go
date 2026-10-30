package localingest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// spoolDirInHome is where a writer's spools live, under its account's home directory.
const spoolDirInHome = ".beacon/endpoint/spool"

// DefaultSpoolDir is the current account's spool directory, or "" when it has no home.
func DefaultSpoolDir() string {
	home, err := accountHome()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, spoolDirInHome)
}

// defaultFailureBackoff is how long an account skips the socket after a failed send.
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
	// Euid stands in for the process's privilege in tests: 0 is a privileged writer, anything else
	// is not. Nil asks the operating system (processPrivileged).
	Euid func() int
	// FailureBackoff is how long, after a failed send, this account goes straight to the direct
	// append without trying the socket. Zero means 10s; negative turns it off.
	FailureBackoff time.Duration
}

// Applies reports whether a write to logPath goes through Write's socket path at all. A privileged
// writer -- root, or on Windows SYSTEM or an elevated administrator -- writes the log directly: it
// may write the log in every mode, and the events it writes -- a backfill of the console user's
// sessions, the collector's own -- name their account themselves, which the ingest server would
// replace with its own. A per-user log is private to its account and always written directly.
func (d Deliverer) Applies(logPath string) bool {
	return !d.privileged() && !asymptoteobserve.RuntimeLogIsPrivate(logPath)
}

func (d Deliverer) privileged() bool {
	if d.Euid != nil {
		return d.Euid() == 0
	}
	return processPrivileged()
}

// Outcome is where Write put a line.
type Outcome int

const (
	// Appended: written directly, as writers always have.
	Appended Outcome = iota
	// Sent: handed to the collector, which wrote it.
	Sent
	// Spooled: kept in this account's spool, to be written with a later line. Callers that only
	// record telemetry treat it as success; one whose job is to prove the log takes events (a
	// destination validation) must not.
	Spooled
)

// Write gets one line, meant for logPath, into that log. appendLine appends one line directly, the
// way the writer always has.
//
//   - Where Applies is false, it is appendLine and nothing else.
//   - Otherwise the line goes to the collector's ingest socket when there is one, the collector
//     writes logPath, and it takes the line. A rejected line is reported, never retried.
//   - If there is no socket, or the send fails -- a socket a crashed collector left behind, a
//     collector that is hung -- the line is appended directly. On a shared log that simply works.
//   - Only when the direct append is refused for permission, as a restricted log refuses it while
//     its collector is down, is the line spooled. A collector that answered for some other log
//     will never take it, so then the refusal is returned instead.
//
// Every line that gets through takes up to a batch of this account's spool for logPath with it, by
// the same route. After a failed send the account skips the socket for FailureBackoff, in this
// process and in the hook processes that follow it, so a hung collector costs one timeout rather
// than one per event.
func (d Deliverer) Write(logPath string, line []byte, appendLine func([]byte) error) error {
	_, err := d.WriteOutcome(logPath, line, appendLine)
	return err
}

// WriteOutcome is Write, reporting where the line went.
func (d Deliverer) WriteOutcome(logPath string, line []byte, appendLine func([]byte) error) (Outcome, error) {
	if !d.Applies(logPath) {
		return Appended, appendLine(line)
	}
	collectorElsewhere := false
	if !d.inBackoff() {
		c := d.Client
		c.LogPath = logPath
		resp, err := c.Send([][]byte{line})
		switch {
		case err == nil && resp.Rejected > 0:
			return Sent, fmt.Errorf("the collector rejected the event: %w", ErrRejected)
		case err == nil:
			d.clearFailure()
			d.drain(logPath, func(lines [][]byte) (int, error) {
				resp, err := c.Send(lines)
				return resp.Processed, err
			})
			return Sent, nil
		case errors.Is(err, ErrLogMismatch):
			collectorElsewhere = true
		case errors.Is(err, ErrNoSocket), errors.Is(err, ErrUntrustedSocket):
			// No collector this line can go through: the endpoint is shared.
		default:
			d.recordFailure()
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
		return Appended, nil
	}
	if !errors.Is(err, fs.ErrPermission) || collectorElsewhere {
		return Appended, err
	}
	if spoolErr := d.spoolLine(logPath, line); spoolErr != nil {
		return Spooled, fmt.Errorf("%w; spool: %v", err, spoolErr)
	}
	return Spooled, nil
}

// sendFailureMarker, in the account's spool directory, is touched when a send fails. Hooks run as
// a new process per event, so the failure has to outlive the process that saw it.
const sendFailureMarker = ".send-failure"

func (d Deliverer) backoff() time.Duration {
	if d.FailureBackoff == 0 {
		return defaultFailureBackoff
	}
	return d.FailureBackoff
}

func (d Deliverer) inBackoff() bool {
	backoff := d.backoff()
	if backoff <= 0 {
		return false
	}
	if last := lastSendFailure.Load(); last != 0 && time.Since(time.Unix(0, last)) < backoff {
		return true
	}
	dir := d.spoolRoot()
	if dir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, sendFailureMarker))
	if err != nil {
		return false
	}
	age := time.Since(info.ModTime())
	return age >= 0 && age < backoff
}

func (d Deliverer) recordFailure() {
	lastSendFailure.Store(time.Now().UnixNano())
	if d.backoff() <= 0 {
		return
	}
	dir := d.spoolRoot()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, SpoolDirMode); err != nil {
		return
	}
	path := filepath.Join(dir, sendFailureMarker)
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, SpoolFileMode); err == nil {
			_ = f.Close()
		}
	}
}

func (d Deliverer) clearFailure() {
	lastSendFailure.Store(0)
	if dir := d.spoolRoot(); dir != "" {
		_ = os.Remove(filepath.Join(dir, sendFailureMarker))
	}
}

func (d Deliverer) spoolRoot() string {
	if d.Spool.Dir != "" {
		return d.Spool.Dir
	}
	return DefaultSpoolDir()
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
