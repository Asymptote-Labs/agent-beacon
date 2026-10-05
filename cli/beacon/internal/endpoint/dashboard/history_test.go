package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// Every test in this package runs with the local history pointed at a path that does not exist,
// so nothing here can read or write a developer's real ~/.beacon/endpoint/history.db. Tests that
// want a history opt in with withHistory.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "beacon-dashboard-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv(endpointconfig.HistoryStoreEnv, filepath.Join(dir, "absent", "history.db"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// withHistory points the local history at a fresh path for the rest of the test. The store
// itself is created by the first ReindexTraceStore.
func withHistory(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.db")
	t.Setenv(endpointconfig.HistoryStoreEnv, path)
	return path
}

// withoutHistory makes every trace read in the rest of the test take the JSONL scan.
func withoutHistory(t *testing.T) {
	t.Helper()
	t.Setenv(endpointconfig.HistoryStoreEnv, filepath.Join(t.TempDir(), "absent", "history.db"))
}

func newTestLog(t *testing.T, lines [][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "logs", "runtime.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	writeTestLog(t, path, lines...)
	return path
}

// optIn builds the history from a log. Fixtures are dated in the past, so retention is set far
// beyond them; the retention test sets its own.
func optIn(t *testing.T, path string) {
	t.Helper()
	if _, err := EnableHistoryStore(path, HistoryOptions{RetentionDays: 100 * 365}); err != nil {
		t.Fatalf("EnableHistoryStore: %v", err)
	}
}

// traceStoreIndexedLog writes lines to a new log and opts the test in to a history built from it.
func traceStoreIndexedLog(t *testing.T, lines [][]byte) string {
	t.Helper()
	path := newTestLog(t, lines)
	withHistory(t)
	optIn(t, path)
	return path
}

// traceStoreBlockedLog writes lines to a new log that only the JSONL scan will read.
func traceStoreBlockedLog(t *testing.T, lines [][]byte) string {
	t.Helper()
	withoutHistory(t)
	return newTestLog(t, lines)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// traceAnswers runs a fixed set of list, search and show queries against a log and returns their
// JSON, keyed by query, so two ways of answering can be compared byte for byte.
func traceAnswers(t *testing.T, path string, showIDs []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	queries := []string{
		"", "index", "INDEX LOCAL", "go", "go test", "dashboard go", "endpoint.dashboard", "endpoint/dashboard",
		"(", `"quoted"`, "a-b", "path:/repo", "*", "AND", "NEAR", "ü", "naïve café", "claude_code.token.usage",
		"nothing-matches-this", "  ", "evt", "s1", "codex_cli", "retained answer", "no-near enforce",
	}
	for _, q := range queries {
		for _, page := range []int{1, 2} {
			list, err := ReadTraceList(path, TraceQuery{EventQuery: EventQuery{Q: q}, Limit: 3, Page: page})
			if err != nil {
				t.Fatalf("ReadTraceList(%q): %v", q, err)
			}
			out[fmt.Sprintf("list q=%q page=%d", q, page)] = mustJSON(t, list)
		}
		for _, level := range []string{"trace", "event"} {
			search, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: q}, ResultLevel: level, Limit: 4})
			if err != nil {
				t.Fatalf("SearchTraces(%q, %s): %v", q, level, err)
			}
			out[fmt.Sprintf("search q=%q level=%s", q, level)] = mustJSON(t, search)
		}
		typed, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: q}, ResultLevel: "event", EventTypes: []string{"command", "agent_text"}, Limit: 50})
		if err != nil {
			t.Fatalf("SearchTraces typed(%q): %v", q, err)
		}
		out[fmt.Sprintf("search q=%q typed", q)] = mustJSON(t, typed)
	}
	for _, state := range []string{"local_only", "LOCAL_ONLY", "shared"} {
		list, err := ReadTraceList(path, TraceQuery{State: state, Limit: 50})
		if err != nil {
			t.Fatalf("ReadTraceList state=%s: %v", state, err)
		}
		out["list state="+state] = mustJSON(t, list)
	}
	for _, id := range showIDs {
		for name, query := range map[string]TraceQuery{
			"all":      {Limit: 100},
			"page":     {Limit: 2, Offset: 2},
			"past-end": {Limit: 5, Offset: 999},
			"typed":    {Limit: 100, EventTypes: []string{"user_message", "command"}},
			"around":   {AroundEvent: 3, Before: 1, After: 1},
			"agent":    {Limit: 100, EventTypes: []string{"agent_text"}},
			// Lens data reads a whole trace in one call.
			"no-limit":        {EventQuery: EventQuery{NoLimit: true}},
			"no-limit-offset": {EventQuery: EventQuery{NoLimit: true}, Offset: 2},
		} {
			show, ok, err := ShowTrace(path, id, query)
			if err != nil {
				t.Fatalf("ShowTrace(%s, %s): %v", id, name, err)
			}
			out[fmt.Sprintf("show %s %s ok=%v", id, name, ok)] = mustJSON(t, show)
		}
	}
	return out
}

func compareAnswers(t *testing.T, want, got map[string]string) {
	t.Helper()
	for key, w := range want {
		if g, ok := got[key]; !ok || g != w {
			t.Errorf("%s differs\n want %s\n  got %s", key, w, g)
		}
	}
}

