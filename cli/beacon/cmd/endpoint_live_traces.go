package cmd

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/telemetry"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const (
	// liveTraceScanBytes bounds what one run reads. A backlog larger than this (a job that did not
	// run for a long time) is read from its newest end, and its oldest sessions go unreported.
	liveTraceScanBytes = 32 << 20
	// liveTraceHeadBytes is how much of a file's first line identifies the file across rotation.
	liveTraceHeadBytes = 4096
	// liveTraceMaxArchives bounds the numbered archives looked at for the file a cursor is in.
	liveTraceMaxArchives = 64
)

// Swapped by tests.
var (
	telemetryLoadLogCursor = telemetry.LoadLogCursor
	telemetrySaveLogCursor = telemetry.SaveLogCursor
)

// recordLiveTraces sends cli_trace_recorded (source live) for the sessions that did something in
// the runtime log since the last run, whichever way they were collected: hooks, OTLP, plugins or
// a session-store sync. The scheduled inventory job calls it, so a local-only endpoint reports the
// sessions it records after the install without the opt-in session sync. RecordTraces reports each
// session once, at most 50 a run.
//
// Only a local-only endpoint with telemetry on reads anything. A first run, a log the cursor was
// not for, and a connected endpoint (whose sessions Beacon Cloud counts) move the cursor to the end
// of the log without reading it, so history is never reported as live.
func recordLiveTraces(logPath string, userMode bool) {
	if !endpointSweepRecordsSessions() {
		return
	}
	key := liveTraceLogKey(logPath)
	cursor, ok := telemetryLoadLogCursor()
	if !ok || cursor.Log != key || traceEndpointConnected(userMode) {
		telemetrySaveLogCursor(liveTraceEndCursor(logPath))
		return
	}
	sessions, next := liveSessionsSince(logPath, cursor, liveTraceScanBytes)
	// Not reported when the cursor cannot move, or every later run would read the same lines.
	if !telemetrySaveLogCursor(next) || len(sessions) == 0 {
		return
	}
	traces := make([]telemetry.Trace, 0, len(sessions))
	for _, session := range sessions {
		traces = append(traces, telemetry.Trace{Harness: session.Harness, ID: session.ID})
	}
	telemetryRecordTraces(telemetry.TraceSourceLive, traces)
}

// startLiveTraceCursor puts the cursor at the end of the log an install wrote, unless one is already
// kept for it, so the first scheduled run reports only sessions recorded after the install (whose
// backfill reports the history itself).
func startLiveTraceCursor(logPath string) {
	if !endpointSweepRecordsSessions() {
		return
	}
	if cursor, ok := telemetryLoadLogCursor(); ok && cursor.Log == liveTraceLogKey(logPath) {
		return
	}
	telemetrySaveLogCursor(liveTraceEndCursor(logPath))
}

// liveTraceLogKey identifies a runtime log without keeping its path.
func liveTraceLogKey(logPath string) string {
	if abs, err := filepath.Abs(logPath); err == nil {
		logPath = abs
	}
	sum := sha256.Sum256([]byte(logPath))
	return hex.EncodeToString(sum[:16])
}

// liveTraceEndCursor is a cursor at the end of the last complete line of the live log.
func liveTraceEndCursor(logPath string) telemetry.LogCursor {
	cursor := telemetry.LogCursor{Log: liveTraceLogKey(logPath)}
	f, err := os.Open(logPath)
	if err != nil {
		return cursor
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return cursor
	}
	cursor.Head = fileHead(f)
	cursor.Offset = lastLineEnd(f, info.Size())
	return cursor
}

// fileHead hashes the first line of f (at most liveTraceHeadBytes of it), "" when f has no complete
// line yet. The first line of a runtime log file carries an event's own timestamp and ID, so it
// tells a file apart from the one it replaced when the log rotated.
func fileHead(f *os.File) string {
	buf := make([]byte, liveTraceHeadBytes)
	n, _ := f.ReadAt(buf, 0)
	head := buf[:n]
	if i := bytes.IndexByte(head, '\n'); i >= 0 {
		head = head[:i+1]
	} else if n < liveTraceHeadBytes {
		return ""
	}
	sum := sha256.Sum256(head)
	return hex.EncodeToString(sum[:16])
}

// lastLineEnd is the offset just past the last newline in f before size, 0 when there is none: a
// line still being written is left for the next run.
func lastLineEnd(f *os.File, size int64) int64 {
	const chunk = 64 * 1024
	buf := make([]byte, chunk)
	for end := size; end > 0; {
		start := max(end-chunk, 0)
		n, err := f.ReadAt(buf[:end-start], start)
		if n < int(end-start) && err != nil {
			return 0
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			return start + int64(i) + 1
		}
		end = start
	}
	return 0
}

// liveTraceSegment is a byte range of one file of the log to read.
type liveTraceSegment struct {
	path     string
	from, to int64
	// midLine is true when from was cut into the file rather than being a line start.
	midLine bool
	// live is true for the live log, whose unfinished last line is left for later.
	live bool
}

