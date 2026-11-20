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
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/telemetry"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// sweptSession is one session a sweep wrote records for: the normalized harness name and the
// runtime's own session ID, as the runtime log carries them.
type sweptSession struct {
	Harness string
	ID      string
}

// endpointSweepRecordsSessions decides whether a sweep reads back which sessions it wrote. Only
// telemetry uses the answer, so the read is skipped when telemetry is off. A seam for tests.
var endpointSweepRecordsSessions = telemetry.Enabled

// logSize is the size of the runtime log, 0 when it does not exist yet.
func logSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// sessionsWrittenSince lists the sessions with session-store (poll) records appended to the runtime
// log after offset, each once, in the order they first appear. It reads the log rather than asking
// every collector, so each session-store collector, present and future, is covered the same way,
// and live hook or OTLP lines written while the sweep ran are left out by their collection method.
// A sweep never rotates the log (a collector stops before it would), so a log now shorter than
// offset was replaced by something else and yields nothing.
func sessionsWrittenSince(path string, offset int64) []sweptSession {
	sessions, _ := sessionsInLog(path, offset, asymptoteobserve.CollectionMethodPoll, time.Time{})
	return sessions
}

// sessionsInLog lists the sessions with records in the runtime log after offset, each once, in the
// order they first appear, and the offset just past the last complete line it read. method keeps
// only records of that collection method; "" keeps every record a collector wrote (any non-empty
// collection method), never the records Beacon writes about a session itself, such as a share or
// a commit link, which carry an old session's ID. A non-zero pollSince also leaves out session-store
// (poll) records from before it: a manual sync of old history writes them now, but they are not new
// sessions. A log shorter than offset yields nothing, and offset back.
func sessionsInLog(path string, offset int64, method string, pollSince time.Time) ([]sweptSession, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.Size() < offset {
		return nil, offset
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset
	}
	var marker []byte
	if method != "" {
		marker = []byte(`"` + method + `"`)
	}
	seen := map[sweptSession]bool{}
	var sessions []sweptSession
	reader := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			// A line without its newline is still being written; the next read starts at it.
			return sessions, offset
		}
		offset += int64(len(line))
		if marker != nil && !bytes.Contains(line, marker) {
			continue
		}
		var event struct {
			Timestamp string                        `json:"timestamp"`
			Harness   asymptoteobserve.HarnessInfo  `json:"harness"`
			Session   *asymptoteobserve.SessionInfo `json:"session"`
		}
		if json.Unmarshal(line, &event) != nil || event.Session == nil || event.Session.ID == "" || event.Harness.Name == "" {
			continue
		}
		if event.Harness.CollectionMethod == "" || (method != "" && event.Harness.CollectionMethod != method) {
			continue
		}
		if !pollSince.IsZero() && event.Harness.CollectionMethod == asymptoteobserve.CollectionMethodPoll {
			if at, err := asymptoteobserve.ParseTimestamp(event.Timestamp); err != nil || at.Before(pollSince) {
				continue
			}
		}
		session := sweptSession{Harness: event.Harness.Name, ID: event.Session.ID}
		if !seen[session] {
			seen[session] = true
			sessions = append(sessions, session)
		}
	}
}

// Swapped by tests.
var (
	telemetryRecordTraces  = telemetry.RecordTraces
	traceEndpointConnected = asymptote.Connected
	telemetryLiveTrace     = telemetry.LiveTrace
	telemetrySetLiveTrace  = telemetry.SetLiveTrace
	liveTraceNow           = time.Now
)

const (
	// maxLiveTraceScanBytes bounds what one scan for live sessions reads, across the live log and
	// the archives rotated since the last scan. A longer gap is skipped rather than read.
	maxLiveTraceScanBytes = 64 << 20
	// maxLiveTraceGap is how old the last scan may be for the log written since to count as live.
	// Older (telemetry was off, or the job did not run), only the position moves. It is days, not
	// a couple of inventory intervals, because the job's timer does not fire while a laptop sleeps:
	// a night or a weekend closed must not cost the sessions of the evening before.
	maxLiveTraceGap = 72 * time.Hour
	// livePollMargin is how long before the last scan a session-store record may have happened and
	// still count as live. Those records reach the log after the fact (the session sync runs every
	// 15 minutes, and not while a laptop sleeps), so a session that ended just before a scan is
	// written just after it; history a manual sync writes is weeks old.
	livePollMargin = maxLiveTraceGap
	// maxLiveTraceArchives is how many numbered archives beside the log a scan looks through for
	// the file it last read. Writers keep writer.DefaultRotateArchives; the margin covers a log
	// configured to keep more.
	maxLiveTraceArchives = 16
)

