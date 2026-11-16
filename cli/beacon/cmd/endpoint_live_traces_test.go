package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/telemetry"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// liveLine is a runtime log line of one action in a session.
func liveLine(harness, method, session, action string) string {
	return `{"timestamp":"2026-11-16T00:00:00Z","event":{"id":"` + harness + session + action + `","action":"` + action +
		`"},"harness":{"name":"` + harness + `","collection_method":"` + method + `"},"session":{"id":"` + session +
		`"},"content":{"prompt":"secret prompt text"}}`
}

func appendLog(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLineActiveSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want bool
	}{
		{"hook prompt", liveLine("claude_code", "hook", "s1", "prompt.submitted"), true},
		{"otlp token usage", liveLine("codex", "otlp", "s1", "token.usage"), true},
		{"poll tool call", liveLine("cursor", "poll", "s1", "tool.invoked"), true},
		{"session start", liveLine("pi_cli", "hook", "s1", "session.started"), false},
		{"session end", liveLine("pi_cli", "hook", "s1", "session.ended"), false},
		{"commit link", liveLine("claude_code", "", "s1", "session.commit_linked"), false},
		{"share", liveLine("claude_code", "", "s1", "session.shared"), false},
		{"no session", `{"event":{"action":"inventory.heartbeat"},"harness":{"name":"beacon"}}`, false},
		{"empty session", liveLine("claude_code", "hook", "", "prompt.submitted"), false},
		{"no harness", `{"event":{"action":"prompt.submitted"},"session":{"id":"s1"}}`, false},
		{"not json", `{"session":`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := lineActiveSession([]byte(tc.line)); ok != tc.want {
				t.Fatalf("active = %t, want %t", ok, tc.want)
			}
		})
	}
}