// liveSessionsSince lists the sessions with activity in the runtime log after cursor, each once, in
// the order they first appear, and the cursor to start from next time. The cursor's file is found
// among the live log and its archives by its first line, so a rotation since the last run loses
// nothing that is still kept; a cursor whose file is gone reads everything that is. At most
// maxBytes are read, the newest.
func liveSessionsSince(logPath string, cursor telemetry.LogCursor, maxBytes int64) ([]sweptSession, telemetry.LogCursor) {
	next := cursor
	files := liveTraceLogFiles(logPath)
	segments := make([]liveTraceSegment, 0, len(files))
	found := false
	for i, path := range files {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		info, err := f.Stat()
		head := fileHead(f)
		f.Close()
		if err != nil {
			continue
		}
		segment := liveTraceSegment{path: path, to: info.Size(), live: i == 0}
		if head == cursor.Head && cursor.Offset <= info.Size() {
			segment.from = cursor.Offset
			found = true
		}
		if i == 0 {
			next = telemetry.LogCursor{Log: cursor.Log, Head: head}
		}
		segments = append(segments, segment)
		if found {
			break
		}
	}
	if len(segments) == 0 {
		return nil, next
	}
	// Oldest first, cut from the oldest end to maxBytes.
	for i, j := 0, len(segments)-1; i < j; i, j = i+1, j-1 {
		segments[i], segments[j] = segments[j], segments[i]
	}
	var total int64
	for _, s := range segments {
		total += s.to - s.from
	}
	for excess := total - maxBytes; excess > 0 && len(segments) > 0; {
		if size := segments[0].to - segments[0].from; size <= excess {
			excess -= size
			segments = segments[1:]
			continue
		}
		segments[0].from += excess
		segments[0].midLine = true
		excess = 0
	}

	seen := map[sweptSession]bool{}
	var sessions []sweptSession
	for _, s := range segments {
		end := readLiveTraceSegment(s, func(line []byte) {
			if session, ok := lineActiveSession(line); ok && !seen[session] {
				seen[session] = true
				sessions = append(sessions, session)
			}
		})
		if s.live {
			next.Offset = end
		}
	}
	if len(segments) == 0 || !segments[len(segments)-1].live {
		// Everything to read was cut, or the live log could not be opened: start over at its end.
		next = liveTraceEndCursor(logPath)
	}
	return sessions, next
}

// readLiveTraceSegment calls fn with each complete line of s and returns the offset after the last
// one. A line cut at s.from is skipped, and so is the live log's unfinished last line.
func readLiveTraceSegment(s liveTraceSegment, fn func([]byte)) int64 {
	f, err := os.Open(s.path)
	if err != nil {
		return s.from
	}
	defer f.Close()
	from := s.from
	if s.midLine && from > 0 {
		// Start at the line after the cut, unless the cut is right after a newline.
		from--
	}
	reader := bufio.NewReaderSize(io.NewSectionReader(f, from, s.to-from), 64*1024)
	pos := from
	skip := s.midLine && s.from > 0
	for {
		line, err := reader.ReadBytes('\n')
		complete := len(line) > 0 && line[len(line)-1] == '\n'
		if complete || (len(line) > 0 && !s.live) {
			pos += int64(len(line))
			if skip {
				skip = false
			} else {
				fn(line)
			}
		}
		if err != nil {
			return pos
		}
	}
}

// liveTraceLogFiles returns the live log and its numbered archives, newest first, stopping at the
// first archive number that does not exist.
func liveTraceLogFiles(logPath string) []string {
	files := []string{logPath}
	for i := 1; i <= liveTraceMaxArchives; i++ {
		archive := fmt.Sprintf("%s.%d", logPath, i)
		if _, err := os.Lstat(archive); err != nil {
			break
		}
		files = append(files, archive)
	}
	return files
}

// lineActiveSession returns the session a runtime log line is activity of. A session's start and
// end are not activity, so a session that only started and ended (an automated run that did
// nothing) is never counted, and neither is Beacon's own bookkeeping about a session. Only the
// action, the harness name and the session ID are decoded.
func lineActiveSession(line []byte) (sweptSession, bool) {
	if !bytes.Contains(line, []byte(`"session"`)) {
		return sweptSession{}, false
	}
	var event struct {
		Event struct {
			Action string `json:"action"`
		} `json:"event"`
		Harness struct {
			Name string `json:"name"`
		} `json:"harness"`
		Session *struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if json.Unmarshal(line, &event) != nil || event.Session == nil || event.Session.ID == "" || event.Harness.Name == "" {
		return sweptSession{}, false
	}
	switch action := event.Event.Action; {
	case action == "session.started", action == "session.ended", asymptoteobserve.IsBeaconOnlyAction(action):
		return sweptSession{}, false
	}
	return sweptSession{Harness: event.Harness.Name, ID: event.Session.ID}, true
}
