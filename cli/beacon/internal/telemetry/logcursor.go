package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/atomicfile"
)

// LogCursorPath is the file, relative to the home directory, that remembers where the scheduled
// inventory job's last read of the runtime log for cli_trace_recorded stopped. It holds hashes and
// an offset, never a path or anything the log says.
const LogCursorPath = ".beacon/telemetry-log-cursor.json"

// LogCursor is a position in the runtime log that survives rotation: the offset is in the file whose
// first line hashes to Head, whether that file is still the live log or one of its archives.
type LogCursor struct {
	// Log is a truncated hash of the runtime log's path, so a cursor is never applied to another log.
	Log string `json:"log"`
	// Head is a truncated hash of the first line of the file Offset is in, "" for a file that had
	// no complete line yet.
	Head string `json:"head"`
	// Offset is where the next read starts, at the end of a complete line.
	Offset int64 `json:"offset"`
}

func logCursorPath(home string) string {
	return filepath.Join(home, LogCursorPath)
}

// LoadLogCursor returns the stored cursor. ok is false when telemetry is off, cannot remember
// anything in this home, or has no cursor yet.
func LoadLogCursor() (cursor LogCursor, ok bool) {
	r := active()
	if r == nil || !r.persist {
		return LogCursor{}, false
	}
	data, err := os.ReadFile(logCursorPath(r.home))
	if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Log == "" || cursor.Offset < 0 {
		return LogCursor{}, false
	}
	return cursor, true
}

// SaveLogCursor stores cursor, and reports whether it did. Nothing is stored with telemetry off.
func SaveLogCursor(cursor LogCursor) bool {
	r := active()
	if r == nil || !r.persist {
		return false
	}
	data, err := json.Marshal(cursor)
	if err != nil {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(logCursorPath(r.home)), 0o755); err != nil {
		return false
	}
	return atomicfile.Write(logCursorPath(r.home), append(data, '\n'), 0o600) == nil
}
