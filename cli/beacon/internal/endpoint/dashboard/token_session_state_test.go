package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
)

func TestReadTokenEventsSessionStateMatchesEventReader(t *testing.T) {
	var events []schema.Event
	for i, session := range []struct {
		id, harness, model, prompt string
		input                      int64
	}{
		{"recurring-a", "cursor", "model-a", "repeat this task", 10},
		{"recurring-b", "cursor", "model-a", "repeat this task", 20},
		{"captured-cursor", "cursor", "model-a", "unique task", 30},
		{"captured-codex", "codex_cli", "model-b", "another task", 40},
		{"empty", "cursor", "model-a", "", 50},
	} {
		start := recurringTestEvent(fmt.Sprintf("2026-06-11T10:00:%02dZ", i), session.id, session.prompt, session.harness, "repo-a", "alice")
		if session.prompt == "" {
			start.Event.Action, start.Event.Category = "session.started", "session"
			start.Prompt = nil
		}
		usage := testSchemaEvent(fmt.Sprintf("2026-06-11T10:00:%02dZ", 10+i), session.harness, "token.usage", "metric", "repo-a")
		usage.Session = start.Session
		usage.Model = session.model
		usage.GenAI = &schema.GenAIInfo{Usage: &schema.GenAIUsageInfo{InputTokens: int64Ptr(session.input)}}
		events = append(events, start, usage)
	}
	path := newTestLog(t, marshalEvents(t, events...))
	for _, tc := range []struct {
		name  string
		query EventQuery
		input int64
	}{
		{"captured", EventQuery{SessionState: "captured"}, 70},
		{"recurring", EventQuery{SessionState: "recurring"}, 30},
		{"empty", EventQuery{SessionState: "empty"}, 50},
		{"normalized captured", EventQuery{SessionState: " CaPtUrEd "}, 70},
		{"normalized recurring", EventQuery{SessionState: " ReCuRrInG "}, 30},
		{"normalized empty", EventQuery{SessionState: " EmPtY "}, 50},
		{"all", EventQuery{SessionState: "all"}, 150},
		{"unknown", EventQuery{SessionState: "unknown"}, 150},
		{"unfiltered", EventQuery{}, 150},
		{"time window", EventQuery{SessionState: "captured", Since: mustParseTime(t, "2026-06-11T10:00:02Z"), Until: mustParseTime(t, "2026-06-11T10:00:12Z")}, 30},
		{"harness", EventQuery{SessionState: "captured", Harness: "cursor"}, 30},
		{"model", EventQuery{SessionState: "captured", Model: "model-b"}, 40},
		{"session", EventQuery{SessionState: "recurring", Session: "recurring-a"}, 10},
		{"limit", EventQuery{SessionState: "captured", Limit: 1}, 70},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := ReadEventsAppendOrder(path, tc.query)
			if err != nil {
				t.Fatal(err)
			}
			got, contexts, err := ReadTokenEventsAppendOrder(path, tc.query)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("token reader selected %d events, ordinary reader selected %d; events differ", len(got), len(want))
			}
			report := tokens.AggregateScopedWithContexts(got, contexts, "", tokens.Options{})
			if report.Totals.InputTokens != tc.input || report.TotalEvents != len(want) {
				t.Fatalf("input=%d events=%d, want input=%d events=%d", report.Totals.InputTokens, report.TotalEvents, tc.input, len(want))
			}
		})
	}
}

