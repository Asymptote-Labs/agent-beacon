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
	// Euid reports the process's effective uid; nil means os.Geteuid. Tests replace it.
	Euid func() int
	// FailureBackoff is how long, after a failed send, this account goes straight to the direct
	// append without trying the socket. Zero means 10s; negative turns it off.
	FailureBackoff time.Duration
	// UnconfirmedGrace is how long lines the collector may already have written wait for it before
	// a direct append may take them. Zero means 2m; negative means no wait.
	UnconfirmedGrace time.Duration
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
//   - If there is no socket, or the send fails before the line reached a collector -- a socket a
//     crashed collector left behind, a collector that refused the connection or said it was busy
//     -- the line is appended directly. On a shared log that simply works.
//   - If the line reached a collector that never gave a usable reply -- killed or restarted
//     mid-write, or hung -- it may already be in the log. It is spooled apart and resent through
//     the collector, which stamps the resend as it stamped the first copy, so the worst case is
//     one event written twice under one event.id rather than two different events. If no
//     collector has taken it within UnconfirmedGrace, a direct append that works takes it, so it
//     is never stranded.
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
		case resp.Rejected > 0:
			return Sent, fmt.Errorf("the collector rejected the event: %w", ErrRejected)
		case resp.Accepted > 0:
			// Written, whatever went wrong after.
			if err != nil {
				d.recordFailure()
				return Sent, nil
			}
			d.clearFailure()
			d.drainThroughCollector(logPath, c)
			return Sent, nil
		case errors.Is(err, ErrLogMismatch):
			collectorElsewhere = true
		case errors.Is(err, ErrNoSocket), errors.Is(err, ErrUntrustedSocket):
			// No collector this line can go through: the endpoint is shared.
		case errors.Is(err, ErrOutcomeUnknown):
			// The collector may have written it before the reply was lost. Appending it here too
			// would log it twice, and the copies would not even share an id, so it waits for the
			// collector instead, which stamps a resent line exactly as it stamped the first.
			d.recordFailure()
			if spoolErr := d.spoolUnconfirmed(logPath, [][]byte{line}); spoolErr != nil {
				return Spooled, fmt.Errorf("%w; spool: %v", err, spoolErr)
			}
			return Spooled, nil
		default:
			d.recordFailure()
		}
	}
	err := appendLine(line)
	if err == nil {
		appendAll := func(lines [][]byte) (int, error) {
			for i, line := range lines {
				if err := appendLine(line); err != nil {
					return i, err
				}
			}
			return len(lines), nil
		}
		d.drain(logPath, appendAll)
		d.drainUnconfirmedDirectly(logPath, appendAll)
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

// unconfirmedDir, inside a log's spool, holds lines the collector may already have written: sent
// with no usable reply. They go back only through the collector, never by direct append.
const unconfirmedDir = "unconfirmed"

func (d Deliverer) unconfirmedFor(logPath string) Spool {
	s := d.spoolFor(logPath)
	if s.Dir != "" {
		s.Dir = filepath.Join(s.Dir, unconfirmedDir)
	}
	return s
}

func (d Deliverer) spoolUnconfirmed(logPath string, lines [][]byte) error {
	s := d.unconfirmedFor(logPath)
	if s.Dir == "" {
		return errors.New("no home directory to spool in")
	}
	dropped, err := s.Append(lines)
	if err != nil {
		return err
	}
	if dropped > 0 {
		return fmt.Errorf("the spool in %s is full", s.Dir)
	}
	return nil
}

// drain sends one batch of logPath's spool through send. Best effort: whatever is left goes with a
// later line. Lines whose delivery is unconfirmed are not in it: see drainThroughCollector and
// drainUnconfirmedDirectly.
func (d Deliverer) drain(logPath string, send func([][]byte) (int, error)) {
	s := d.spoolFor(logPath)
	if s.Dir == "" {
		return
	}
	_, _ = s.Drain(MaxLinesPerConn, send)
}

// defaultUnconfirmedGrace outlasts a collector restart (a repair or package upgrade), so a
// collector that comes back gets the first chance at lines it may already have written.
const defaultUnconfirmedGrace = 2 * time.Minute

// drainUnconfirmedDirectly lets a direct append take unconfirmed lines once nothing has been added
// to them for UnconfirmedGrace. By then a collector that was only restarting has answered; one that
// has not is gone (the endpoint went back to shared, or its collector writes another log), and
// waiting longer would lose the lines rather than risk one of them twice.
func (d Deliverer) drainUnconfirmedDirectly(logPath string, send func([][]byte) (int, error)) {
	u := d.unconfirmedFor(logPath)
	if u.Dir == "" {
		return
	}
	info, err := os.Stat(u.filePath())
	if err != nil {
		return
	}
	grace := d.UnconfirmedGrace
	if grace == 0 {
		grace = defaultUnconfirmedGrace
	}
	if time.Since(info.ModTime()) < grace {
		return
	}
	_, _ = u.Drain(MaxLinesPerConn, send)
}

// drainThroughCollector sends a batch of logPath's unconfirmed lines and a batch of its spool to
// the collector. A batch that itself ends without a usable reply moves to the unconfirmed spool
// rather than staying where a direct append could take it.
func (d Deliverer) drainThroughCollector(logPath string, c Client) {
	if u := d.unconfirmedFor(logPath); u.Dir != "" {
		_, _ = u.Drain(MaxLinesPerConn, func(lines [][]byte) (int, error) {
			resp, err := c.Send(lines)
			return resp.Processed, err
		})
	}
	d.drain(logPath, func(lines [][]byte) (int, error) {
		resp, err := c.Send(lines)
		if err != nil && errors.Is(err, ErrOutcomeUnknown) {
			if spoolErr := d.spoolUnconfirmed(logPath, lines[resp.Processed:]); spoolErr != nil {
				return resp.Processed, err
			}
			return len(lines), nil
		}
		return resp.Processed, err
	})
}

// Pending reports how many events this account has spooled for logPath, for doctor, including the
// ones waiting for the collector to confirm them.
func (d Deliverer) Pending(logPath string) (int, error) {
	s := d.spoolFor(logPath)
	if s.Dir == "" {
		return 0, nil
	}
	n, _, err := s.Pending()
	if err != nil {
		return n, err
	}
	u, _, err := d.unconfirmedFor(logPath).Pending()
	return n + u, err
}
