package hermessession

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync/sessionsynctest"
)

// A sync larger than the rotation window used to rotate its own first events away while moving
// every cursor past them, and report success (#973).
func TestCollectOnceKeepsWhatItWritesAcrossTheRotationWindow(t *testing.T) {
	const sessions = 60
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	createHermesFixtureDB(t, dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < sessions; i++ {
		id := fmt.Sprintf("hermes-session-%03d", i)
		if _, err := db.Exec(`INSERT INTO sessions (id, source, model, cwd, started_at, ended_at, end_reason, message_count, tool_call_count, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, estimated_cost_usd, title)
SELECT ?, source, model, cwd, started_at + ?, ended_at + ?, end_reason, message_count, tool_call_count, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, estimated_cost_usd, title FROM sessions WHERE id = 'hermes-session-1'`, id, i, i); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO messages (session_id, role, content, tool_call_id, tool_calls, tool_name, timestamp, reasoning_content, finish_reason, active)
SELECT ?, role, content, tool_call_id, tool_calls, tool_name, timestamp + ?, reasoning_content, finish_reason, active FROM messages WHERE session_id = 'hermes-session-1' ORDER BY id`, id, i); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	opts := CollectOptions{DBPath: dbPath, Write: true, UserMode: true}
	sessionsynctest.CheckSweepKeepsItsOutput(t, func(statePath, logPath string, rotateBytes int64) (sessionsynctest.Result, error) {
		opts.StatePath, opts.LogPath, opts.RotateBytes = statePath, logPath, rotateBytes
		s, err := CollectOnce(opts)
		return sessionsynctest.Result{EventsEmitted: s.EventsEmitted, Errors: s.Errors, Retention: s.Retention}, err
	})
}
