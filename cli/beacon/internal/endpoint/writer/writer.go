package writer

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

const (
	UserLogPath           = ".beacon/endpoint/logs/runtime.jsonl"
	MaxEventBytes         = 64 * 1024
	DefaultRotateBytes    = 10 * 1024 * 1024
	DefaultRotateArchives = asymptoteobserve.DefaultRuntimeLogRotateArchives
	RotateBytes           = DefaultRotateBytes
	// runtimeFileMode is the shared (system-mode) log's mode. A log inside the current user's home
	// is private instead; asymptoteobserve.RuntimeLogModes decides, identically in all three writers.
	runtimeFileMode = asymptoteobserve.SharedRuntimeLogFileMode
)

type Options struct {
	Path           string
	UserMode       bool
	MaxBytes       int
	RotateSize     int64
	RotateArchives int
	// Guard, when set, keeps this append from rotating out anything the guard's earlier appends
	// wrote, and records where this one landed. See RetentionGuard.
	Guard *RetentionGuard
	// Budget, when set, caps what this append may add to the log; an event that does not fit
	// is refused with ErrBudgetSpent. See Budget.
	Budget *Budget
	// RequireWritten makes a line that could only be spooled an error (ErrSpooled) rather than a
	// success. A caller whose job is to prove the log takes events, such as a destination
	// validation, sets it; telemetry writers leave it unset, because a spooled line is late, not
	// lost.
	RequireWritten bool
}

// ErrSpooled reports that a restricted log refused a line while its collector was unreachable, so
// the line waits in this account's spool for a later one to carry it in.
var ErrSpooled = errors.New("the event was spooled: the restricted runtime log refused it and the Beacon collector is unreachable")

// SystemLogPath is the system-mode runtime log, resolved per platform.
//
// Delegated rather than duplicated: this was one of fourteen copies of the same literal, and the
// copies could not be given a Windows value independently without disagreeing.
func SystemLogPath() string { return endpointconfig.SystemLogPath() }

func DefaultPath(userMode bool) string {
	if userMode {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".", UserLogPath)
		}
		return filepath.Join(home, UserLogPath)
	}
	return SystemLogPath()
}

func AppendEvent(event schema.Event, opts Options) (string, error) {
	if opts.Path == "" {
		opts.Path = DefaultPath(opts.UserMode)
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = MaxEventBytes
	}
	if opts.RotateSize <= 0 {
		opts.RotateSize = DefaultRotateBytes
	}
	if opts.RotateArchives < 1 {
		opts.RotateArchives = DefaultRotateArchives
	}
	asymptoteobserve.AttachDerivedTrace(&event)
	event = SanitizeEvent(event, opts.MaxBytes)
	if err := event.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	if len(data) > opts.MaxBytes {
		event.Raw = nil
		event.Message = asymptoteobserve.TruncateString(event.Message, 1024)
		event.Truncated = true
		data, err = json.Marshal(event)
		if err != nil {
			return "", err
		}
	}
	if len(data) > opts.MaxBytes {
		// Retained content is the next thing to go after raw, as in the hook adapter and the
		// collector exporter. Each string is capped but their number is not (a multi-edit's
		// arguments, a long todo list), and an error here makes a session sweep stop at the line
		// and meet it again on every later sweep.
		compactRetainedContent(&event)
		data, err = json.Marshal(event)
		if err != nil {
			return "", err
		}
	}
	if len(data) > opts.MaxBytes {
		return "", fmt.Errorf("event exceeds maximum size after truncation: %d bytes", len(data))
	}
	// Stamped last, over the bytes that are actually about to be written, so the
	// truncation fallback above cannot leave the ID describing an event that no
	// longer exists. An event already at the size limit keeps its bytes and goes
	// without one: the limit is a hard contract, and refusing to write an event
	// over forty bytes of identity would be the worse trade.
	if event.Event.ID == "" {
		event.Event.ID = asymptoteobserve.EventIDForLine(data)
		stamped, err := json.Marshal(event)
		if err != nil {
			return "", err
		}
		if len(stamped) <= opts.MaxBytes {
			data = stamped
		}
	}
	line := append(data, '\n')
	if opts.Guard == nil && opts.Budget == nil && Ingest.Applies(opts.Path) {
		if err := deliver(opts, data); err != nil {
			return "", err
		}
		return opts.Path, nil
	}
	if err := appendJSONL(opts.Path, line, opts.RotateSize, opts.RotateArchives, opts.Guard, opts.Budget); err != nil {
		return "", err
	}
	return opts.Path, nil
}

// Ingest delivers to the system log through the collector when the endpoint is in the restricted
// log access mode. Tests point it at a socket and spool of their own; nothing else changes it.
var Ingest = defaultIngest()

