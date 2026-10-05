package dshsession

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

// drainSpoolTailBytes is how much of the end of the runtime log loadDrainedIDs scans. It
// matches pkg/asymptoteobserve's dedupe tail window so the redelivery guard and the writer's
// own duplicate suppression cover the same history: beyond it, a crash-redrained spool can
// re-append an event whose first copy rotated out of the window, which the log tolerates the
// same way it tolerates any other late backfill.
const drainSpoolTailBytes = 256 * 1024

// drainSpoolMaxLineBytes bounds one spool line while scanning. Spool lines are hook-writer
// output, capped at 64 KiB like every endpoint event; 1 MiB is headroom, not a contract.
const drainSpoolMaxLineBytes = 1024 * 1024

// loadDrainedIDs collects the event.ids present in the last drainSpoolTailBytes of the
// runtime log.
//
// It is the guard that makes a redrain safe. The drain protocol is append-then-delete under
// the spool lock, so the only way the same file is seen twice is a crash between the two
// steps; on redelivery every already-appended id is skipped. This matters most for the
// lifecycle events -- session.started, prompt.submitted, subagent.* -- which sit outside the
// writer's call-id dedupe allowlist and would otherwise land twice, and it is safe in both
// directions: an id found in the tail means that exact event is already recorded, so
// skipping it is correct no matter which copy arrived first.
func loadDrainedIDs(logPath string) map[string]struct{} {
	seen := map[string]struct{}{}
	if strings.TrimSpace(logPath) == "" {
		return seen
	}
	f, err := os.Open(logPath)
	if err != nil {
		return seen
	}
	defer func() { _ = f.Close() }()
	var start int64
	if info, err := f.Stat(); err == nil && info.Size() > drainSpoolTailBytes {
		start = info.Size() - drainSpoolTailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return seen
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), drainSpoolMaxLineBytes)
	// A window that starts mid-line leaves a partial first line; it cannot parse and must
	// not be mistaken for anything.
	partial := start > 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if partial {
			partial = false
			continue
		}
		var event schema.Event
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.Event.ID != "" {
			seen[event.Event.ID] = struct{}{}
		}
	}
	return seen
}

// drainSessionSpool moves one session's staged hook events out of its workspace spool and
// into the runtime log, then deletes each file it emptied. Returns how many events landed.
//
// Why a spool exists: DeepSeek Harness runs hook commands inside the session sandbox
// (#605), which cannot write ~/.beacon but can write the session workspace in
// workspace-write mode. The hook stages events at <cwd>/.beacon/dsh-spool/<id>.jsonl;
// nothing inside the sandbox can hand them to the runtime log, so the drain happens here,
// outside the harness, on the sweep an operator or the watch loop already runs.
//
// The whole sweep holds the spool's own <file>.lock -- the same lock every hook append and
// every rotation of these files takes -- so the file set cannot change under it and a hook
// firing during the drain blocks rather than drops (filelock.Exclusive blocks by design).
// Delete-after-append plus loadDrainedIDs makes a crash mid-drain re-deliverable without
// double-counting. Each staged line must pass spoolLineAccepted; refused lines are counted,
// not appended, and leave with the file.
func drainSessionSpool(ref SessionRef, opts CollectOptions, seen map[string]struct{}) (spoolDrain, error) {
	if !opts.Write || opts.Print {
		return spoolDrain{}, nil
	}
	if ref.Meta == nil || strings.TrimSpace(ref.Meta.CWD) == "" {
		return spoolDrain{}, nil
	}
	base, ok := asymptoteobserve.DSHSpoolPath(ref.Meta.CWD, ref.ID)
	if !ok {
		return spoolDrain{}, nil
	}
	// The workspace is writable from inside the sandbox and this runs outside it, so every
	// path below is checked not to lead somewhere else: a symlinked spool directory, lock or
	// data file would point the drain at files the sandbox itself cannot reach.
	if err := asymptoteobserve.CheckDSHSpoolDir(ref.Meta.CWD); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return spoolDrain{}, nil
		}
		return spoolDrain{}, err
	}
	if files := asymptoteobserve.DSHSpoolFiles(ref.Meta.CWD, ref.ID); len(files) == 0 {
		return spoolDrain{}, nil
	}
	lock, err := openSpoolFile(base+".lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		return spoolDrain{}, fmt.Errorf("open spool lock %s: %w", base+".lock", err)
	}
	held, err := filelock.Exclusive(lock)
	if err != nil {
		_ = lock.Close()
		return spoolDrain{}, fmt.Errorf("lock spool %s: %w", base, err)
	}
	// Unlock before closing: the close listed first runs last.
	defer lock.Close()
	defer held.Release()

	// Re-enumerate under the lock: a hook staged or rotated a file between the first stat
	// and here, and rotation runs under this same lock, so the set is now stable.
	files := asymptoteobserve.DSHSpoolFiles(ref.Meta.CWD, ref.ID)
	var result spoolDrain
	for _, path := range files {
		n, err := drainSpoolFile(path, ref, opts, seen)
		result.Drained += n.Drained
		result.Rejected += n.Rejected
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// spoolDrain counts what one drain did: events appended to the runtime log, and staged lines
// refused because they were not events the session's own hook could have staged.
type spoolDrain struct {
	Drained  int
	Rejected int
}

// openSpoolFile opens a spool path only if it is a regular file, never through a symlink:
// it refuses a non-regular file it finds, then confirms the file it opened is the one it
// checked, which closes the window for swapping a link in between.
func openSpoolFile(path string, flag int) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (mode %s); refusing to follow it", path, info.Mode().Type())
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, flag, asymptoteobserve.DSHSpoolFileMode)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	linked, err := os.Lstat(path)
	if err != nil || !linked.Mode().IsRegular() || !os.SameFile(opened, linked) {
		_ = f.Close()
		return nil, fmt.Errorf("%s changed while it was opened; refusing to follow it", path)
	}
	return f, nil
}

