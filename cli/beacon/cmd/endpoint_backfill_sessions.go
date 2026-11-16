package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"

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
	sessions, _ := sessionsInLog(path, offset, asymptoteobserve.CollectionMethodPoll)
	return sessions
}

// sessionsInLog lists the sessions with records in the runtime log after offset, each once, in the
// order they first appear, and the offset just past the last complete line it read. method keeps
// only records of that collection method; "" keeps every method. A log shorter than offset yields
// nothing, and offset back.
func sessionsInLog(path string, offset int64, method string) ([]sweptSession, int64) {
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
			Harness asymptoteobserve.HarnessInfo  `json:"harness"`
			Session *asymptoteobserve.SessionInfo `json:"session"`
		}
		if json.Unmarshal(line, &event) != nil || event.Session == nil || event.Session.ID == "" || event.Harness.Name == "" {
			continue
		}
		if method != "" && event.Harness.CollectionMethod != method {
			continue
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
	telemetryRecordTraces       = telemetry.RecordTraces
	traceEndpointConnected      = asymptote.Connected
	telemetryLiveTraceOffset    = telemetry.LiveTraceOffset
	telemetrySetLiveTraceOffset = telemetry.SetLiveTraceOffset
)

// maxLiveTraceScanBytes bounds what one scan for live sessions reads. A longer gap (telemetry was
// off for a while) is skipped rather than read: those sessions are not new any more.
const maxLiveTraceScanBytes = 64 << 20

// recordLiveTraces sends cli_trace_recorded (source live) for the sessions any collector (hooks,
// OTLP, session stores) wrote to the runtime log since the last scheduled inventory run, which is
// on by default, unlike the session sync. The first run, and a run after a long gap, only records
// where the log ends, so history is never reported as live; RecordTraces skips sessions already
// reported. A rotated log (shorter than the offset) is read from its start. An endpoint connected
// to Beacon Cloud only moves the offset: the cloud counts its sessions.
func recordLiveTraces(logPath string, userMode bool) {
	if !endpointSweepRecordsSessions() {
		return
	}
	size := logSize(logPath)
	offset, ok := telemetryLiveTraceOffset()
	if offset > size {
		offset = 0
	}
	if !ok || size-offset > maxLiveTraceScanBytes {
		telemetrySetLiveTraceOffset(size)
		return
	}
	sessions, next := sessionsInLog(logPath, offset, "")
	telemetrySetLiveTraceOffset(next)
	recordLocalTraces(telemetry.TraceSourceLive, userMode, endpointBackfillResult{Sessions: sessions})
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
