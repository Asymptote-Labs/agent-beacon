package asymptote

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

// BackfillLogName is the file a first connect stages the session backfill in, beside the runtime
// log.
const BackfillLogName = siempack.BackfillLogName

// BackfillLogPath is where the forwarder's backfill source reads, derived from the runtime log so
// a hand-run pack and the managed forwarder agree on it.
func BackfillLogPath(logPath string) string { return siempack.BackfillLogPath(logPath) }

// BackfillOptions bounds what a first connect ships from before the connection point.
type BackfillOptions struct {
	// Since drops events older than it.
	Since time.Time
	// MaxBytes keeps the newest events that fit; older ones are dropped.
	MaxBytes int64
}

// BackfillResult reports what StageBackfill wrote.
type BackfillResult struct {
	Path   string `json:"path"`
	Events int    `json:"events"`
	Bytes  int64  `json:"bytes"`
	// Trimmed is set when older events in the window were left out to stay under MaxBytes.
	Trimmed bool `json:"trimmed,omitempty"`
}

// StageBackfill copies the session-store backfill already in the runtime log into the file the
// forwarder's backfill source reads from the beginning.
//
// The runtime source starts at the connection point, so history recorded before it is never
// shipped. The one exception is the bounded session backfill, which is what makes the Beacon Cloud
// dashboard useful the moment an endpoint connects: events a session-store sync wrote
// (harness.collection_method=poll) at or after Since, newest kept first up to MaxBytes. Live hook,
// OTLP and plugin events from before the connection stay local. The lines are copied byte for
// byte, so the forwarder's privacy transforms apply to them exactly as to live lines.
//
// It runs before the forwarder starts and only on a first connect: a re-connect resumes from
// Vector's checkpoints and would otherwise ship the same history twice. The file is replaced
// atomically, 0600, so a reader never sees half of it.
func StageBackfill(logPath string, opts BackfillOptions) (BackfillResult, error) {
	result := BackfillResult{Path: BackfillLogPath(logPath)}
	if logPath == "" {
		return result, errors.New("runtime log path is required")
	}
	paths := writer.RetainedLogPaths(logPath)
	var lines [][]byte
	var total int64
	// Oldest file first so the staged lines keep the log's order.
	for i := len(paths) - 1; i >= 0; i-- {
		err := scanBackfillLines(paths[i], opts.Since, func(line []byte) {
			lines = append(lines, line)
			total += int64(len(line)) + 1
		})
		if err != nil {
			return result, err
		}
	}
	if opts.MaxBytes > 0 {
		drop := 0
		for total > opts.MaxBytes && drop < len(lines) {
			total -= int64(len(lines[drop])) + 1
			drop++
		}
		if drop > 0 {
			result.Trimmed = true
			lines = lines[drop:]
		}
	}
	if len(lines) == 0 {
		return result, nil
	}
	if err := os.MkdirAll(filepath.Dir(result.Path), 0o755); err != nil {
		return result, err
	}
	data := make([]byte, 0, total)
	for _, line := range lines {
		data = append(append(data, line...), '\n')
	}
	if err := writeFileAtomic(result.Path, data, 0o600); err != nil {
		return result, fmt.Errorf("stage the session backfill: %w", err)
	}
	result.Events = len(lines)
	result.Bytes = int64(len(data))
	return result, nil
}

// RemoveBackfill deletes the staged backfill file. Disconnect calls it so a later first connect
// stages a fresh one rather than shipping a stale copy.
func RemoveBackfill(logPath string) error {
	if logPath == "" {
		return nil
	}
	if err := os.Remove(BackfillLogPath(logPath)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

type backfillLine struct {
	Timestamp string `json:"timestamp"`
	Harness   struct {
		CollectionMethod string `json:"collection_method"`
	} `json:"harness"`
}

func scanBackfillLines(path string, since time.Time, keep func([]byte)) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), writer.MaxEventBytes+1024)
	for scanner.Scan() {
		raw := scanner.Bytes()
		var line backfillLine
		if json.Unmarshal(raw, &line) != nil || line.Harness.CollectionMethod != schema.CollectionMethodPoll {
			continue
		}
		if !since.IsZero() {
			ts, err := schema.ParseTimestamp(line.Timestamp)
			if err != nil || ts.Before(since) {
				continue
			}
		}
		keep(append([]byte(nil), raw...))
	}
	return scanner.Err()
}