// defaultIngest is the real socket and the account's spool, except in a test binary, where a
// test that writes a system log as an ordinary account must never reach the machine's collector
// or leave lines in the developer's own spool: there it is a socket that does not exist and a
// spool in a private temporary directory.
func defaultIngest() localingest.Deliverer {
	if !testing.Testing() {
		return localingest.Deliverer{}
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("beacon-writer-test-%d", os.Getpid()))
	socket := filepath.Join(dir, "no-collector.sock")
	if runtime.GOOS == "windows" {
		socket = fmt.Sprintf(`\\.\pipe\beacon-writer-test-%d-none`, os.Getpid())
	}
	return localingest.Deliverer{
		Client: localingest.Client{SocketPath: socket},
		Spool:  localingest.Spool{Dir: filepath.Join(dir, "spool")},
	}
}

// deliver writes one line to the system log as an account that is not root, the way the hook
// adapter does: through the collector's ingest socket when there is one, directly otherwise, and
// into this account's private spool only when the restricted log refuses the direct append (see
// localingest.Deliverer.Write). A guarded or budgeted append (a session backfill) never comes
// here: those limits are decided under the log's lock, which only a direct writer holds, and the
// backfill that sets them runs as root on a system log.
func deliver(opts Options, data []byte) error {
	outcome, err := Ingest.WriteOutcome(opts.Path, data, func(line []byte) error {
		return appendJSONL(opts.Path, append(line[:len(line):len(line)], '\n'), opts.RotateSize, opts.RotateArchives, nil, nil)
	})
	if err == nil && outcome == localingest.Spooled && opts.RequireWritten {
		return ErrSpooled
	}
	return err
}

// compactRetainedContent drops an event's retained text and records that it did, so a reader can
// still tell what the event was about and that its content was held back rather than never
// captured. Mirrors the collector exporter's step of the same name.
func compactRetainedContent(event *schema.Event) {
	if event.File != nil {
		event.File.Diff = ""
	}
	if event.Command != nil {
		event.Command.Output = ""
	}
	if event.Prompt != nil {
		event.Prompt.Text = ""
	}
	if event.GenAI != nil {
		event.GenAI.Input = nil
		event.GenAI.Output = nil
		if event.GenAI.Tool != nil && event.GenAI.Tool.Call != nil {
			event.GenAI.Tool.Call.Arguments = nil
			event.GenAI.Tool.Call.Result = nil
		}
		// An event whose only gen_ai content was the turn text drops the object rather than
		// writing "gen_ai":{}, as the exporter does.
		if reflect.ValueOf(*event.GenAI).IsZero() {
			event.GenAI = nil
		}
	}
	if event.Content != nil {
		event.Content.Included = false
		event.Content.Truncated = true
	}
}

// EnsureSystemLogWritable prepares a machine-wide runtime log so the people whose agent sessions
// this endpoint captures can append to it, and nobody else can redirect what the privileged
// writers do there.
//
// path is the runtime log itself, not its directory: on Windows the log and its lock are created
// here, and the files of its family (archives, lock) are reset, so the base name matters.
//
// On POSIX this makes the directory 0755 and leaves the 0666 file mode to openRuntimeFile. On
// Windows it is the step without which a system-mode install produces a healthy collector that
// records nothing the agent did -- and the step that keeps the grant from being more than hooks
// need; see secureSystemLog.
//
// Creates the directory first. Install calls this before anything has written a log there, so on a
// fresh system-mode install the directory does not exist yet -- and securing a path that is not
// there fails, taking the whole install down before the service is ever registered. Ensure means
// ensure.
func EnsureSystemLogWritable(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create the log directory %s: %w", dir, err)
	}
	// Chmod after MkdirAll, and for an existing directory too.
	//
	// MkdirAll's mode is masked by the process umask, so on a host with umask 027 or 077 -- any
	// CIS-hardened build -- this directory came out 0750 or 0700 owned by root. The runtime log
	// inside it is deliberately 0666 so hooks running as the logged-in user can append, but a
	// non-root process cannot open a file it cannot traverse to. Every hook write then failed with
	// EACCES while the root-owned collector stayed perfectly healthy, so the endpoint looked fine
	// and silently lost every hook-sourced event.
	//
	// 0755 rather than something narrower: hooks need traverse, and the sticky-bit dance that would
	// let them create files here is not needed because the collector owns rotation. The file mode
	// is what grants write, and openRuntimeFile sets that explicitly for the same umask reason.
	if err := os.Chmod(dir, 0755); err != nil {
		return fmt.Errorf("make the log directory %s traversable: %w", dir, err)
	}
	if err := secureSystemLog(path); err != nil {
		return fmt.Errorf("secure the log directory %s: %w", dir, err)
	}
	return nil
}

// LogACLState is what doctor reports about a system log's access control.
type LogACLState struct {
	// HooksCanAppend is true when a hook running as the interactive user can open the log for
	// append and its lock for read-write.
	HooksCanAppend bool
	// Problems lists grants or owners that would let an ordinary user delete, rename or redirect
	// the files a SYSTEM process writes. Empty when the ACL is the one install applies.
	Problems []string
}