func TestLiveSessionsSinceReadsOnlyNewActivity(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendLog(t, logPath, liveLine("claude_code", "hook", "old", "prompt.submitted"))
	cursor := liveTraceEndCursor(logPath)

	appendLog(t, logPath,
		liveLine("claude_code", "hook", "s1", "session.started"),
		liveLine("claude_code", "hook", "s1", "prompt.submitted"),
		liveLine("claude_code", "hook", "s1", "tool.invoked"),
		// An automated run that only started and ended is not a trace.
		liveLine("pi_cli", "hook", "auto", "session.started"),
		liveLine("pi_cli", "hook", "auto", "session.ended"),
		liveLine("codex", "otlp", "s2", "token.usage"),
		liveLine("cursor", "poll", "s1", "agent.message"),
	)
	// A line still being written is left for the next run.
	partial := liveLine("gemini_cli", "hook", "s3", "prompt.submitted")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(partial[:20]); err != nil {
		t.Fatal(err)
	}
	f.Close()

	sessions, next := liveSessionsSince(logPath, cursor, liveTraceScanBytes)
	want := []sweptSession{{"claude_code", "s1"}, {"codex", "s2"}, {"cursor", "s1"}}
	if !reflect.DeepEqual(sessions, want) {
		t.Fatalf("sessions = %+v, want %+v", sessions, want)
	}
	info, _ := os.Stat(logPath)
	if next.Offset != info.Size()-20 || next.Head != cursor.Head || next.Log != cursor.Log {
		t.Fatalf("next = %+v, cursor %+v, size %d", next, cursor, info.Size())
	}

	f, err = os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(partial[20:] + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	sessions, next = liveSessionsSince(logPath, next, liveTraceScanBytes)
	if want := []sweptSession{{"gemini_cli", "s3"}}; !reflect.DeepEqual(sessions, want) {
		t.Fatalf("after the line was finished: sessions = %+v, want %+v", sessions, want)
	}
	if sessions, _ := liveSessionsSince(logPath, next, liveTraceScanBytes); len(sessions) != 0 {
		t.Fatalf("nothing new, yet sessions = %+v", sessions)
	}
}

func TestLiveSessionsSinceFollowsRotation(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendLog(t, logPath, liveLine("claude_code", "hook", "old", "prompt.submitted"))
	cursor := liveTraceEndCursor(logPath)
	appendLog(t, logPath, liveLine("claude_code", "hook", "before", "prompt.submitted"))

	// Rotated twice since the last run: the cursor's file is now the second archive.
	if err := os.Rename(logPath, logPath+".1"); err != nil {
		t.Fatal(err)
	}
	appendLog(t, logPath, liveLine("codex", "hook", "between", "prompt.submitted"))
	if err := os.Rename(logPath+".1", logPath+".2"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(logPath, logPath+".1"); err != nil {
		t.Fatal(err)
	}
	appendLog(t, logPath, liveLine("cursor", "hook", "after", "prompt.submitted"))

	sessions, next := liveSessionsSince(logPath, cursor, liveTraceScanBytes)
	want := []sweptSession{{"claude_code", "before"}, {"codex", "between"}, {"cursor", "after"}}
	if !reflect.DeepEqual(sessions, want) {
		t.Fatalf("sessions = %+v, want %+v", sessions, want)
	}
	if end := liveTraceEndCursor(logPath); next != end {
		t.Fatalf("next = %+v, want the live log's end %+v", next, end)
	}

	// The cursor's file rotated out of retention: what is still kept is read.
	if err := os.Remove(logPath + ".2"); err != nil {
		t.Fatal(err)
	}
	sessions, _ = liveSessionsSince(logPath, cursor, liveTraceScanBytes)
	if want := []sweptSession{{"codex", "between"}, {"cursor", "after"}}; !reflect.DeepEqual(sessions, want) {
		t.Fatalf("after retention: sessions = %+v, want %+v", sessions, want)
	}

	// The log was removed: nothing, and the cursor is kept; a log written again is read from its
	// beginning, since its first line is not the cursor's.
	for _, path := range []string{logPath, logPath + ".1"} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	sessions, next = liveSessionsSince(logPath, cursor, liveTraceScanBytes)
	if len(sessions) != 0 || next != cursor {
		t.Fatalf("missing log: sessions = %+v, next = %+v", sessions, next)
	}
	appendLog(t, logPath, liveLine("gemini_cli", "hook", "new", "prompt.submitted"))
	if sessions, _ := liveSessionsSince(logPath, next, liveTraceScanBytes); !reflect.DeepEqual(sessions, []sweptSession{{"gemini_cli", "new"}}) {
		t.Fatalf("recreated log: sessions = %+v", sessions)
	}
}

func TestLiveSessionsSinceReadsAtMostTheNewestBytes(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendLog(t, logPath, liveLine("claude_code", "hook", "old", "prompt.submitted"))
	cursor := liveTraceEndCursor(logPath)
	var lines []string
	for _, id := range []string{"a", "b", "c", "d"} {
		lines = append(lines, liveLine("claude_code", "hook", id, "prompt.submitted"))
	}
	appendLog(t, logPath, lines...)
	// Room for the last two lines and half of the one before: the cut line is skipped whole.
	limit := int64(len(lines[2])+1+len(lines[3])+1) + int64(len(lines[1]))/2
	sessions, next := liveSessionsSince(logPath, cursor, limit)
	if want := []sweptSession{{"claude_code", "c"}, {"claude_code", "d"}}; !reflect.DeepEqual(sessions, want) {
		t.Fatalf("sessions = %+v, want %+v", sessions, want)
	}
	if info, _ := os.Stat(logPath); next.Offset != info.Size() {
		t.Fatalf("next offset %d, want the end %d", next.Offset, info.Size())
	}
	// A cut exactly at a line start keeps that line.
	limit = int64(len(lines[2]) + 1 + len(lines[3]) + 1)
	if sessions, _ := liveSessionsSince(logPath, cursor, limit); len(sessions) != 2 {
		t.Fatalf("sessions = %+v, want c and d", sessions)
	}
}

// stubLiveTraces keeps the cursor in memory and records what recordLiveTraces sends.
func stubLiveTraces(t *testing.T, enabled, connected bool) (cursor **telemetry.LogCursor, calls *[]string) {
	t.Helper()
	calls = recordTraces(t, connected)
	cursor = new(*telemetry.LogCursor)
	oldLoad, oldSave, oldEnabled := telemetryLoadLogCursor, telemetrySaveLogCursor, endpointSweepRecordsSessions
	telemetryLoadLogCursor = func() (telemetry.LogCursor, bool) {
		if *cursor == nil {
			return telemetry.LogCursor{}, false
		}
		return **cursor, true
	}
	telemetrySaveLogCursor = func(c telemetry.LogCursor) bool {
		*cursor = &c
		return true
	}
	endpointSweepRecordsSessions = func() bool { return enabled }
	t.Cleanup(func() {
		telemetryLoadLogCursor, telemetrySaveLogCursor, endpointSweepRecordsSessions = oldLoad, oldSave, oldEnabled
	})
	return cursor, calls
}

func TestRecordLiveTraces(t *testing.T) {
	t.Run("first run starts at the end", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
		appendLog(t, logPath, liveLine("claude_code", "hook", "history", "prompt.submitted"))
		cursor, calls := stubLiveTraces(t, true, false)
		recordLiveTraces(logPath, true)
		if len(*calls) != 0 || *cursor == nil || **cursor != liveTraceEndCursor(logPath) {
			t.Fatalf("calls %v, cursor %+v", *calls, *cursor)
		}

		appendLog(t, logPath,
			liveLine("claude_code", "hook", "s1", "prompt.submitted"),
			liveLine("pi_cli", "hook", "auto", "session.started"),
			liveLine("pi_cli", "hook", "auto", "session.ended"))
		recordLiveTraces(logPath, true)
		if want := []string{"live:claude_code/s1"}; !reflect.DeepEqual(*calls, want) {
			t.Fatalf("calls %v, want %v", *calls, want)
		}
		if **cursor != liveTraceEndCursor(logPath) {
			t.Fatalf("cursor %+v not at the end", **cursor)
		}
		recordLiveTraces(logPath, true)
		if len(*calls) != 1 {
			t.Fatalf("a run with nothing new sent %v", *calls)
		}
	})
	t.Run("telemetry off reads and writes nothing", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
		cursor, calls := stubLiveTraces(t, false, false)
		start := liveTraceEndCursor(logPath)
		*cursor = &start
		appendLog(t, logPath, liveLine("claude_code", "hook", "s1", "prompt.submitted"))
		recordLiveTraces(logPath, true)
		if len(*calls) != 0 || **cursor != start {
			t.Fatalf("calls %v, cursor %+v", *calls, **cursor)
		}
	})
	t.Run("connected endpoint only moves the cursor", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
		cursor, calls := stubLiveTraces(t, true, true)
		start := liveTraceEndCursor(logPath)
		*cursor = &start
		appendLog(t, logPath, liveLine("claude_code", "hook", "s1", "prompt.submitted"))
		recordLiveTraces(logPath, true)
		if len(*calls) != 0 || **cursor != liveTraceEndCursor(logPath) {
			t.Fatalf("calls %v, cursor %+v", *calls, **cursor)
		}
	})
	t.Run("a cursor for another log is reset", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "runtime.jsonl")
		cursor, calls := stubLiveTraces(t, true, false)
		other := liveTraceEndCursor(filepath.Join(dir, "other.jsonl"))
		*cursor = &other
		appendLog(t, logPath, liveLine("claude_code", "hook", "s1", "prompt.submitted"))
		recordLiveTraces(logPath, true)
		if len(*calls) != 0 || **cursor != liveTraceEndCursor(logPath) {
			t.Fatalf("calls %v, cursor %+v", *calls, **cursor)
		}
	})
}

