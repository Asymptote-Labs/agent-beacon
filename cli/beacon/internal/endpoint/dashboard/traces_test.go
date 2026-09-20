package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestTraceListAggregatesHookSessionsAndOTLPSpans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	prompt := testSchemaEvent("2026-06-11T10:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-a")
	prompt.Event.ID = "evt-prompt"
	prompt.Session = &schema.SessionInfo{ID: "s1", WorkingDirectory: "/repo/a"}
	prompt.Prompt = &schema.PromptInfo{Text: "Build the dashboard trace view"}
	prompt.Content = &schema.ContentInfo{Retention: "full", Included: true, Hash: "prompt-hash", Bytes: 30}
	prompt.Raw = map[string]interface{}{
		"shared_url":           "https://traces.example/t/s1",
		"visibility":           "unlisted",
		"namespace_slug":       "eng",
		"remote_message_count": float64(1),
		"shared_event_count":   float64(2),
	}
	command := testSchemaEvent("2026-06-11T10:01:00Z", "cursor", "command.executed", "command", "repo-a")
	command.Event.ID = "evt-command"
	command.Session = &schema.SessionInfo{ID: "s1", WorkingDirectory: "/repo/a"}
	command.Command = &schema.CommandInfo{Command: "go test ./internal/endpoint/dashboard", DurationMS: 1200}
	command.Content = &schema.ContentInfo{Retention: "metadata", Included: false, Redacted: true}

	span := testSchemaEvent("2026-06-11T11:00:00Z", "asymptote_observe", "tool.invoked", "tool", "repo-b")
	span.Event.ID = "evt-span"
	span.Session = &schema.SessionInfo{ID: "cloud-session", WorkingDirectory: "/repo/b"}
	span.Trace = &schema.TraceInfo{ID: "trace-1", SpanID: "span-root"}
	span.Model = "gpt-4o-mini"
	span.GenAI = &schema.GenAIInfo{
		Usage: &schema.GenAIUsageInfo{InputTokens: int64Ptr(60), OutputTokens: int64Ptr(20)},
		Tool:  &schema.GenAIToolInfo{Name: "shell", Call: &schema.GenAIToolCallInfo{ID: "call-1", Arguments: map[string]interface{}{"cmd": "date"}}},
	}
	child := testSchemaEvent("2026-06-11T11:01:00Z", "asymptote_observe", "tool.completed", "tool", "repo-b")
	child.Event.ID = "evt-child"
	child.Session = span.Session
	child.Trace = &schema.TraceInfo{ID: "trace-1", SpanID: "span-child", ParentSpanID: "span-root"}

	writeTestLog(t, path, marshalEvents(t, prompt, command, span, child)...)

	result, err := ReadTraceList(path, TraceQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ReadTraceList returned error: %v", err)
	}
	if result.TotalMatched != 2 || result.Returned != 2 {
		t.Fatalf("trace totals = matched %d returned %d, want 2/2", result.TotalMatched, result.Returned)
	}
	var sessionTrace, otlpTrace TraceSummaryV1
	for _, trace := range result.Traces {
		switch trace.ID {
		case "session:cursor:s1":
			sessionTrace = trace
		case "trace:trace-1":
			otlpTrace = trace
		}
	}
	if sessionTrace.Title != "Build the dashboard trace view" || sessionTrace.LocalMessageCount != 1 {
		t.Fatalf("session trace summary = %#v", sessionTrace)
	}
	if sessionTrace.Sharing.State != "shared" || sessionTrace.Sharing.Visibility != "unlisted" || sessionTrace.Namespace.Slug != "eng" {
		t.Fatalf("session trace sharing = %#v namespace=%#v", sessionTrace.Sharing, sessionTrace.Namespace)
	}
	if !sessionTrace.Content.HasRedactions || sessionTrace.Content.Retention != "mixed" {
		t.Fatalf("session content summary = %#v, want mixed with redactions", sessionTrace.Content)
	}
	if otlpTrace.Trace == nil || otlpTrace.Trace.ID != "trace-1" || otlpTrace.Trace.RootSpanID != "span-root" {
		t.Fatalf("otlp trace identity = %#v", otlpTrace.Trace)
	}
	if otlpTrace.TokenUsage == nil || otlpTrace.TokenUsage.InputTokens != 60 || otlpTrace.TokenUsage.OutputTokens != 20 {
		t.Fatalf("otlp usage = %#v", otlpTrace.TokenUsage)
	}
}