func TestTokensEndpointSessionStateKeepsUnfilteredContexts(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	context := testSchemaEvent("2026-06-11T09:59:00Z", "claude_code", "session.context", "session", "repo-a")
	context.Event.ID = "context"
	context.Session = &schema.SessionInfo{ID: "s1"}
	context.User = schema.UserInfo{Name: "alice", UID: "501"}
	live := testSchemaEvent("2026-06-11T10:00:00Z", "claude_code", "prompt.submitted", "prompt", "repo-a")
	live.Event.ID = "live-start"
	live.Session = context.Session
	live.Harness.CollectionMethod = "otlp"
	live.Prompt = &schema.PromptInfo{Text: "first task"}
	command := testSchemaEvent("2026-06-11T10:00:01Z", "claude_code", "command.executed", "command", "repo-a")
	command.Session = context.Session
	command.Command = &schema.CommandInfo{Command: "go test ./..."}
	usage := func(timestamp, method string) schema.Event {
		event := testSchemaEvent(timestamp, "claude_code", "token.usage", "metric", "repo-a")
		event.Session = context.Session
		event.Harness.CollectionMethod = method
		event.Model = "claude-opus-4-6"
		event.GenAI = &schema.GenAIInfo{Usage: &schema.GenAIUsageInfo{InputTokens: int64Ptr(6), OutputTokens: int64Ptr(120)}}
		return event
	}
	path := newTestLog(t, marshalEvents(t, context, live, command,
		usage("2026-06-11T10:00:02Z", "poll"), usage("2026-06-11T10:00:06Z", "otlp")))
	query := EventQuery{SessionState: "captured", Since: mustParseTime(t, "2026-06-11T10:00:01Z")}
	events, contexts, err := ReadTokenEventsAppendOrder(path, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("selected events=%d, want command and two usage records", len(events))
	}
	if len(contexts) != 2 || contexts[0].Event.ID != "context" || contexts[1].Event.ID != "live-start" {
		t.Fatalf("contexts=%+v, want the session context and first live event outside the window", contexts)
	}
	assertReport := func(t *testing.T, report tokens.Report) {
		t.Helper()
		if report.TotalEvents != 3 || report.EventsWithUsage != 1 || report.SuppressedPollEvents != 1 ||
			report.Totals.InputTokens != 6 || report.Totals.OutputTokens != 120 {
			t.Fatalf("report=%+v, want three selected events and one usage contribution", report)
		}
		if len(report.ByUser) != 1 || report.ByUser[0].Key != "alice [501]" {
			t.Fatalf("users=%+v, want attribution from the context before the window", report.ByUser)
		}
	}
	assertReport(t, tokens.AggregateScopedWithContexts(events, contexts, "", tokens.Options{}))
	handler, err := Handler(Options{UserMode: true, LogPath: path})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tokens?session_state=captured&since=2026-06-11T10:00:01Z", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var report tokens.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	assertReport(t, report)
}

func TestReadTokenEventsSessionStateKeepsCumulativeAppendOrder(t *testing.T) {
	for _, sequenced := range []bool{false, true} {
		t.Run(fmt.Sprintf("sequenced=%t", sequenced), func(t *testing.T) {
			usage := func(input int64) schema.Event {
				event := testSchemaEvent("2026-06-11T10:00:00Z", "claude_code", "token.usage", "metric", "repo-a")
				event.Session = &schema.SessionInfo{ID: "s1"}
				event.Model = "model-a"
				event.GenAI = &schema.GenAIInfo{Usage: &schema.GenAIUsageInfo{InputTokens: int64Ptr(input)}}
				event.Raw = map[string]interface{}{"metric_name": "claude_code.token.usage", "metric_temporality": "Cumulative"}
				if sequenced {
					event.Sequence = uint64(input)
				}
				return event
			}
			path := filepath.Join(t.TempDir(), "runtime.jsonl")
			writeTestLog(t, path+".2", marshalEvents(t, usage(100))...)
			writeTestLog(t, path+".1", marshalEvents(t, usage(200))...)
			active := []schema.Event{usage(300), usage(400)}
			if sequenced {
				active[0], active[1] = active[1], active[0]
			}
			writeTestLog(t, path, marshalEvents(t, active...)...)
			events, contexts, err := ReadTokenEventsAppendOrder(path, EventQuery{SessionState: "empty"})
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 4 {
				t.Fatalf("selected events=%d, want all four metric-only session records", len(events))
			}
			for i, event := range events {
				if got, want := *event.GenAI.Usage.InputTokens, int64(i+1)*100; got != want {
					t.Fatalf("event %d input=%d want %d in append order", i, got, want)
				}
			}
			report := tokens.AggregateScopedWithContexts(events, contexts, "", tokens.Options{})
			if report.Totals.InputTokens != 400 {
				t.Fatalf("cumulative total=%d, want 400", report.Totals.InputTokens)
			}
		})
	}
}
