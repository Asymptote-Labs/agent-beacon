package gitlink

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// SessionEvidence is what the runtime log says one session did to the repository in the window.
type SessionEvidence struct {
	Link
	WorkingDirectory string `json:"working_directory,omitempty"`
	// Files maps each repository-relative path the session wrote to the time it last wrote it.
	Files map[string]time.Time `json:"-"`
}

// writeOperations are the file.operation values that change a file. Reads and listings carry a
// file path too, and a session that only read a file did not write the commit that changed it.
var writeOperations = map[string]bool{
	"create": true, "modify": true, "delete": true, "write": true, "edit": true, "rename": true, "move": true,
}

var writeActions = map[string]bool{
	"file.created": true, "file.modified": true, "file.deleted": true, "file.edited": true,
}

// ReadEvidence scans the runtime log at logPath for sessions that wrote files inside repo between
// since and until, both inclusive.
//
// The log rotates into numbered archives (runtime.jsonl.1 is the newest). They are read newest
// first and the scan stops at the first file last modified before since: nothing in it or in any
// older archive can fall inside the window. That keeps a hook's cost proportional to recent
// activity rather than to how much history the endpoint keeps.
func ReadEvidence(ctx context.Context, repo Repo, logPath string, since, until time.Time) (map[string]*SessionEvidence, error) {
	sessions := map[string]*SessionEvidence{}
	pending := map[string][]pendingPath{}
	for _, path := range logFilesNewestFirst(logPath) {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().Before(since) {
			break
		}
		if err := scanLogFile(ctx, path, repo, since, until, sessions, pending); err != nil {
			return nil, err
		}
	}
	// Relative paths wait for the session's working directory, which a later event may be the
	// first to report.
	for key, paths := range pending {
		session := sessions[key]
		if session == nil || session.WorkingDirectory == "" {
			continue
		}
		for _, p := range paths {
			if rel, ok := repo.RelPath(p.path, session.WorkingDirectory); ok {
				session.recordFile(rel, p.at)
			}
		}
	}
	for key, session := range sessions {
		if len(session.Files) == 0 {
			delete(sessions, key)
		}
	}
	return sessions, nil
}

type pendingPath struct {
	path string
	at   time.Time
}

func (s *SessionEvidence) recordFile(rel string, at time.Time) {
	if s.Files == nil {
		s.Files = map[string]time.Time{}
	}
	if prev, ok := s.Files[rel]; !ok || at.After(prev) {
		s.Files[rel] = at
	}
}

// LastWrite is the latest time the session wrote any of paths, zero when it wrote none of them.
func (s *SessionEvidence) LastWrite(paths []string) time.Time {
	var last time.Time
	for _, p := range paths {
		if at, ok := s.Files[p]; ok && at.After(last) {
			last = at
		}
	}
	return last
}

func scanLogFile(ctx context.Context, path string, repo Repo, since, until time.Time,
	sessions map[string]*SessionEvidence, pending map[string][]pendingPath) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	sessionKey := []byte(`"session"`)
	for n := 0; ; n++ {
		if n%4096 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		line, err := readLine(reader, maxLineBytes)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// Most lines are not about a session at all (collector health, inventory, metrics). A
		// substring check is far cheaper than decoding them to find that out.
		if !bytes.Contains(line, sessionKey) {
			continue
		}
		var event schema.Event
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.Session == nil || strings.TrimSpace(event.Session.ID) == "" {
			continue
		}
		at, err := schema.ParseTimestamp(event.Timestamp)
		if err != nil || at.Before(since) || at.After(until) {
			continue
		}
		link := Link{
			Harness:   asymptoteobserve.NormalizeHarnessName(event.Harness.Name),
			SessionID: strings.TrimSpace(event.Session.ID),
		}
		if !link.Valid() {
			continue
		}
		key := link.Key()
		session := sessions[key]
		if session == nil {
			session = &SessionEvidence{Link: link}
			sessions[key] = session
		}
		cwd := strings.TrimSpace(event.Session.WorkingDirectory)
		if session.WorkingDirectory == "" && filepath.IsAbs(cwd) {
			session.WorkingDirectory = filepath.Clean(cwd)
		}
		base := cwd
		if base == "" {
			base = session.WorkingDirectory
		}
		for _, written := range writtenPaths(event) {
			if rel, ok := repo.RelPath(written, base); ok {
				session.recordFile(rel, at)
			} else if !filepath.IsAbs(written) && base == "" {
				pending[key] = append(pending[key], pendingPath{path: written, at: at})
			}
		}
	}
}