func TestShowTraceSupportsRangesEventTypeFiltersAndSpans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	first := testSchemaEvent("2026-06-11T10:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-a")
	first.Event.ID = "evt-1"
	first.Session = &schema.SessionInfo{ID: "s1"}
	first.Prompt = &schema.PromptInfo{Text: "Run tests"}
	second := testSchemaEvent("2026-06-11T10:01:00Z", "cursor", "command.executed", "command", "repo-a")
	second.Event.ID = "evt-2"
	second.Session = first.Session
	second.Command = &schema.CommandInfo{Command: "go test ./..."}
	third := testSchemaEvent("2026-06-11T10:02:00Z", "cursor", "file.modified", "file", "repo-a")
	third.Event.ID = "evt-3"
	third.Session = first.Session
	third.File = &schema.FileInfo{Path: "trace.go", Operation: "modify", Diff: "@@ -1 +1 @@"}
	writeTestLog(t, path, marshalEvents(t, first, second, third)...)

	show, ok, err := ShowTrace(path, "session:cursor:s1", TraceQuery{Limit: 1, Offset: 2})
	if err != nil || !ok {
		t.Fatalf("ShowTrace ok=%t err=%v", ok, err)
	}
	if show.Range.TotalEvents != 3 || show.Range.Offset != 2 || show.Range.ReturnedEvents != 1 {
		t.Fatalf("range = %#v", show.Range)
	}
	if len(show.Events) != 1 || show.Events[0].Number != 2 || show.Events[0].Type != "command" {
		t.Fatalf("returned events = %#v", show.Events)
	}

	commands, ok, err := ShowTrace(path, "session:cursor:s1", TraceQuery{Limit: 10, EventTypes: []string{"command"}})
	if err != nil || !ok {
		t.Fatalf("ShowTrace command ok=%t err=%v", ok, err)
	}
	if commands.Range.TotalEvents != 1 || len(commands.Events) != 1 || commands.Events[0].Command.Command != "go test ./..." {
		t.Fatalf("command-filtered trace = range %#v events %#v", commands.Range, commands.Events)
	}
}

func TestSearchTracesSupportsTraceAndEventResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	prompt := testSchemaEvent("2026-06-11T10:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-a")
	prompt.Event.ID = "evt-prompt"
	prompt.Session = &schema.SessionInfo{ID: "s1", WorkingDirectory: "/repo/a"}
	prompt.Prompt = &schema.PromptInfo{Text: "Investigate database migrations"}
	command := testSchemaEvent("2026-06-11T10:01:00Z", "cursor", "command.executed", "command", "repo-a")
	command.Event.ID = "evt-command"
	command.Session = prompt.Session
	command.Command = &schema.CommandInfo{Command: "go test ./migrations"}
	writeTestLog(t, path, marshalEvents(t, prompt, command)...)

	traceResults, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: "database"}, Limit: 10, ResultLevel: "trace"})
	if err != nil {
		t.Fatalf("SearchTraces trace returned error: %v", err)
	}
	if traceResults.TotalMatched != 1 || len(traceResults.Traces) != 1 {
		t.Fatalf("trace results = %#v", traceResults)
	}
	eventResults, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: "migrations"}, Limit: 10, ResultLevel: "event"})
	if err != nil {
		t.Fatalf("SearchTraces event returned error: %v", err)
	}
	if eventResults.TotalMatched != 2 || len(eventResults.Events) != 2 {
		t.Fatalf("event results = %#v", eventResults)
	}
	if eventResults.Events[0].Trace.ID != "session:cursor:s1" || eventResults.Events[0].Event.Number != 1 {
		t.Fatalf("event match = %#v", eventResults.Events[0])
	}
}

func TestTraceAPIEndpoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	prompt := testSchemaEvent("2026-06-11T10:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-a")
	prompt.Event.ID = "evt-prompt"
	prompt.Session = &schema.SessionInfo{ID: "s1"}
	prompt.Prompt = &schema.PromptInfo{Text: "Share this trace"}
	prompt.Raw = map[string]interface{}{"shared_url": "https://traces.example/t/s1", "visibility": "public"}
	writeTestLog(t, path, marshalEvents(t, prompt)...)
	handler, err := Handler(Options{UserMode: true, LogPath: path})
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/traces?visibility=public", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("traces status = %d body=%s", rec.Code, rec.Body.String())
	}
	var list TraceListResultV1
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if list.TotalMatched != 1 || list.Traces[0].Sharing.URL == "" {
		t.Fatalf("list response = %#v", list)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/trace?id=session:cursor:s1&limit=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("trace status = %d body=%s", rec.Code, rec.Body.String())
	}
	var show TraceShowResultV1
	if err := json.Unmarshal(rec.Body.Bytes(), &show); err != nil {
		t.Fatalf("unmarshal show: %v", err)
	}
	if show.Trace.ID != "session:cursor:s1" || show.Range.TotalEvents != 1 {
		t.Fatalf("show response = %#v", show)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/trace/search?q=share&result_level=event", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"result_level":"event"`) {
		t.Fatalf("search body missing event result level: %s", rec.Body.String())
	}
}

func TestTraceTitlePrefersUserPromptOverEarlierLifecycleEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	started := testSchemaEvent("2026-06-11T10:00:00Z", "cursor", "session.started", "session", "repo-a")
	started.Event.ID = "evt-started"
	started.Session = &schema.SessionInfo{ID: "s1", WorkingDirectory: "/repo/a"}
	context := testSchemaEvent("2026-06-11T10:00:01Z", "cursor", "session.context", "session", "repo-a")
	context.Event.ID = "evt-context"
	context.Session = started.Session
	prompt := testSchemaEvent("2026-06-11T10:00:02Z", "cursor", "prompt.submitted", "prompt", "repo-a")
	prompt.Event.ID = "evt-prompt"
	prompt.Session = started.Session
	prompt.Prompt = &schema.PromptInfo{Text: "Investigate the flaky dashboard test"}
	later := testSchemaEvent("2026-06-11T10:00:03Z", "cursor", "prompt.submitted", "prompt", "repo-a")
	later.Event.ID = "evt-prompt-2"
	later.Session = started.Session
	later.Prompt = &schema.PromptInfo{Text: "Now ship it"}
	writeTestLog(t, path, marshalEvents(t, started, context, prompt, later)...)

	result, err := ReadTraceList(path, TraceQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ReadTraceList returned error: %v", err)
	}
	if len(result.Traces) != 1 {
		t.Fatalf("traces = %#v, want 1", result.Traces)
	}
	trace := result.Traces[0]
	if trace.Title != "Investigate the flaky dashboard test" {
		t.Fatalf("trace title = %q, want the first user prompt", trace.Title)
	}
	if trace.Preview != "Investigate the flaky dashboard test" {
		t.Fatalf("trace preview = %q, want the first user prompt", trace.Preview)
	}
	if trace.EventCount != 4 {
		t.Fatalf("trace event count = %d, want 4", trace.EventCount)
	}
}

func TestTraceTitleFallsBackToFirstEventWithoutPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	command := testSchemaEvent("2026-06-11T10:00:00Z", "cursor", "command.executed", "command", "repo-a")
	command.Event.ID = "evt-command"
	command.Session = &schema.SessionInfo{ID: "s1"}
	command.Command = &schema.CommandInfo{Command: "go test ./..."}

	lifecycle := testSchemaEvent("2026-06-11T11:00:00Z", "cursor", "session.started", "session", "repo-a")
	lifecycle.Event.ID = "evt-lifecycle"
	lifecycle.Session = &schema.SessionInfo{ID: "s2"}
	writeTestLog(t, path, marshalEvents(t, command, lifecycle)...)

	result, err := ReadTraceList(path, TraceQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ReadTraceList returned error: %v", err)
	}
	titles := map[string]string{}
	previews := map[string]string{}
	for _, trace := range result.Traces {
		titles[trace.ID] = trace.Title
		previews[trace.ID] = trace.Preview
	}
	if titles["session:cursor:s1"] != "go test ./..." {
		t.Fatalf("command trace title = %q, want its only event", titles["session:cursor:s1"])
	}
	// A trace that never gets past lifecycle events is still named by that event
	// rather than falling back to its raw trace ID.
	if titles["session:cursor:s2"] != "session.started" {
		t.Fatalf("lifecycle-only trace title = %q, want session.started", titles["session:cursor:s2"])
	}
	if previews["session:cursor:s2"] == "" {
		t.Fatalf("lifecycle-only trace has no preview: %#v", result.Traces)
	}
}