// parityFixture is a log with the shapes the projection treats differently: hook and OTLP
// captures, spans, commands with output, file edits, MCP calls, approvals, token usage, metric
// samples the dashboard renames, retained assistant text, sharing metadata in raw, events that
// share an ID, events with no ID, and text in several scripts.
func parityFixture() [][]byte {
	lines := []string{
		`{"timestamp":"2026-06-11T10:00:00Z","event":{"id":"e1","action":"session.started","category":"session"},"harness":{"name":"cursor","collection_method":"hook"},"session":{"id":"s1","working_directory":"/repo/a"},"repository":"repo-a","branch":"main","message":"Session started"}`,
		`{"timestamp":"2026-06-11T10:00:05Z","event":{"id":"e2","action":"prompt.submitted","category":"prompt"},"harness":{"name":"cursor","collection_method":"hook"},"session":{"id":"s1"},"prompt":{"text":"Index local traces for the dashboard, naïve café ü"},"content":{"retention":"full","included":true,"hash":"abc","bytes":40}}`,
		`{"timestamp":"2026-06-11T10:01:00Z","event":{"id":"e3","action":"command.executed","category":"command"},"harness":{"name":"cursor","collection_method":"hook"},"session":{"id":"s1"},"tool":{"name":"Shell","command":"go test ./internal/endpoint/dashboard"},"command":{"command":"go test ./internal/endpoint/dashboard","exit_code":1,"duration_ms":1200,"output":"FAIL a-b path:/repo \"quoted\""}}`,
		`{"timestamp":"2026-06-11T10:01:30Z","event":{"id":"e3","action":"command.executed","category":"command"},"harness":{"name":"cursor","collection_method":"otlp"},"session":{"id":"s1"},"command":{"command":"go test ./internal/endpoint/dashboard","exit_code":0}}`,
		`{"timestamp":"2026-06-11T10:02:00Z","event":{"id":"e4","action":"file.modified","category":"file"},"harness":{"name":"cursor"},"session":{"id":"s1"},"file":{"path":"internal/endpoint/dashboard/traces.go","operation":"modify","language":"go","diff":"+ matchesAllTerms","diff_hash":"dh"}}`,
		`{"timestamp":"2026-06-11T10:02:30Z","event":{"id":"e5","action":"approval.denied","category":"approval"},"harness":{"name":"cursor"},"session":{"id":"s1"},"approval":{"required":true,"decision":"denied","reason":"AND NEAR * not allowed"},"policy":{"id":"no-near","decision":"deny","enforcement":"enforce","reason":"provider denied"}}`,
		`{"timestamp":"2026-06-11T10:03:00Z","event":{"id":"e6","action":"mcp.tool_invoked","category":"mcp"},"harness":{"name":"cursor"},"session":{"id":"s1"},"mcp":{"server":"beacon","tool":"search_sessions","method":{"name":"tools/call"}},"gen_ai":{"tool":{"name":"mcp__beacon__search_sessions","call":{"id":"call-1","arguments":{"query":"index"},"result":"one session"}}}}`,
		`{"timestamp":"2026-06-11T10:04:00Z","event":{"action":"session.activity","category":"session"},"harness":{"name":"cursor"},"session":{"id":"s1"},"message":"hook with no event id"}`,
		`{"timestamp":"2026-06-11T11:00:00Z","event":{"id":"o1","action":"tool.invoked","category":"tool"},"harness":{"name":"claude_code","collection_method":"otlp"},"session":{"id":"s2"},"trace":{"id":"t-abc","span_id":"span-1"},"tool":{"name":"Bash"},"message":"root span","model":"claude-opus-5"}`,
		`{"timestamp":"2026-06-11T11:00:01Z","event":{"id":"o2","action":"tool.completed","category":"tool"},"harness":{"name":"claude_code","collection_method":"otlp"},"session":{"id":"s2"},"trace":{"id":"t-abc","span_id":"span-2","parent_span_id":"span-1"},"tool":{"name":"Bash"}}`,
		`{"timestamp":"2026-06-11T11:00:02Z","event":{"id":"o3","action":"token.usage","category":"metric"},"harness":{"name":"claude_code","collection_method":"otlp"},"session":{"id":"s2"},"trace":{"id":"t-abc","span_id":"span-2","parent_span_id":"span-1"},"gen_ai":{"usage":{"input_tokens":120,"output_tokens":30,"cache_read":{"input_tokens":5},"cost_usd":0.0123}},"model":"claude-opus-5"}`,
		`{"timestamp":"2026-06-11T11:00:03Z","event":{"id":"m1","action":"","category":""},"harness":{"name":"claude_code","collection_method":"otlp"},"message":"claude_code.token.usage","raw":{"metric_name":"claude_code.token.usage","otel_signal":"metrics"}}`,
		`{"timestamp":"2026-06-11T11:00:04Z","event":{"id":"m2","action":"metric.observed","category":"metric"},"harness":{"name":"claude_code","collection_method":"otlp"},"message":"claude_code.active_time.total"}`,
		`{"timestamp":"2026-06-11T12:00:00Z","event":{"id":"c1","action":"prompt.submitted","category":"prompt"},"harness":{"name":"codex_cli","collection_method":"hook"},"session":{"id":"s3"},"prompt":{"text":"rotate the flaky fixture"}}`,
		`{"timestamp":"2026-06-11T12:00:10Z","event":{"id":"c2","action":"agent.message","category":"agent"},"harness":{"name":"codex_cli","collection_method":"hook"},"session":{"id":"s3"},"message":"Codex assistant message","gen_ai":{"output":{"messages":[{"role":"assistant","parts":[{"type":"text","content":"the retained answer"},{"type":"reasoning","content":"hidden thought"}]}]}}}`,
		`{"timestamp":"2026-06-11T12:00:11Z","event":{"id":"c3","action":"agent.reasoning","category":"agent"},"harness":{"name":"codex_cli"},"session":{"id":"s3"},"gen_ai":{"output":{"messages":"bare string output"}}}`,
		`{"timestamp":"2026-06-11T12:00:12Z","event":{"id":"c4","action":"session.status","category":"session"},"harness":{"name":"codex_cli"},"session":{"id":"s3"},"raw":{"shared_url":"https://example.test/s3","visibility":"team","remote_event_count":9,"namespace_slug":"acme"}}`,
		`{"timestamp":"not-a-time","event":{"id":"x1","action":"prompt.submitted","category":"prompt"},"harness":{"name":"opencode"},"session":{"id":"s4"},"prompt":{"text":"日本語のプロンプト index"}}`,
		`{"timestamp":"2026-06-11T13:00:00Z","event":{"action":"prompt.submitted","category":"prompt"},"harness":{"name":"opencode"},"session":{"id":"s5"},"prompt":{"text":"no id at all"}}`,
		`not json at all`,
		``,
		// Out of order, as an OpenTelemetry batch lands after the hook events it precedes: this
		// event becomes the first of session s1, which changes the trace's start, its session
		// working directory and its harness version.
		`{"timestamp":"2026-06-11T09:59:59Z","event":{"id":"late1","action":"session.activity","category":"session"},"harness":{"name":"cursor","version":"9.9.9","collection_method":"otlp"},"session":{"id":"s1","working_directory":"/late/first"},"message":"arrived last, happened first"}`,
		// Equal timestamps: unsequenced first, then by sequence, then by position.
		`{"timestamp":"2026-06-11T14:00:00Z","sequence":7,"event":{"id":"q7","action":"tool.invoked","category":"tool"},"harness":{"name":"codex_cli"},"session":{"id":"s6"},"tool":{"name":"seven"}}`,
		`{"timestamp":"2026-06-11T14:00:00Z","sequence":3,"event":{"id":"q3","action":"tool.invoked","category":"tool"},"harness":{"name":"codex_cli"},"session":{"id":"s6"},"tool":{"name":"three"}}`,
		`{"timestamp":"2026-06-11T14:00:00Z","event":{"id":"q0","action":"prompt.submitted","category":"prompt"},"harness":{"name":"codex_cli"},"session":{"id":"s6"},"prompt":{"text":"unsequenced goes first"}}`,
		`{"timestamp":"2026-06-11T13:59:00Z","event":{"id":"q-early","action":"session.started","category":"session"},"harness":{"name":"codex_cli","version":"1.0"},"session":{"id":"s6"}}`,
		`{"timestamp":"2026-06-11T13:00:05Z","event":{"id":"s5b","action":"command.executed","category":"command"},"harness":{"name":"opencode"},"session":{"id":"s5"},"command":{"command":"ls a-b"}}`,
	}
	out := make([][]byte, len(lines))
	for i, line := range lines {
		out[i] = []byte(line)
	}
	return out
}

