package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/atomicfile"
)

// TracesPath is the file, relative to the home directory, that remembers which sessions
// cli_trace_recorded has already been sent for. It holds truncated hashes, never session IDs.
const TracesPath = ".beacon/telemetry-sessions.json"

const (
	// maxTraceEventsPerRun bounds the events one sweep sends, so the first backfill of a machine
	// with months of history does not flood the queue; the rest are remembered as seen.
	maxTraceEventsPerRun = 50
	// maxSeenTraces bounds the remembered sessions. The oldest are forgotten first; a session that
	// old is not going to get new records.
	maxSeenTraces = 5000
)

// Trace is one agent session a sweep wrote records for.
type Trace struct {
	Harness string
	ID      string
}

type seenTraces struct {
	// Seen lists the hashes of sessions already reported, oldest first.
	Seen []string `json:"seen"`
}

func tracesPath(home string) string {
	return filepath.Join(home, TracesPath)
}

// traceKey identifies a session without keeping its ID: the first 16 bytes of
// SHA-256(harness NUL id), hex.
func traceKey(t Trace) string {
	sum := sha256.Sum256([]byte(t.Harness + "\x00" + t.ID))
	return hex.EncodeToString(sum[:16])
}

// RecordTraces sends cli_trace_recorded for each session in traces this machine has not reported
// before, at most maxTraceEventsPerRun of them, and remembers every one as reported. source is
// TraceSourceBackfill or TraceSourceLive. Nothing is sent when the reported set cannot be saved,
// since every later run would report the same sessions again.
func RecordTraces(source string, traces []Trace) {
	r := active()
	if r == nil || !r.persist || len(traces) == 0 {
		return
	}
	var state seenTraces
	if data, err := os.ReadFile(tracesPath(r.home)); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	first := len(state.Seen) == 0
	seen := make(map[string]bool, len(state.Seen))
	for _, key := range state.Seen {
		seen[key] = true
	}
	var fresh []Trace
	for _, t := range traces {
		if t.ID == "" || t.Harness == "" {
			continue
		}
		key := traceKey(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		state.Seen = append(state.Seen, key)
		fresh = append(fresh, t)
	}
	if len(fresh) == 0 {
		return
	}
	if len(state.Seen) > maxSeenTraces {
		state.Seen = state.Seen[len(state.Seen)-maxSeenTraces:]
	}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(tracesPath(r.home)), 0o755); err != nil {
		return
	}
	if err := atomicfile.Write(tracesPath(r.home), append(data, '\n'), 0o600); err != nil {
		return
	}
	for i, t := range fresh {
		if i == maxTraceEventsPerRun {
			break
		}
		Capture(EventTraceRecorded, map[string]any{
			PropHarness:          t.Harness,
			PropSource:           source,
			PropIsFirstForDevice: first && i == 0,
		})
	}
}