// maxLineBytes bounds one log line. Beacon's writers cap an event far below it; a longer line is
// corrupt or foreign, and is skipped rather than allowed to end the scan.
const maxLineBytes = 4 * 1024 * 1024

// readLine returns the next line without its newline. A line longer than max is consumed and
// returned empty, so the caller moves past it. io.EOF comes only once no bytes remain.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > max {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		switch err {
		case nil:
			if tooLong {
				return []byte{}, nil
			}
			return bytes.TrimRight(line, "\r\n"), nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(line) == 0 && !tooLong {
				return nil, io.EOF
			}
			if tooLong {
				return []byte{}, nil
			}
			return line, nil
		default:
			return nil, err
		}
	}
}

// writtenPaths returns the paths an event says were written, as recorded (absolute, or relative
// to the session's working directory).
func writtenPaths(event schema.Event) []string {
	var paths []string
	if event.File != nil && event.File.Path != "" {
		op := strings.ToLower(strings.TrimSpace(event.File.Operation))
		if writeActions[event.Event.Action] || writeOperations[op] {
			paths = append(paths, event.File.Path)
		}
	}
	if event.Command != nil && event.Command.Command != "" {
		paths = append(paths, ShellWrittenPaths(event.Command.Command)...)
	} else if event.Tool != nil && event.Tool.Command != "" {
		paths = append(paths, ShellWrittenPaths(event.Tool.Command)...)
	}
	if event.GenAI != nil && event.GenAI.Tool != nil && event.GenAI.Tool.Call != nil {
		paths = append(paths, patchPathsIn(event.GenAI.Tool.Call.Arguments)...)
	}
	return paths
}

// patchPathsIn finds apply_patch envelopes anywhere in a tool call's arguments, which may arrive
// as a decoded object or as the JSON text of one.
func patchPathsIn(value interface{}) []string {
	switch v := value.(type) {
	case string:
		if !strings.Contains(v, "*** Begin Patch") {
			return nil
		}
		// JSON text of the arguments first: its patch is escaped, and read raw it is one line.
		trimmed := strings.TrimSpace(v)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var decoded interface{}
			if json.Unmarshal([]byte(trimmed), &decoded) == nil {
				return patchPathsIn(decoded)
			}
		}
		return PatchPaths(v)
	case map[string]interface{}:
		var out []string
		for _, item := range v {
			out = append(out, patchPathsIn(item)...)
		}
		return out
	case []interface{}:
		var out []string
		for _, item := range v {
			out = append(out, patchPathsIn(item)...)
		}
		return out
	}
	return nil
}

// logFilesNewestFirst returns the active log followed by its numbered archives, newest first.
func logFilesNewestFirst(logPath string) []string {
	files := []string{logPath}
	dir, base := filepath.Dir(logPath), filepath.Base(logPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return files
	}
	type archive struct {
		index int
		path  string
	}
	var archives []archive
	for _, entry := range entries {
		suffix, ok := strings.CutPrefix(entry.Name(), base+".")
		if !ok {
			continue
		}
		index, err := strconv.Atoi(suffix)
		if err != nil || index <= 0 {
			continue
		}
		archives = append(archives, archive{index: index, path: filepath.Join(dir, entry.Name())})
	}
	sort.Slice(archives, func(i, j int) bool { return archives[i].index < archives[j].index })
	for _, a := range archives {
		files = append(files, a.path)
	}
	return files
}