func TestSearchTracesEventResultsAreOrderedAndTypeFiltered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	events := []schema.Event{}
	// Three sessions, each a prompt plus a command, all mentioning "migrations".
	// Sessions are written oldest first so the newest sorts to the top.
	for _, session := range []struct{ id, hour string }{{"s1", "10"}, {"s2", "11"}, {"s3", "12"}} {
		prompt := testSchemaEvent("2026-06-11T"+session.hour+":00:00Z", "cursor", "prompt.submitted", "prompt", "repo-a")
		prompt.Event.ID = "evt-prompt-" + session.id
		prompt.Session = &schema.SessionInfo{ID: session.id}
		prompt.Prompt = &schema.PromptInfo{Text: "Investigate migrations in " + session.id}
		command := testSchemaEvent("2026-06-11T"+session.hour+":00:01Z", "cursor", "command.executed", "command", "repo-a")
		command.Event.ID = "evt-command-" + session.id
		command.Session = prompt.Session
		command.Command = &schema.CommandInfo{Command: "go test ./migrations # " + session.id}
		events = append(events, prompt, command)
	}
	writeTestLog(t, path, marshalEvents(t, events...)...)

	// Six events match but only two are returned, so the order decides which
	// ones. Repeat the search: the aggregates live in a map, and an unsorted
	// walk would hand back a different pair on some runs.
	for i := 0; i < 8; i++ {
		results, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: "migrations"}, Limit: 2, ResultLevel: "event"})
		if err != nil {
			t.Fatalf("SearchTraces returned error: %v", err)
		}
		if results.TotalMatched != 6 || len(results.Events) != 2 {
			t.Fatalf("event results = matched %d returned %d, want 6/2", results.TotalMatched, len(results.Events))
		}
		if got := results.Events[0].Event.ID; got != "evt-prompt-s3" {
			t.Fatalf("first event = %q, want evt-prompt-s3 (newest trace first)", got)
		}
		if got := results.Events[1].Event.ID; got != "evt-command-s3" {
			t.Fatalf("second event = %q, want evt-command-s3", got)
		}
	}

	filtered, err := SearchTraces(path, TraceQuery{
		EventQuery:  EventQuery{Q: "migrations"},
		Limit:       10,
		ResultLevel: "event",
		EventTypes:  []string{"command"},
	})
	if err != nil {
		t.Fatalf("SearchTraces filtered returned error: %v", err)
	}
	if filtered.TotalMatched != 3 || len(filtered.Events) != 3 {
		t.Fatalf("filtered results = matched %d returned %d, want 3/3", filtered.TotalMatched, len(filtered.Events))
	}
	for _, match := range filtered.Events {
		if match.Event.Type != "command" {
			t.Fatalf("event_type filter returned %q", match.Event.Type)
		}
	}
	if filtered.Filters["event_type"] != "command" {
		t.Fatalf("filters = %#v, want event_type command", filtered.Filters)
	}
}