// CheckSystemLogACL reports the state EnsureSystemLogWritable establishes, for doctor.
func CheckSystemLogACL(path string) (LogACLState, error) {
	return inspectSystemLog(path)
}

// EnsureRuntimeFile creates the runtime log without appending an event, for doctor --fix and setup
// flows that pre-create the log so hooks can append to it later. The shared system log gets the
// shared writable mode; a log in the current user's home is private (see
// asymptoteobserve.RuntimeLogModes).
func EnsureRuntimeFile(path string) error {
	_, dirMode, _ := asymptoteobserve.RuntimeLogModes(path)
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return err
	}
	f, err := openRuntimeFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	if err != nil {
		return err
	}
	return f.Close()
}

func LastLine(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var last string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), MaxEventBytes)
	for scanner.Scan() {
		last = scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return last, nil
}

func SanitizeEvent(event schema.Event, maxBytes int) schema.Event {
	return asymptoteobserve.SanitizeEvent(event, maxBytes)
}

// Keep this file-mode contract mirrored with the hook adapter and beaconjson exporter; the rotation
// itself is asymptoteobserve.RotateRuntimeLog for all three.
func appendJSONL(path string, line []byte, rotateBytes int64, rotateArchives int, guard *RetentionGuard, budget *Budget) (err error) {
	_, dirMode, _ := asymptoteobserve.RuntimeLogModes(path)
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return err
	}
	lock, err := openRuntimeFileAs(path+".lock", path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return err
	}
	held, err := filelock.Exclusive(lock)
	if err != nil {
		_ = lock.Close()
		return err
	}
	// Unlock before closing. The previous form deferred the unlock first and the close second,
	// which LIFO ordering ran in the opposite order -- it worked only because closing a descriptor
	// releases the lock implicitly, leaving the explicit unlock to fail on a closed handle and have
	// its error discarded.
	defer lock.Close()
	defer held.Release()
	if asymptoteobserve.IsDuplicateEndpointEvent(path, line, asymptoteobserve.EndpointDuplicateWindow) {
		return nil
	}
	// A replay adds no bytes. Reserve only after the duplicate check under the
	// existing log lock, so suppressed events cannot spend backfill capacity.
	if budget != nil {
		if !budget.reserve(int64(len(line))) {
			return ErrBudgetSpent
		}
		defer func() {
			if err != nil {
				budget.release(int64(len(line)))
			}
		}()
	}
	if guard != nil {
		// Decided under the lock, so no other Beacon writer can rotate between the check and the
		// rotation it permits.
		rotate, err := asymptoteobserve.RuntimeLogNeedsRotation(path, rotateBytes, int64(len(line)))
		if err != nil {
			return err
		}
		if rotate {
			discard, err := guard.rotationWouldDiscardOwnOutput(path, rotateArchives)
			if err != nil {
				return err
			}
			if discard {
				return ErrRetentionWindowFull
			}
		}
	}
	// A rotation that cannot happen now is deferred, never fatal, and the line is appended past the
	// size limit for a later write to rotate. Two cases reach here. An unprivileged writer of a system
	// log may not rename or delete in its directory: on POSIX it is root-owned 0755, and on Windows its
	// ACL withholds those rights on purpose, since they would let a user redirect a SYSTEM write. And
	// on Windows a reader that holds the log open without delete sharing blocks the rename for
	// everyone until it lets go. asymptoteobserve.RotateRuntimeLog moves nothing in either case.
	_ = asymptoteobserve.RotateRuntimeLog(path, rotateBytes, rotateArchives, int64(len(line)))
	f, err := openRuntimeFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return err
	}
	if guard != nil {
		info, statErr := f.Stat()
		guard.record(path, rotateArchives, line, info, statErr)
	}
	return nil
}

func openRuntimeFile(path string, flag int) (*os.File, error) {
	return openRuntimeFileAs(path, path, flag)
}

// openRuntimeFileAs opens path (the log or its lock) with the mode logPath calls for, and settles
// its mode through asymptoteobserve.OpenRuntimeLogFile: a new file gets that mode whatever the
// umask, an existing one is only ever narrowed toward it, by its owner, and root never follows a
// symlink or opens anything but a regular file there.
func openRuntimeFileAs(path, logPath string, flag int) (*os.File, error) {
	return asymptoteobserve.OpenRuntimeLogFile(path, logPath, flag)
}

// RetainedLogPaths returns the runtime log at path followed by every archive the rotation contract
// keeps for it, newest first: path, path.1, ..., path.N for N = DefaultRotateArchives. Readers that
// need history older than the live file -- the last event of some kind, say -- walk this list
// rather than guessing how many archives exist. The hook adapter and the beaconjson exporter rotate
// with the same count, so the list covers whichever process wrote the log.
func RetainedLogPaths(path string) []string {
	return retainedPaths(path, DefaultRotateArchives)
}