// recordLiveTraces sends cli_trace_recorded (source live) for the sessions any collector (hooks,
// OTLP, session stores) wrote to the runtime log since the last scheduled inventory run, which is
// on by default, unlike the session sync. The first run, and a run more than maxLiveTraceGap after
// the last, only records where the log ends, so history is never reported as live, and session-store
// records count only when they happened no more than livePollMargin before the last run (a manual
// sync of 30-day-old sessions writes them after it, but they are history); RecordTraces
// skips sessions already reported. When the log has rotated since the last run, the file it read
// is found among the archives by its first line and read on from where it stopped, then every
// newer archive, then the live log. An endpoint connected to Beacon Cloud only moves the position:
// the cloud counts its sessions.
func recordLiveTraces(logPath string, userMode bool) {
	if !endpointSweepRecordsSessions() {
		return
	}
	now := liveTraceNow()
	size := logSize(logPath)
	logID := runtimeLogID(logPath)
	save := func(offset int64) {
		telemetrySetLiveTrace(telemetry.LiveTracePosition{Offset: offset, LogID: logID, SavedAt: now})
	}
	last, ok := telemetryLiveTrace()
	if !ok || last.SavedAt.IsZero() || now.Sub(last.SavedAt) > maxLiveTraceGap {
		save(size)
		return
	}
	parts := liveTraceParts(logPath, last, logID, size)
	if parts == nil {
		save(size)
		return
	}
	seen := map[sweptSession]bool{}
	var sessions []sweptSession
	next := int64(0)
	pollSince := last.SavedAt.Add(-livePollMargin)
	for _, part := range parts {
		found, end := sessionsInLog(part.path, part.offset, "", pollSince)
		for _, session := range found {
			if !seen[session] {
				seen[session] = true
				sessions = append(sessions, session)
			}
		}
		if part.path == logPath {
			next = end
		}
	}
	save(next)
	recordLocalTraces(telemetry.TraceSourceLive, userMode, endpointBackfillResult{Sessions: sessions})
}

// liveTracePart is one file a scan for live sessions reads, from offset to its end.
type liveTracePart struct {
	path   string
	offset int64
}

// liveTraceParts lists what a scan reads to cover everything written since last, oldest first,
// ending with the live log. It is nil when that is more than maxLiveTraceScanBytes.
func liveTraceParts(logPath string, last telemetry.LiveTracePosition, logID string, size int64) []liveTracePart {
	if (last.LogID == "" || last.LogID == logID) && last.Offset <= size {
		if size-last.Offset > maxLiveTraceScanBytes {
			return nil
		}
		return []liveTracePart{{logPath, last.Offset}}
	}
	if last.LogID == "" {
		// The file last read had no complete line, so it cannot be told apart from the archives:
		// read the live log from its start rather than every archive.
		return []liveTracePart{{logPath, 0}}
	}
	// Rotated: path.1 is the newest archive. Every archive newer than the file last read is new
	// throughout. When that file has rotated out of reach, every archive left is newer than it.
	var newer []liveTracePart
	var parts []liveTracePart
	for i := 1; i <= maxLiveTraceArchives; i++ {
		archive := fmt.Sprintf("%s.%d", logPath, i)
		if _, err := os.Lstat(archive); err != nil {
			continue
		}
		if runtimeLogID(archive) == last.LogID {
			parts = append(parts, liveTracePart{archive, last.Offset})
			break
		}
		newer = append(newer, liveTracePart{archive, 0})
	}
	for i := len(newer) - 1; i >= 0; i-- {
		parts = append(parts, newer[i])
	}
	parts = append(parts, liveTracePart{logPath, 0})
	total := int64(0)
	for _, part := range parts {
		if n := logSize(part.path) - part.offset; n > 0 {
			total += n
		}
	}
	if total > maxLiveTraceScanBytes {
		return nil
	}
	return parts
}

// runtimeLogID identifies a runtime log file by its first complete line, which appends never
// change and a rotation carries to the archive with it: the first 16 bytes of its SHA-256, hex.
// "" when the file is missing or has no complete line yet.
func runtimeLogID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 64*1024).ReadSlice('\n')
	if err != nil {
		// No newline within the first 64 KiB: still being written, or not a runtime log.
		return ""
	}
	sum := sha256.Sum256(line)
	return hex.EncodeToString(sum[:16])
}

// recordLocalTraces sends cli_trace_recorded for the sessions a sweep wrote that this machine has
// not reported before. Only a local-only endpoint does: one connected to Beacon Cloud forwards the
// sessions themselves, and the cloud counts them there.
func recordLocalTraces(source string, userMode bool, result endpointBackfillResult) {
	if len(result.Sessions) == 0 || traceEndpointConnected(userMode) {
		return
	}
	traces := make([]telemetry.Trace, 0, len(result.Sessions))
	for _, session := range result.Sessions {
		traces = append(traces, telemetry.Trace{Harness: session.Harness, ID: session.ID})
	}
	telemetryRecordTraces(source, traces)
}