func TestFreeTextSelectsTracesWithoutDroppingTheirEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	prompt := testSchemaEvent("2026-06-11T10:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-a")
	prompt.Event.ID = "evt-prompt"
	prompt.Session = &schema.SessionInfo{ID: "s1"}
	prompt.Prompt = &schema.PromptInfo{Text: "Investigate database migrations"}
	command := testSchemaEvent("2026-06-11T10:01:00Z", "cursor", "command.executed", "command", "repo-a")
	command.Event.ID = "evt-command"
	command.Session = prompt.Session
	command.Command = &schema.CommandInfo{Command: "go test ./migrations"}
	note := testSchemaEvent("2026-06-11T10:02:00Z", "cursor", "file.modified", "file", "repo-a")
	note.Event.ID = "evt-file"
	note.Session = prompt.Session
	note.File = &schema.FileInfo{Path: "notes.txt", Operation: "modify"}
	other := testSchemaEvent("2026-06-11T09:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-b")
	other.Event.ID = "evt-other"
	other.Session = &schema.SessionInfo{ID: "s2"}
	other.Prompt = &schema.PromptInfo{Text: "Rewrite the changelog"}
	writeTestLog(t, path, marshalEvents(t, other, prompt, command, note)...)

	list, err := ReadTraceList(path, TraceQuery{EventQuery: EventQuery{Q: "database"}, Limit: 10})
	if err != nil {
		t.Fatalf("ReadTraceList returned error: %v", err)
	}
	if list.TotalMatched != 1 || len(list.Traces) != 1 || list.Traces[0].ID != "session:cursor:s1" {
		t.Fatalf("list = %#v, want only the matching trace", list)
	}
	if list.Traces[0].EventCount != 3 {
		t.Fatalf("matched trace event count = %d, want all 3 of its events", list.Traces[0].EventCount)
	}

	// A term that only one event carries still selects the whole trace.
	byEvent, err := ReadTraceList(path, TraceQuery{EventQuery: EventQuery{Q: "notes.txt"}, Limit: 10})
	if err != nil {
		t.Fatalf("ReadTraceList by event returned error: %v", err)
	}
	if byEvent.TotalMatched != 1 || byEvent.Traces[0].EventCount != 3 {
		t.Fatalf("event-matched list = %#v, want the whole trace", byEvent.Traces)
	}

	show, ok, err := ShowTrace(path, "session:cursor:s1", TraceQuery{EventQuery: EventQuery{Q: "database"}, Limit: 10})
	if err != nil || !ok {
		t.Fatalf("ShowTrace ok=%t err=%v", ok, err)
	}
	if show.Range.TotalEvents != 3 || len(show.Events) != 3 {
		t.Fatalf("show = range %#v events %d, want the full 3-event timeline", show.Range, len(show.Events))
	}
}

func TestTraceStoreIndexesAndRefreshesRuntimeLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "runtime.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	prompt := testSchemaEvent("2026-06-11T10:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-a")
	prompt.Event.ID = "evt-prompt"
	prompt.Session = &schema.SessionInfo{ID: "s1", WorkingDirectory: "/repo/a"}
	prompt.Prompt = &schema.PromptInfo{Text: "Index local traces"}
	command := testSchemaEvent("2026-06-11T10:01:00Z", "cursor", "command.executed", "command", "repo-a")
	command.Event.ID = "evt-command"
	command.Session = prompt.Session
	command.Command = &schema.CommandInfo{Command: "go test ./internal/endpoint/dashboard"}
	writeTestLog(t, path, marshalEvents(t, prompt, command)...)

	status, err := TraceStoreStatus(path)
	if err != nil {
		t.Fatalf("TraceStoreStatus returned error: %v", err)
	}
	if status.Path != filepath.Join(filepath.Dir(filepath.Dir(path)), "traces.db") {
		t.Fatalf("trace store path = %q", status.Path)
	}
	if status.Traces != 1 || status.Events != 2 || status.IndexRows != 3 {
		t.Fatalf("status = %#v, want 1 trace, 2 events, 3 index rows", status)
	}

	results, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: "dashboard"}, ResultLevel: "event", Limit: 10})
	if err != nil {
		t.Fatalf("SearchTraces returned error: %v", err)
	}
	if results.TotalMatched != 1 || len(results.Events) != 1 || results.Events[0].Event.ID != "evt-command" {
		t.Fatalf("indexed search results = %#v", results)
	}

	later := testSchemaEvent("2026-06-11T10:02:00Z", "cursor", "file.modified", "file", "repo-a")
	later.Event.ID = "evt-file"
	later.Session = prompt.Session
	later.File = &schema.FileInfo{Path: "trace_store.go", Operation: "modify"}
	writeTestLog(t, path, marshalEvents(t, prompt, command, later)...)

	status, err = TraceStoreStatus(path)
	if err != nil {
		t.Fatalf("TraceStoreStatus after refresh returned error: %v", err)
	}
	if status.Events != 3 || status.IndexRows != 4 {
		t.Fatalf("refreshed status = %#v, want 3 events and 4 index rows", status)
	}
}

func int64Ptr(v int64) *int64 { return &v }
