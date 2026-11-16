package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/atomicfile"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

// TracesPath is the file, relative to the home directory, that remembers which sessions
// cli_trace_recorded has already been sent for. It holds truncated hashes, never session IDs.
const TracesPath = ".beacon/telemetry-sessions.json"

// tracesLockPath is the lock file that serializes reading and rewriting TracesPath across
// processes: the install's backfill, the scheduled jobs and a manual sync can finish together.
const tracesLockPath = ".beacon/telemetry-sessions.lock"

// tracesLockWait bounds how long RecordTraces waits for another process's update. Telemetry must
// never cost a command anything, so a run that cannot get the lock sends nothing; the sessions
// stay unreported and the next run reports them.
var tracesLockWait = time.Second

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
	unlock, ok := lockTraces(r.home)
	if !ok {
		return
	}
	fresh, first := markReported(r.home, traces)
	unlock()
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

// lockTraces takes the cross-process lock on the reported set, waiting at most tracesLockWait.
func lockTraces(home string) (unlock func(), ok bool) {
	path := filepath.Join(home, tracesLockPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), tracesLockWait)
	defer cancel()
	lock, err := filelock.ExclusiveContext(ctx, f)
	if err != nil {
		_ = f.Close()
		return nil, false
	}
	return func() {
		_ = lock.Release()
		_ = f.Close()
	}, true
}

// markReported adds the sessions in traces that are not in the reported set to it and saves it,
// returning them, and whether the set was empty before. Nothing is returned when the set cannot
// be saved, since every later run would report the same sessions again. The caller holds the lock.
func markReported(home string, traces []Trace) (fresh []Trace, first bool) {
	var state seenTraces
	if data, err := os.ReadFile(tracesPath(home)); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	first = len(state.Seen) == 0
	seen := make(map[string]bool, len(state.Seen))
	for _, key := range state.Seen {
		seen[key] = true
	}
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
		return nil, first
	}
	if len(state.Seen) > maxSeenTraces {
		state.Seen = state.Seen[len(state.Seen)-maxSeenTraces:]
	}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, false
	}
	if err := atomicfile.Write(tracesPath(home), append(data, '\n'), 0o600); err != nil {
		return nil, false
	}
	return fresh, first
}

// LiveTrace is where the scheduled scan for live sessions stopped reading the runtime log. ok is
// false before the scan's first run, and when telemetry is off.
func LiveTrace() (pos LiveTracePosition, ok bool) {
	r := active()
	if r == nil {
		return LiveTracePosition{}, false
	}
	st := loadState(r.home)
	if st.LiveTrace == nil {
		return LiveTracePosition{}, false
	}
	return *st.LiveTrace, true
}

// SetLiveTrace records where the scan for live sessions stopped reading the runtime log.
func SetLiveTrace(pos LiveTracePosition) {
	r := active()
	if r == nil || !r.persist {
		return
	}
	updateState(r.home, func(st *state) bool {
		st.LiveTrace = &pos
		return true
	})
}
