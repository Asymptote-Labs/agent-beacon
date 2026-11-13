// Package workspacespool drains the workspace spools hooks stage events in when a sandbox leaves
// them no other way to deliver one: no ingest socket, no runtime log, no account spool (#605,
// #1284). A sandbox that confines a hook's writes to the session's working directory still lets it
// write <workspace>/.beacon/spool/<harness>/<session>/, and Beacon, outside, moves those events
// into the runtime log later.
//
// Everything in a spool is untrusted: the workspace is writable by the agent and by whatever a
// checked-out repository carries. A line reaches the log only through
// asymptoteobserve.AdmitStagedLine, and a spool is drained only for a session Beacon has already
// recorded in that workspace (KnownSessions) or under a root an operator configured
// (RootSessions), so a planted spool for an invented session is never read.
package workspacespool

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

// legacyDSHHarness is the harness whose hooks once staged in .beacon/dsh-spool/<session>.
const legacyDSHHarness = "deepseek_harness"

// knownTailBytes bounds how much of the runtime log KnownSessions reads: enough for the sessions
// of recent days on a busy machine, without reading a whole rotation window on every sweep.
const knownTailBytes int64 = 8 << 20

// maxKnownSessions caps KnownSessions, so a log stuffed with distinct ids cannot make a sweep
// stat an unbounded number of workspace paths.
const maxKnownSessions = 10000

// Session is one harness session in one workspace, whose spool a drain looks in.
type Session struct{ Workspace, Harness, ID string }

// Summary counts what drains did.
type Summary struct {
	Sessions int `json:"sessions"` // sessions whose spool held at least one line
	Events   int `json:"events"`   // staged events appended to the runtime log
	Rejected int `json:"rejected"` // staged lines refused (AdmitStagedLine false, or undecodable into schema.Event)
}

// Emit appends one admitted event, through the sweep's own sink.
type Emit func(event schema.Event) error

// spoolDirs is every spool directory s may have staged in: the shared one, and for DeepSeek
// Harness the directory its hooks used before every harness shared it. A spool whose path below
// the workspace passes through anything but real directories is left out (realSpool).
func (s Session) spoolDirs() []string {
	var out []string
	if dir, ok := asymptoteobserve.WorkspaceSpoolDir(s.Workspace, s.Harness, s.ID); ok && realSpool(s.Workspace, dir) {
		out = append(out, dir)
	}
	if s.Harness == legacyDSHHarness {
		if dir, ok := asymptoteobserve.LegacyDSHSpoolDir(s.Workspace, s.ID); ok && realSpool(s.Workspace, dir) {
			out = append(out, dir)
		}
	}
	return out
}

// realSpool reports whether every component of dir below workspace (.beacon, spool, harness,
// session) is a real directory and the spool file, if present, a regular file no larger than a
// spool can grow. The sandboxed agent can write the workspace and knows its own session id, so it
// can plant .beacon/spool/<harness>/<its-id> as a link to the account's own spool; a drain through
// it would remove the lines it refuses from that spool. Spool.Append never lets the file outgrow
// DefaultSpoolMaxBytes, so a larger one was written by hand and is left unread. The workspace
// itself may be a link; it comes from Beacon's own log or an operator's root.
func realSpool(workspace, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(workspace), dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	path := filepath.Clean(workspace)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	info, err := os.Lstat(localingest.Spool{Dir: dir}.FilePath())
	return errors.Is(err, os.ErrNotExist) || (err == nil && info.Mode().IsRegular() && info.Size() <= localingest.DefaultSpoolMaxBytes)
}

// DrainSession moves s's staged events through emit, counting them in summary.
//
// Each spool is drained in one pass: Spool.Drain reads it once, emit takes the lines in order, and
// the remainder is rewritten once. A drain in small batches would re-read and rewrite the whole
// file per batch. The file is at most DefaultSpoolMaxBytes (realSpool), so one read is bounded.
//
// Delivery is at least once: a drain killed between emitting and removing the lines emits them
// again next time, the contract localingest.Spool keeps for every spool. An emit error (a spent
// budget, a full retention window) stops the drain with the unsent rest left staged. A refused
// line -- not admitted, undecodable, or not a valid event -- is removed with the rest, since it
// would be refused on every later drain too and would block the lines behind it.
//
// Under sessionfs confinement (a root sweep) it does nothing: the drain writes and removes files
// under a user's workspace, which root must not do on their behalf. The user's own sweep drains it.
func DrainSession(s Session, emit Emit, summary *Summary) error {
	if sessionfs.Active() {
		return nil
	}
	counted := false
	for _, dir := range s.spoolDirs() {
		if err := drainSpool(localingest.Spool{Dir: dir}, s, emit, summary, &counted); err != nil {
			return err
		}
	}
	return nil
}

func drainSpool(spool localingest.Spool, s Session, emit Emit, summary *Summary, counted *bool) error {
	_, err := spool.Drain(math.MaxInt, func(lines [][]byte) (int, error) {
		if !*counted {
			*counted = true
			summary.Sessions++
		}
		for i, line := range lines {
			if !asymptoteobserve.AdmitStagedLine(line, s.Harness, s.ID) {
				summary.Rejected++
				continue
			}
			var event schema.Event
			if err := json.Unmarshal(line, &event); err != nil || event.Validate() != nil {
				summary.Rejected++
				continue
			}
			if err := emit(event); err != nil {
				return i, err
			}
			summary.Events++
		}
		return len(lines), nil
	})
	return err
}

