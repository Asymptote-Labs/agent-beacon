package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"

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
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.Size() < offset {
		return nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil
	}
	poll := []byte(`"` + asymptoteobserve.CollectionMethodPoll + `"`)
	seen := map[sweptSession]bool{}
	var sessions []sweptSession
	reader := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && bytes.Contains(line, poll) {
			var event struct {
				Harness asymptoteobserve.HarnessInfo  `json:"harness"`
				Session *asymptoteobserve.SessionInfo `json:"session"`
			}
			if json.Unmarshal(line, &event) == nil && event.Session != nil && event.Session.ID != "" &&
				event.Harness.CollectionMethod == asymptoteobserve.CollectionMethodPoll && event.Harness.Name != "" {
				session := sweptSession{Harness: event.Harness.Name, ID: event.Session.ID}
				if !seen[session] {
					seen[session] = true
					sessions = append(sessions, session)
				}
			}
		}
		if err != nil {
			return sessions
		}
	}
}