// spoolLineAccepted reports whether a staged event is one this session's sandboxed hook
// could have staged: a DeepSeek Harness hook event naming the session whose spool holds it,
// and not one of the actions only Beacon itself writes.
//
// The spool is the one place sandboxed code can hand bytes to a writer running outside the
// sandbox, and anything in the workspace -- the agent included -- can write it. Without this
// check a staged line could claim another harness, another session, an approval for another
// runtime, or a Beacon-internal record such as an inventory snapshot or a commit link, and
// it would land in the log as an observed hook event. This does not make a staged event
// trustworthy -- whatever runs in the workspace can still write events for its own session
// -- but it confines what the workspace can say to what its own hook could have said.
func spoolLineAccepted(event schema.Event, ref SessionRef) bool {
	if asymptoteobserve.NormalizeHarnessName(event.Harness.Name) != Harness {
		return false
	}
	if event.Harness.CollectionMethod != asymptoteobserve.CollectionMethodHook {
		return false
	}
	if event.Session == nil || event.Session.ID != ref.ID {
		return false
	}
	action := strings.ToLower(strings.TrimSpace(event.Event.Action))
	if action == "" || strings.HasPrefix(action, "inventory.") || action == "session.commit_linked" {
		return false
	}
	return true
}

// drainSpoolFile appends one spool file's events to the runtime log and removes the file.
// An append failure returns early with the file left in place: the events already appended
// carry ids into seen, so the next sweep redelivers only what is left.
func drainSpoolFile(path string, ref SessionRef, opts CollectOptions, seen map[string]struct{}) (spoolDrain, error) {
	var result spoolDrain
	f, err := openSpoolFile(path, os.O_RDONLY)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("read spool %s: %w", path, err)
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		return result, fmt.Errorf("read spool %s: %w", path, err)
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event schema.Event
		if err := json.Unmarshal(line, &event); err != nil {
			// Not an event: a torn tail line from a killed hook, or corrupt bytes nothing
			// downstream could parse either. It is dropped with the file rather than
			// blocking the drain of the valid events around it.
			continue
		}
		if !spoolLineAccepted(event, ref) {
			result.Rejected++
			continue
		}
		// A staged id is trusted only when it re-derives from the line itself, which every
		// line the hook writer stages does. An id that does not was set by something else,
		// and trusting it would let a staged line mark a genuine event as already recorded;
		// the writer derives a fresh one instead.
		if _, verified := asymptoteobserve.VerifiedEventID(line); !verified {
			event.Event.ID = ""
		}
		if event.Event.ID != "" {
			if _, duplicate := seen[event.Event.ID]; duplicate {
				continue
			}
		}
		if _, err := writer.AppendEvent(event, writer.Options{Path: opts.LogPath, UserMode: opts.UserMode}); err != nil {
			return result, fmt.Errorf("append spooled event from %s: %w", path, err)
		}
		if event.Event.ID != "" {
			seen[event.Event.ID] = struct{}{}
		}
		result.Drained++
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return result, fmt.Errorf("remove drained spool %s: %w", path, err)
	}
	return result, nil
}