// Drain drains each session. A stopped sweep (sessionsync.Stopped) ends it, since every later
// session would be refused the same way; any other error is one session's, and the rest still run.
func Drain(sessions []Session, emit Emit, summary *Summary) error {
	var errs []error
	for _, s := range sessions {
		if err := DrainSession(s, emit, summary); err != nil {
			if sessionsync.Stopped(err) {
				return errors.Join(append(errs, err)...)
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// PendingBytes is how many bytes s's spools hold, waiting for a drain. The sizes come from
// sessionfs.Stat, so a confined reader never follows a user-planted path out of the user's home.
func PendingBytes(s Session) int64 {
	var total int64
	for _, dir := range s.spoolDirs() {
		if info, err := sessionfs.Stat(localingest.Spool{Dir: dir}.FilePath()); err == nil {
			total += info.Size()
		}
	}
	return total
}

// KnownSessions lists the sessions Beacon has recorded in the tail of the runtime log at logPath:
// the distinct working directory, harness and session id of its lines. Session-store collectors
// and live hooks both record a session's working directory, so this is how a sweep learns which
// workspaces to look in, and for which sessions, without trusting anything a workspace holds.
//
// Only the last knownTailBytes are read: of the current file, and of its first archive when the
// current file holds less. A missing log is no sessions.
func KnownSessions(logPath string) ([]Session, error) {
	current, size, err := readTail(logPath, knownTailBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var archive []byte
	if size < knownTailBytes {
		archive, _, err = readTail(logPath+".1", knownTailBytes-size)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	seen := map[Session]bool{}
	var out []Session
	for _, data := range [][]byte{archive, current} {
		for len(data) > 0 && len(out) < maxKnownSessions {
			line := data
			if i := bytes.IndexByte(data, '\n'); i >= 0 {
				line, data = data[:i], data[i+1:]
			} else {
				data = nil
			}
			s, ok := lineSession(line)
			if ok && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// lineSession decodes only the fields that name a line's session.
func lineSession(line []byte) (Session, bool) {
	var event struct {
		Harness struct {
			Name string `json:"name"`
		} `json:"harness"`
		Session *struct {
			ID               string `json:"id"`
			WorkingDirectory string `json:"working_directory"`
		} `json:"session"`
	}
	if len(bytes.TrimSpace(line)) == 0 || json.Unmarshal(line, &event) != nil || event.Session == nil {
		return Session{}, false
	}
	ws := event.Session.WorkingDirectory
	if ws == "" || !filepath.IsAbs(ws) || !asymptoteobserve.ValidSpoolName(event.Harness.Name) || !asymptoteobserve.ValidSpoolName(event.Session.ID) {
		return Session{}, false
	}
	return Session{Workspace: filepath.Clean(ws), Harness: event.Harness.Name, ID: event.Session.ID}, true
}

// readTail reads at most limit bytes from the end of path, without the partial line a cut through
// the middle of the file starts with, and returns the file's size.
func readTail(path string, limit int64) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := info.Size()
	offset := size - limit
	if offset < 0 {
		offset = 0
	}
	data, err := io.ReadAll(io.NewSectionReader(f, offset, size-offset))
	if err != nil {
		return nil, 0, err
	}
	if offset > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		} else {
			data = nil
		}
	}
	return data, size, nil
}

// RootSessions lists every session spool under the operator's configured roots, each a glob of
// workspace directories. An operator naming a root vouches for what is staged there, so these
// sessions need not be known to Beacon; their lines still pass AdmitStagedLine. An invalid glob
// names nothing.
func RootSessions(roots []string) []Session {
	var out []Session
	for _, root := range roots {
		matches, err := filepath.Glob(root)
		if err != nil {
			continue
		}
		for _, ws := range matches {
			out = append(out, WorkspaceSessions(ws)...)
		}
	}
	return out
}

// WorkspaceSessions lists every session spool in one workspace, named literally (`beacon endpoint
// spool drain --workspace`): a path is never a glob here, so one holding `[` or `*` names itself.
// Whoever names the workspace vouches for it, as for a root. Harness and session directories are
// taken only as real directories, never through a symlink a workspace could plant to point a
// drain elsewhere.
func WorkspaceSessions(ws string) []Session {
	var out []Session
	spoolRoot := filepath.Join(ws, asymptoteobserve.WorkspaceSpoolRootRel)
	for _, harness := range realSubdirs(spoolRoot) {
		for _, id := range realSubdirs(filepath.Join(spoolRoot, harness)) {
			out = append(out, Session{Workspace: ws, Harness: harness, ID: id})
		}
	}
	for _, id := range realSubdirs(filepath.Join(ws, asymptoteobserve.LegacyDSHSpoolRootRel)) {
		out = append(out, Session{Workspace: ws, Harness: legacyDSHHarness, ID: id})
	}
	return out
}

// realSubdirs names dir's subdirectories that are real directories with valid spool names. dir
// itself must be a real directory too.
func realSubdirs(dir string) []string {
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		name := entry.Name()
		if !asymptoteobserve.ValidSpoolName(name) {
			continue
		}
		if info, err := os.Lstat(filepath.Join(dir, name)); err == nil && info.IsDir() {
			out = append(out, name)
		}
	}
	return out
}

// Discover is every session a drain covers: those Beacon has recorded, those under the configured
// roots, and those in the workspaces named literally, deduplicated and sorted. A runtime log that
// cannot be read is reported alongside the others.
func Discover(logPath string, roots, workspaces []string) ([]Session, error) {
	known, err := KnownSessions(logPath)
	seen := map[Session]bool{}
	var out []Session
	all := append(known, RootSessions(roots)...)
	for _, ws := range workspaces {
		all = append(all, WorkspaceSessions(ws)...)
	}
	for _, s := range all {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Workspace != b.Workspace {
			return a.Workspace < b.Workspace
		}
		if a.Harness != b.Harness {
			return a.Harness < b.Harness
		}
		return a.ID < b.ID
	})
	return out, err
}