var parityShowIDs = []string{
	"session:cursor:s1", "trace:t-abc", "session:codex_cli:s3", "session:opencode:s4", "session:opencode:s5",
	"session:codex_cli:s6", "event:m1", "missing",
}

// The core promise: for the same log, the history answers every list, search and show exactly
// as the JSONL scan does.
func TestHistoryAnswersExactlyLikeTheJSONLScan(t *testing.T) {
	path := newTestLog(t, parityFixture())
	withoutHistory(t)
	want := traceAnswers(t, path, parityShowIDs)

	withHistory(t)
	optIn(t, path)
	compareAnswers(t, want, traceAnswers(t, path, parityShowIDs))
}

func TestHistoryIsNotCreatedWithoutOptIn(t *testing.T) {
	path := newTestLog(t, parityFixture())
	store := endpointconfig.HistoryStorePath()
	if _, err := ReadTraceList(path, TraceQuery{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: "index"}, Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ShowTrace(path, "session:cursor:s1", TraceQuery{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	status, err := TraceStoreStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled {
		t.Fatalf("status = %#v, want a disabled history", status)
	}
	if HistoryStoreEnabled() {
		t.Fatal("HistoryStoreEnabled reported a store nobody opted in to")
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("history store exists after reads without opt-in: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(path)), "traces.db")); !os.IsNotExist(err) {
		t.Fatalf("a traces.db cache was created: %v", err)
	}
}

func TestHistoryIndexesAndCatchesUp(t *testing.T) {
	prompt := testSchemaEvent("2026-06-11T10:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-a")
	prompt.Event.ID = "evt-prompt"
	prompt.Session = &schema.SessionInfo{ID: "s1", WorkingDirectory: "/repo/a"}
	prompt.Prompt = &schema.PromptInfo{Text: "Index local traces"}
	command := testSchemaEvent("2026-06-11T10:01:00Z", "cursor", "command.executed", "command", "repo-a")
	command.Event.ID = "evt-command"
	command.Session = prompt.Session
	command.Command = &schema.CommandInfo{Command: "go test ./internal/endpoint/dashboard"}
	path := traceStoreIndexedLog(t, marshalEvents(t, prompt, command))

	status, err := TraceStoreStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || status.Path != endpointconfig.HistoryStorePath() || status.Source != traceSourceKey(path) {
		t.Fatalf("status = %#v", status)
	}
	if status.Traces != 1 || status.Events != 2 || status.IndexRows != 3 || status.SizeBytes == 0 {
		t.Fatalf("status = %#v, want 1 trace, 2 events, 3 index rows", status)
	}
	if status.RetentionDays != 100*365 || status.MaxBytes != defaultHistoryMaxBytes {
		t.Fatalf("status settings = %#v", status)
	}
	results, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: "dashboard"}, ResultLevel: "event", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if results.TotalMatched != 1 || results.Events[0].Event.ID != "evt-command" {
		t.Fatalf("search = %#v", results)
	}

	later := testSchemaEvent("2026-06-11T10:02:00Z", "cursor", "file.modified", "file", "repo-a")
	later.Event.ID = "evt-file"
	later.Session = prompt.Session
	later.File = &schema.FileInfo{Path: "history_store.go", Operation: "modify"}
	writeTestLog(t, path, marshalEvents(t, prompt, command, later)...)

	status, err = TraceStoreStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if status.Events != 3 || status.IndexRows != 4 {
		t.Fatalf("after append status = %#v, want 3 events and 4 index rows", status)
	}
}

// What the store is for: the log rotates away, the history does not.
func TestHistoryKeepsTracesAfterTheLogIsGone(t *testing.T) {
	path := traceStoreIndexedLog(t, parityFixture())
	for _, archive := range []string{"", ".1", ".2"} {
		_ = os.Remove(path + archive)
	}
	list, err := ReadTraceList(path, TraceQuery{EventQuery: EventQuery{Q: "retained answer"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if list.TotalMatched != 1 || list.Traces[0].ID != "session:codex_cli:s3" {
		t.Fatalf("list after the log was removed = %#v", list)
	}
	show, ok, err := ShowTrace(path, "session:cursor:s1", TraceQuery{Limit: 100})
	if err != nil || !ok || len(show.Events) == 0 {
		t.Fatalf("show after the log was removed ok=%v err=%v events=%d", ok, err, len(show.Events))
	}
}

func TestHistorySearchesLongEventTextInFull(t *testing.T) {
	needle := "needle-after-sixteen-kilobytes"
	prompt := testSchemaEvent("2026-06-11T10:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-a")
	prompt.Event.ID = "evt-prompt"
	prompt.Session = &schema.SessionInfo{ID: "s1"}
	prompt.Prompt = &schema.PromptInfo{Text: strings.Repeat("a", 20*1024) + needle}
	path := traceStoreIndexedLog(t, marshalEvents(t, prompt))
	_ = os.Remove(path) // only the store can answer now
	list, err := ReadTraceList(path, TraceQuery{EventQuery: EventQuery{Q: needle}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if list.TotalMatched != 1 {
		t.Fatalf("matched %d traces, want the one whose text holds the needle past 16KB", list.TotalMatched)
	}
}

func TestHistoryKeepsEventsThatShareAnID(t *testing.T) {
	first := testSchemaEvent("2026-06-11T10:00:00Z", "cursor", "command.executed", "command", "repo-a")
	first.Event.ID = "same-event-id"
	first.Session = &schema.SessionInfo{ID: "s1"}
	first.Command = &schema.CommandInfo{Command: "go build ./..."}
	second := first
	second.Timestamp = "2026-06-11T10:00:01Z"
	path := traceStoreIndexedLog(t, marshalEvents(t, first, second))
	status, err := TraceStoreStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if status.Traces != 1 || status.Events != 2 {
		t.Fatalf("status = %#v, want 1 trace and 2 events", status)
	}
	show, ok, err := ShowTrace(path, "session:cursor:s1", TraceQuery{Limit: 10})
	if err != nil || !ok || len(show.Events) != 2 {
		t.Fatalf("ShowTrace ok=%v err=%v events=%d", ok, err, len(show.Events))
	}
}

// Two logs can describe the same session. Each is its own source.
func TestHistoryScopesTracesBySourceLog(t *testing.T) {
	withHistory(t)
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	shared := testSchemaEvent("2026-06-11T10:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-a")
	shared.Event.ID = "evt-prompt"
	shared.Session = &schema.SessionInfo{ID: "shared-session"}
	shared.Prompt = &schema.PromptInfo{Text: "first log"}
	other := shared
	other.Prompt = &schema.PromptInfo{Text: "second log"}
	first := filepath.Join(dir, "runtime.jsonl")
	second := filepath.Join(dir, "archive.jsonl")
	writeTestLog(t, first, marshalEvents(t, shared)...)
	writeTestLog(t, second, marshalEvents(t, other)...)
	if TraceStorePath(first) != TraceStorePath(second) {
		t.Fatal("both logs should share one history")
	}
	for _, tc := range []struct{ path, want, not string }{{first, "first log", "second log"}, {second, "second log", "first log"}} {
		optIn(t, tc.path)
		status, err := TraceStoreStatus(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if status.Traces != 1 || status.Events != 1 {
			t.Fatalf("status for %s = %#v", tc.path, status)
		}
		for q, want := range map[string]int{tc.want: 1, tc.not: 0} {
			result, err := SearchTraces(tc.path, TraceQuery{EventQuery: EventQuery{Q: q}, ResultLevel: "event", Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if result.TotalMatched != want {
				t.Fatalf("searching %s for %q matched %d, want %d", tc.path, q, result.TotalMatched, want)
			}
		}
	}
}

func TestHistoryTreatsEquivalentLogPathsAsOneSource(t *testing.T) {
	path := traceStoreIndexedLog(t, parityFixture())
	before, err := TraceStoreStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := TraceStoreStatus(filepath.Join(filepath.Dir(path), ".", "runtime.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Traces != before.Traces || after.Events != before.Events || after.Source != before.Source {
		t.Fatalf("equivalent path status = %#v, want %#v", after, before)
	}
}

// rotatingLog writes every event to a log that rotates at rotateSize and to an unrotated shadow
// log holding every line ever written, so the history over the first can be compared with a JSONL
// scan over the second.
type rotatingLog struct {
	path, shadow string
	rotateSize   int64
	n            int
}

func newRotatingLog(t *testing.T, rotateSize int64) *rotatingLog {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"logs", "shadow"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return &rotatingLog{path: filepath.Join(dir, "logs", "runtime.jsonl"), shadow: filepath.Join(dir, "shadow", "runtime.jsonl"), rotateSize: rotateSize}
}

// append writes n distinct events. Distinct matters: the writer drops a repeat of a recent event,
// judged against the tail of each file, and the two files' tails differ after a rotation.
func (r *rotatingLog) append(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		r.n++
		// Every third event is stamped a few seconds before the ones already written, as an
		// OpenTelemetry batch is.
		at := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC).Add(time.Duration(r.n) * time.Second)
		if r.n%3 == 0 {
			at = at.Add(-7 * time.Second)
		}
		event := testSchemaEvent(at.Format(time.RFC3339), "cursor", "command.executed", "command", "repo-a")
		event.Session = &schema.SessionInfo{ID: fmt.Sprintf("s%d", r.n%4)}
		event.Command = &schema.CommandInfo{Command: fmt.Sprintf("echo event-%04d %s", r.n, strings.Repeat("x", 120))}
		for _, opts := range []writer.Options{{Path: r.path, RotateSize: r.rotateSize}, {Path: r.shadow, RotateSize: 1 << 40}} {
			if _, err := writer.AppendEvent(event, opts); err != nil {
				t.Fatalf("append: %v", err)
			}
		}
	}
}

// appendRotations appends events until the log has rotated exactly n more times. A rotation is
// seen as the live file shrinking: it only grows until the writer renames it away. (os.SameFile
// cannot tell here on Windows, where it resolves both paths' file IDs when it compares them, after
// the rename, and so always reports the same file.)
func (r *rotatingLog) appendRotations(t *testing.T, n int) {
	t.Helper()
	for rotated, appended := 0, 0; rotated < n; appended++ {
		if appended > 10000 {
			t.Fatalf("log rotated %d times in %d appends, want %d", rotated, appended, n)
		}
		before, beforeErr := os.Stat(r.path)
		r.append(t, 1)
		after, afterErr := os.Stat(r.path)
		if beforeErr == nil && afterErr == nil && after.Size() < before.Size() {
			rotated++
		}
	}
}

func (r *rotatingLog) compare(t *testing.T) {
	t.Helper()
	ids := []string{"session:cursor:s0", "session:cursor:s1", "session:cursor:s2", "session:cursor:s3"}
	history := traceAnswers(t, r.path, ids)
	previous := os.Getenv(endpointconfig.HistoryStoreEnv)
	withoutHistory(t)
	want := traceAnswers(t, r.shadow, ids)
	t.Setenv(endpointconfig.HistoryStoreEnv, previous)
	compareAnswers(t, want, history)
}

func TestHistoryFollowsTheLogAcrossRotation(t *testing.T) {
	withHistory(t)
	log := newRotatingLog(t, 4*1024)
	log.append(t, 5)
	optIn(t, log.path)
	// One rotation, then three, then five between catch-ups: never more than the five archives
	// the writer keeps, so nothing is lost.
	for _, rotations := range []int{1, 3, 5} {
		log.appendRotations(t, rotations)
		log.append(t, 2)
		if _, err := ReadTraceList(log.path, TraceQuery{Limit: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(log.path + ".5"); err != nil {
		t.Fatalf("the log never filled all five archives: %v", err)
	}
	log.compare(t)
	status, err := TraceStoreStatus(log.path)
	if err != nil {
		t.Fatal(err)
	}
	if status.Events != log.n || status.Gaps != 0 || status.GapBytes != 0 {
		t.Fatalf("status = %#v, want %d events and no gap", status, log.n)
	}
}

func TestHistoryRecordsAGapWhenTheLogRotatesPastIt(t *testing.T) {
	withHistory(t)
	log := newRotatingLog(t, 2*1024)
	log.append(t, 3)
	optIn(t, log.path)
	// Seven rotations with no catch-up: more than the five archives, so files are deleted unread.
	log.appendRotations(t, 7)
	status, err := TraceStoreStatus(log.path)
	if err != nil {
		t.Fatal(err)
	}
	if status.Gaps == 0 {
		t.Fatalf("status = %#v, want a recorded gap", status)
	}
	if status.Events >= log.n {
		t.Fatalf("history holds %d of %d events after files rotated away unread", status.Events, log.n)
	}
	// Nothing read twice: every stored event is distinct.
	show, ok, err := ShowTrace(log.path, "session:cursor:s1", TraceQuery{Limit: 1000})
	if err != nil || !ok {
		t.Fatalf("ShowTrace ok=%v err=%v", ok, err)
	}
	seen := map[string]bool{}
	for _, event := range show.Events {
		if seen[event.ID] {
			t.Fatalf("event %s stored twice", event.ID)
		}
		seen[event.ID] = true
	}
}

func TestHistoryWaitsForAPartialLine(t *testing.T) {
	withHistory(t)
	lines := parityFixture()
	path := newTestLog(t, lines[:2])
	partial := lines[2][:40]
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(partial); err != nil {
		t.Fatal(err)
	}
	optIn(t, path)
	status, err := TraceStoreStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if status.Events != 2 {
		t.Fatalf("events = %d, want the two complete lines only", status.Events)
	}
	if _, err := f.Write(append(lines[2][40:], '\n')); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	status, err = TraceStoreStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if status.Events != 3 {
		t.Fatalf("events = %d, want the completed line read once", status.Events)
	}
}

func TestHistoryTreatsAReplacedOrShrunkFileAsNew(t *testing.T) {
	lines := parityFixture()
	path := traceStoreIndexedLog(t, lines[:5])
	before, err := TraceStoreStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	// Replaced: a new file at the same path, whose first line differs.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeTestLog(t, path, lines[13:15]...)
	after, err := TraceStoreStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Events != before.Events+2 {
		t.Fatalf("after replacement events = %d, want %d", after.Events, before.Events+2)
	}
	// Shrunk in place: shorter than what was read from it.
	writeTestLog(t, path, lines[16])
	shrunk, err := TraceStoreStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if shrunk.Events != after.Events+1 {
		t.Fatalf("after shrink events = %d, want %d", shrunk.Events, after.Events+1)
	}
}

// Readers and a writer at once, as the MCP server, the CLI and the agents' hooks will be.
func TestHistoryConcurrentWriterAndReaders(t *testing.T) {
	withHistory(t)
	log := newRotatingLog(t, 32*1024)
	log.append(t, 5)
	optIn(t, log.path)
	done := make(chan struct{})
	var wg sync.WaitGroup
	for reader := 0; reader < 3; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				if _, err := ReadTraceList(log.path, TraceQuery{EventQuery: EventQuery{Q: "event"}, Limit: 5}); err != nil {
					t.Errorf("reader: %v", err)
					return
				}
			}
		}()
	}
	log.append(t, 250)
	close(done)
	wg.Wait()
	log.compare(t)
}

func TestHistoryPrunesByRetentionAndSize(t *testing.T) {
	withHistory(t)
	var events []schema.Event
	old := testSchemaEvent("2020-01-01T00:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-a")
	old.Event.ID = "old"
	old.Session = &schema.SessionInfo{ID: "old-session"}
	old.Prompt = &schema.PromptInfo{Text: "an old prompt"}
	events = append(events, old)
	now := time.Now().UTC()
	for i := 0; i < 60; i++ {
		event := testSchemaEvent(now.Add(time.Duration(i-60)*time.Minute).Format(time.RFC3339), "cursor", "prompt.submitted", "prompt", "repo-a")
		event.Event.ID = fmt.Sprintf("recent-%d", i)
		event.Session = &schema.SessionInfo{ID: fmt.Sprintf("recent-%d", i)}
		event.Prompt = &schema.PromptInfo{Text: fmt.Sprintf("recent prompt %d %s", i, noiseText(i, 3000))}
		events = append(events, event)
	}
	path := newTestLog(t, marshalEvents(t, events...))
	status, err := EnableHistoryStore(path, HistoryOptions{RetentionDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if status.Traces != 60 {
		t.Fatalf("after retention traces = %d, want the 60 recent ones", status.Traces)
	}
	if _, ok, _ := ShowTrace(path, "session:cursor:old-session", TraceQuery{Limit: 10}); ok {
		t.Fatal("a trace past retention is still stored")
	}

	status, err = EnableHistoryStore(path, HistoryOptions{MaxBytes: 256 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	if status.Traces == 0 || status.Traces >= 60 {
		t.Fatalf("after the size cap traces = %d, want some but not all", status.Traces)
	}
	if status.SizeBytes > 256*1024+64*1024 {
		t.Fatalf("store is %d bytes after pruning to a 256KiB cap", status.SizeBytes)
	}
	// The newest trace survives; the oldest recent one does not.
	if _, ok, _ := ShowTrace(path, "session:cursor:recent-59", TraceQuery{Limit: 1}); !ok {
		t.Fatal("the newest trace was pruned")
	}
	if _, ok, _ := ShowTrace(path, "session:cursor:recent-0", TraceQuery{Limit: 1}); ok {
		t.Fatal("the oldest trace survived the size cap")
	}
	// Search stays consistent with what remains.
	result, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: "recent prompt"}, ResultLevel: "event", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalMatched != status.Traces {
		t.Fatalf("search matched %d events, want one per remaining trace (%d)", result.TotalMatched, status.Traces)
	}
}

// A new Beacon version that projects events differently rebuilds what it derives from the stored
// lines rather than serving what the old one derived.
func TestHistoryRebuildsDerivedDataForANewProjection(t *testing.T) {
	path := traceStoreIndexedLog(t, parityFixture())
	withoutHistoryPath := os.Getenv(endpointconfig.HistoryStoreEnv)
	want, _, err := ShowTrace(path, "session:codex_cli:s3", TraceQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	store, err := openHistoryStore(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE traces SET aggregate = replace(aggregate, '"title":"rotate the flaky fixture"', '"title":"stale title"')`,
		`UPDATE events SET event_type = 'other', hay = 'stale'`,
		`UPDATE meta SET value = '0' WHERE key = 'derivation_version'`,
	} {
		if _, err := store.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	store.close()
	if os.Getenv(endpointconfig.HistoryStoreEnv) != withoutHistoryPath {
		t.Fatal("history path changed")
	}
	got, ok, err := ShowTrace(path, "session:codex_cli:s3", TraceQuery{Limit: 100})
	if err != nil || !ok {
		t.Fatalf("ShowTrace ok=%v err=%v", ok, err)
	}
	if mustJSON(t, got) != mustJSON(t, want) {
		t.Fatalf("after rebuild\n got %s\nwant %s", mustJSON(t, got), mustJSON(t, want))
	}
	result, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: "retained answer"}, ResultLevel: "event", Limit: 10})
	if err != nil || result.TotalMatched != 1 {
		t.Fatalf("search after rebuild = %#v err=%v", result, err)
	}
}

func TestHistoryResetRemovesTheStore(t *testing.T) {
	path := traceStoreIndexedLog(t, parityFixture())
	if !HistoryStoreEnabled() {
		t.Fatal("history not enabled after reindex")
	}
	if err := ResetHistoryStore(); err != nil {
		t.Fatal(err)
	}
	if HistoryStoreEnabled() {
		t.Fatal("history still enabled after reset")
	}
	status, err := TraceStoreStatus(path)
	if err != nil || status.Enabled {
		t.Fatalf("status after reset = %#v err=%v", status, err)
	}
}

func TestHistoryStoreIsPrivateToItsOwner(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	traceStoreIndexedLog(t, parityFixture())
	info, err := os.Stat(endpointconfig.HistoryStorePath())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("history store mode = %o, want 600", mode)
	}
}

func TestHistoryRemovesTheLegacyTraceCache(t *testing.T) {
	path := newTestLog(t, parityFixture())
	legacy := filepath.Join(filepath.Dir(filepath.Dir(path)), "traces.db")
	if err := os.WriteFile(legacy, []byte("old cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	withHistory(t)
	optIn(t, path)
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("traces.db still present after opting in: %v", err)
	}
}

// On Windows a rename fails while any handle lacks FILE_SHARE_DELETE. A catch-up holding a log
// file must not stop the writer rotating it.
func TestOpenLogFileAllowsTheWriterToRotate(t *testing.T) {
	path := newTestLog(t, parityFixture())
	f, err := openLogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("rename with the log open for reading: %v", err)
	}
}

// noiseText is deterministic text without repeats, so its search index is as large as real text's.
func noiseText(seed, n int) string {
	var b strings.Builder
	sum := sha256.Sum256([]byte(fmt.Sprint(seed)))
	for b.Len() < n {
		sum = sha256.Sum256(sum[:])
		b.WriteString(hex.EncodeToString(sum[:]))
		b.WriteByte(' ')
	}
	return b.String()[:n]
}

// The store keeps only the raw keys in historyRawKeys. A raw key the dashboard reads and the store
// drops would make a rebuilt history differ from the log, so every one must be listed.
func TestHistoryKeepsEveryRawKeyTheDashboardReads(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`(?:rawString\([^,]+, |intFromRaw\([^,]+, |Raw\[)"([^"]+)"`)
	found := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(data), -1) {
			found++
			if !historyRawKeys[match[1]] {
				t.Errorf("%s reads raw key %q, which the history store drops; add it to historyRawKeys", file, match[1])
			}
		}
	}
	if found < 10 {
		t.Fatalf("found %d raw key reads; the pattern no longer matches the code", found)
	}
}

func TestStoredLineKeepsEverythingButUnreadRawKeys(t *testing.T) {
	line := []byte(`{"timestamp":"t","future_field":{"a":1},"raw":{"metric_name":"m","attributes":{"prompt":"secret"},"shared_url":"u"},"event":{"id":"x"}}`)
	stored, err := storedLine(line)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"event":{"id":"x"},"future_field":{"a":1},"raw":{"metric_name":"m","shared_url":"u"},"timestamp":"t"}`
	if string(stored) != want {
		t.Fatalf("stored line = %s, want %s", stored, want)
	}
	roundTrip, err := decompressLine(compressLine(stored))
	if err != nil || string(roundTrip) != want {
		t.Fatalf("round trip = %s, %v", roundTrip, err)
	}
	onlyUnread, err := storedLine([]byte(`{"raw":{"attributes":{}},"event":{"id":"y"}}`))
	if err != nil || string(onlyUnread) != `{"event":{"id":"y"}}` {
		t.Fatalf("stored line without kept raw keys = %s, %v", onlyUnread, err)
	}
}

// Events that arrive after later ones are folded into their place in the trace, both before the
// first checkpoint and after one, and the trace reads exactly as the JSONL scan reads it.
func TestHistoryRefoldsWhenAnEventArrivesLate(t *testing.T) {
	withHistory(t)
	path := newTestLog(t, nil)
	base := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	var lines [][]byte
	appendEvent := func(at time.Time, id, text string) {
		event := testSchemaEvent(at.Format(time.RFC3339Nano), "claude_code", "prompt.submitted", "prompt", "repo-a")
		event.Event.ID = id
		event.Session = &schema.SessionInfo{ID: "long"}
		event.Prompt = &schema.PromptInfo{Text: text}
		lines = append(lines, marshalEvents(t, event)...)
		writeTestLog(t, path, lines...)
	}
	for i := 0; i < 600; i++ {
		appendEvent(base.Add(time.Duration(i)*time.Second), fmt.Sprintf("in-order-%d", i), fmt.Sprintf("prompt %d", i))
	}
	optIn(t, path)
	check := func(label string) {
		t.Helper()
		ids := []string{"session:claude_code:long"}
		history := traceAnswers(t, path, ids)
		previous := os.Getenv(endpointconfig.HistoryStoreEnv)
		withoutHistory(t)
		want := traceAnswers(t, path, ids)
		t.Setenv(endpointconfig.HistoryStoreEnv, previous)
		for key, w := range want {
			if history[key] != w {
				t.Fatalf("%s: %s differs from the JSONL scan", label, key)
			}
		}
	}
	// Lands after the second checkpoint (event 512).
	appendEvent(base.Add(550*time.Second+time.Millisecond), "late-after-checkpoint", "late near the end")
	check("after a checkpoint")
	// Lands between the first two checkpoints.
	appendEvent(base.Add(300*time.Second+time.Millisecond), "late-mid", "late in the middle")
	check("between checkpoints")
	// Becomes the first event: the trace refolds from its start.
	appendEvent(base.Add(-time.Hour), "late-first", "late and first")
	check("first event")
	show, ok, err := ShowTrace(path, "session:claude_code:long", TraceQuery{Limit: 1})
	if err != nil || !ok || show.Events[0].ID != "late-first" || show.Trace.EventCount != 603 {
		t.Fatalf("show = %#v ok=%v err=%v", show.Range, ok, err)
	}
}

// A retention longer than time.Duration can hold must keep everything, not wrap around and prune
// everything.
func TestHistoryVeryLongRetentionKeepsEverything(t *testing.T) {
	path := newTestLog(t, parityFixture())
	withHistory(t)
	for _, days := range []int{maxHistoryRetentionDays, 365000, 1 << 40} {
		status, err := EnableHistoryStore(path, HistoryOptions{RetentionDays: days})
		if err != nil {
			t.Fatal(err)
		}
		if status.Traces == 0 {
			t.Fatalf("retention of %d days pruned every trace", days)
		}
	}
	if cutoff := retentionCutoff(time.Now(), 1<<40); cutoff != math.MinInt64 {
		t.Fatalf("cutoff for an overflowing retention = %d, want no expiry", cutoff)
	}
}

// An event the writer left without an ID is named by where it sits in the log, and the name
// changes when the file rotates. The store renames it too, so show and search keep matching the
// JSONL path.
func TestHistoryRenamesIDlessEventsWhenTheirFileRotates(t *testing.T) {
	withHistory(t)
	idless := func(ts, session, text string) []byte {
		return []byte(`{"timestamp":"` + ts + `","event":{"action":"prompt.submitted","category":"prompt"},"harness":{"name":"cursor"},"session":{"id":"` + session + `"},"prompt":{"text":"` + text + `"},"trace":{"id":"t-idless","span_id":"sp-` + text + `"}}`)
	}
	path := newTestLog(t, [][]byte{
		idless("2026-06-11T10:00:00Z", "a", "first-idless"),
		[]byte(`{"timestamp":"2026-06-11T10:00:01Z","event":{"id":"with-id","action":"command.executed","category":"command"},"harness":{"name":"cursor"},"session":{"id":"a"},"command":{"command":"ls"}}`),
		idless("2026-06-11T10:00:02Z", "a", "second-idless"),
	})
	optIn(t, path)
	// Rotate: the live file becomes .1 and a new live file starts.
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	writeTestLog(t, path, idless("2026-06-11T10:00:03Z", "a", "third-idless"))
	ids := []string{"trace:t-idless"}
	history := traceAnswers(t, path, ids)
	for _, q := range []string{"line-1", "archive-1-line-3", "archive-1", "line-3"} {
		result, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: q}, ResultLevel: "event", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		history["idless "+q] = mustJSON(t, result)
	}
	previous := os.Getenv(endpointconfig.HistoryStoreEnv)
	withoutHistory(t)
	want := traceAnswers(t, path, ids)
	for _, q := range []string{"line-1", "archive-1-line-3", "archive-1", "line-3"} {
		result, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: q}, ResultLevel: "event", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		want["idless "+q] = mustJSON(t, result)
	}
	t.Setenv(endpointconfig.HistoryStoreEnv, previous)
	compareAnswers(t, want, history)
	if !strings.Contains(want["idless archive-1-line-3"], "archive-1-line-3") {
		t.Fatalf("fixture did not produce a rotated ID: %s", want["idless archive-1-line-3"])
	}
}

// The policy block is part of the trace projection, so both the JSONL scan and the history serve it.
func TestTraceEventsCarryThePolicyBlock(t *testing.T) {
	path := newTestLog(t, parityFixture())
	for name, setup := range map[string]func(){
		"jsonl":   func() { withoutHistory(t) },
		"history": func() { withHistory(t); optIn(t, path) },
	} {
		setup()
		show, ok, err := ShowTrace(path, "session:cursor:s1", TraceQuery{Limit: 100})
		if err != nil || !ok {
			t.Fatalf("%s: ShowTrace = ok %v, err %v", name, ok, err)
		}
		var policy *TracePolicyV1
		for _, event := range show.Events {
			if event.ID == "e5" {
				policy = event.Policy
			}
		}
		want := TracePolicyV1{ID: "no-near", Decision: "deny", Enforcement: "enforce", Reason: "provider denied"}
		if policy == nil || *policy != want {
			t.Fatalf("%s: e5 policy = %#v, want %#v", name, policy, want)
		}
		// The policy block is searchable, like the approval beside it.
		found, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: "no-near"}, ResultLevel: "event", Limit: 10})
		if err != nil || found.TotalMatched != 1 || found.Events[0].Event.ID != "e5" {
			t.Fatalf("%s: search for the policy id = %+v, %v", name, found, err)
		}
	}
}