func TestStartLiveTraceCursorKeepsAnExistingOne(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendLog(t, logPath, liveLine("claude_code", "hook", "s1", "prompt.submitted"))
	cursor, _ := stubLiveTraces(t, true, false)
	startLiveTraceCursor(logPath)
	if *cursor == nil || **cursor != liveTraceEndCursor(logPath) {
		t.Fatalf("cursor %+v", *cursor)
	}
	kept := telemetry.LogCursor{Log: liveTraceLogKey(logPath), Offset: 0}
	*cursor = &kept
	startLiveTraceCursor(logPath)
	if **cursor != kept {
		t.Fatalf("an existing cursor was replaced: %+v", **cursor)
	}
}

// The scheduled inventory job reports new sessions, even with the heartbeat itself turned off; a
// manual heartbeat does not.
func TestScheduledInventoryJobRecordsLiveTraces(t *testing.T) {
	for _, scheduled := range []bool{true, false} {
		testenv.SetHome(t, t.TempDir())
		dir := t.TempDir()
		logPath := filepath.Join(dir, "runtime.jsonl")
		configPath := filepath.Join(dir, "config.json")
		disabled := false
		data, err := json.Marshal(map[string]any{"log_path": logPath, "user_mode": true, "inventory_heartbeat": map[string]any{"enabled": &disabled}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		cursor, calls := stubLiveTraces(t, true, false)
		start := liveTraceEndCursor(logPath)
		*cursor = &start
		appendLog(t, logPath, liveLine("claude_code", "hook", "s1", "prompt.submitted"))

		oldOpts := endpointOpts
		t.Cleanup(func() { endpointOpts = oldOpts })
		endpointOpts.userMode, endpointOpts.systemMode = true, false
		endpointOpts.inventoryScheduled = scheduled
		endpointOpts.inventoryHeartbeatConfig = configPath
		if err := runEndpointInventoryHeartbeat(&cobra.Command{}, nil); err != nil {
			t.Fatal(err)
		}
		var want []string
		if scheduled {
			want = []string{"live:claude_code/s1"}
		}
		if !reflect.DeepEqual(*calls, want) {
			t.Fatalf("scheduled=%t: recorded %v, want %v", scheduled, *calls, want)
		}
	}
}

// Nothing about a session but its harness and ID leaves the log reader.
func TestLiveSessionsCarryNoContent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	cursor := liveTraceEndCursor(logPath)
	appendLog(t, logPath, liveLine("claude_code", "hook", "s1", "prompt.submitted"))
	sessions, next := liveSessionsSince(logPath, cursor, liveTraceScanBytes)
	data, _ := json.Marshal(struct {
		S []sweptSession
		N telemetry.LogCursor
	}{sessions, next})
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), filepath.Base(filepath.Dir(logPath))) {
		t.Fatalf("leaked: %s", data)
	}
}
