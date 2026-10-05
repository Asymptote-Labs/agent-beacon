package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// lensFixture writes a log holding two Claude Code sessions that each run a command the baseline
// recursive-root-delete rule matches, so a test can check that one trace's lens data carries only
// its own finding.
func lensFixture(t *testing.T) string {
	t.Helper()
	testenv.SetHome(t, t.TempDir()) // empty rule store -> embedded baseline
	withoutHistory(t)
	var events []schema.Event
	for _, session := range []string{"s1", "s2"} {
		prompt := testSchemaEvent("2026-06-11T10:00:00Z", "claude_code", "prompt.submitted", "prompt", "repo-a")
		prompt.Event.ID = "evt-prompt-" + session
		prompt.Session = &schema.SessionInfo{ID: session}
		prompt.Prompt = &schema.PromptInfo{Text: "clean up " + session}
		command := testSchemaEvent("2026-06-11T10:01:00Z", "claude_code", "command.executed", "command", "repo-a")
		command.Event.ID = "evt-rm-" + session
		command.Session = prompt.Session
		command.Command = &schema.CommandInfo{Command: "rm -rf /"}
		events = append(events, prompt, command)
	}
	return newTestLog(t, marshalEvents(t, events...))
}

func TestBuildLensDataBundlesTheTraceItsFindingsAndCoverage(t *testing.T) {
	path := lensFixture(t)

	data, ok, err := BuildLensData(path, "session:claude_code:s1", LensDataOptions{UserMode: true})
	if err != nil || !ok {
		t.Fatalf("BuildLensData = ok %v, err %v", ok, err)
	}
	if data.APIVersion != asymptoteobserve.LensAPIVersion || data.Trace.SchemaVersion != asymptoteobserve.TraceSchemaVersion {
		t.Fatalf("versions = %q / %q", data.APIVersion, data.Trace.SchemaVersion)
	}
	if data.Trace.ID != "session:claude_code:s1" || len(data.Trace.Events) != 2 || data.Truncated {
		t.Fatalf("trace = %s with %d events, truncated %v", data.Trace.ID, len(data.Trace.Events), data.Truncated)
	}

	if data.Findings == nil || data.Findings.RulesEvaluated == 0 {
		t.Fatalf("findings = %#v, want a scan over the baseline rules", data.Findings)
	}
	var rmFindings []asymptoteobserve.LensFindingV1
	for _, f := range data.Findings.Items {
		if f.RuleID == "recursive-root-delete" {
			rmFindings = append(rmFindings, f)
		}
	}
	if len(rmFindings) != 1 {
		t.Fatalf("recursive-root-delete findings = %#v, want exactly s1's", rmFindings)
	}
	if got := rmFindings[0].EventIDs; len(got) != 1 || got[0] != "evt-rm-s1" {
		t.Fatalf("evidence = %v, want [evt-rm-s1]", got)
	}
	resolved := false
	for _, event := range data.Trace.Events {
		if event.ID == rmFindings[0].EventIDs[0] {
			resolved = true
		}
	}
	if !resolved {
		t.Fatal("finding evidence does not resolve against trace.events[].id")
	}

	// Claude Code is a runtime Beacon reads usage from, and this trace reported none.
	want := asymptoteobserve.LensTokenCoverageV1{Harness: "claude_code", Status: "silent", Expectation: "reported", Reason: "OTLP token and cost telemetry"}
	if data.TokenCoverage == nil || *data.TokenCoverage != want {
		t.Fatalf("token coverage = %#v, want %#v", data.TokenCoverage, want)
	}
}

func TestBuildLensDataWithoutRulesLeavesFindingsUnscanned(t *testing.T) {
	path := lensFixture(t)
	// An empty rules directory is the "no rules to run" case.
	data, ok, err := BuildLensData(path, "session:claude_code:s1", LensDataOptions{UserMode: true, RulesDir: t.TempDir()})
	if err != nil || !ok {
		t.Fatalf("BuildLensData = ok %v, err %v", ok, err)
	}
	if data.Findings != nil {
		t.Fatalf("findings = %#v, want nil (not scanned)", data.Findings)
	}
	if len(data.Trace.Events) != 2 || data.TokenCoverage == nil {
		t.Fatal("the rest of the lens data should survive a rule-load failure")
	}
}

func TestBuildLensDataUnknownTrace(t *testing.T) {
	path := lensFixture(t)
	if _, ok, err := BuildLensData(path, "session:claude_code:nope", LensDataOptions{UserMode: true}); ok || err != nil {
		t.Fatalf("BuildLensData = ok %v, err %v, want not found", ok, err)
	}
}

// TestBuildLensDataReadsPastTheTraceViewPageLimit checks that a lens gets the whole trace, not the
// first page the trace view would show.
func TestBuildLensDataReadsPastTheTraceViewPageLimit(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	withoutHistory(t)
	total := maxEventLimit + 25
	events := make([]schema.Event, 0, total)
	for i := 0; i < total; i++ {
		event := testSchemaEvent("2026-06-11T10:00:00Z", "cursor", "command.executed", "command", "repo-a")
		event.Event.ID = fmt.Sprintf("evt-%05d", i)
		event.Session = &schema.SessionInfo{ID: "long"}
		event.Command = &schema.CommandInfo{Command: "true"}
		events = append(events, event)
	}
	path := newTestLog(t, marshalEvents(t, events...))

	data, ok, err := BuildLensData(path, "session:cursor:long", LensDataOptions{UserMode: true})
	if err != nil || !ok {
		t.Fatalf("BuildLensData = ok %v, err %v", ok, err)
	}
	if len(data.Trace.Events) != total || data.Truncated {
		t.Fatalf("events = %d (truncated %v), want all %d", len(data.Trace.Events), data.Truncated, total)
	}
	if data.Trace.Range == nil || data.Trace.Range.TotalEvents != total || data.Trace.Range.ReturnedEvents != total {
		t.Fatalf("range = %#v", data.Trace.Range)
	}
}

func TestFitLensEventsKeepsTheLongestPrefixWithinBudget(t *testing.T) {
	bundle := asymptoteobserve.TraceBundleV1{Range: &asymptoteobserve.TraceRangeV1{TotalEvents: 4, ReturnedEvents: 4, Offset: 1, Limit: 4}}
	for i := 1; i <= 4; i++ {
		bundle.Events = append(bundle.Events, asymptoteobserve.TraceEventV1{ID: fmt.Sprintf("e%d", i), Number: i, Summary: strings.Repeat("x", 100)})
	}
	one, _ := json.Marshal(bundle.Events[0])
	perEvent := len(one) + 1

	whole := bundle
	whole.Events = append([]asymptoteobserve.TraceEventV1(nil), bundle.Events...)
	if fitLensEvents(&whole, perEvent*4) || len(whole.Events) != 4 {
		t.Fatalf("a bundle that fits was cut to %d events", len(whole.Events))
	}

	cut := bundle
	cut.Range = &asymptoteobserve.TraceRangeV1{TotalEvents: 4, ReturnedEvents: 4, Offset: 1, Limit: 4}
	if !fitLensEvents(&cut, perEvent*2+perEvent/2) {
		t.Fatal("fitLensEvents did not report truncation")
	}
	if len(cut.Events) != 2 || cut.Events[1].ID != "e2" {
		t.Fatalf("kept %d events, want the first 2", len(cut.Events))
	}
	if cut.Range.TotalEvents != 4 || cut.Range.ReturnedEvents != 2 {
		t.Fatalf("range = %#v, want total 4, returned 2", cut.Range)
	}
}

func TestLensDataEndpoint(t *testing.T) {
	path := lensFixture(t)
	handler, err := Handler(Options{UserMode: true, LogPath: path})
	if err != nil {
		t.Fatal(err)
	}
	get := func(url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		return rec
	}

	rec := get("/api/lens-data?trace=" + "session:claude_code:s1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// The served JSON is the contract: it must decode strictly into LensDataV1.
	decoder := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	decoder.DisallowUnknownFields()
	var data asymptoteobserve.LensDataV1
	if err := decoder.Decode(&data); err != nil {
		t.Fatalf("response is not LensDataV1: %v", err)
	}
	if data.Trace.ID != "session:claude_code:s1" {
		t.Fatalf("trace = %q", data.Trace.ID)
	}

	if rec := get("/api/lens-data"); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing trace status = %d, want 400", rec.Code)
	}
	if rec := get("/api/lens-data?trace=session:claude_code:nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown trace status = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/lens-data?trace=session:claude_code:s1", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", rec.Code)
	}
}

func TestLensDataEvidenceResolvesAgainstTheHistoryBundle(t *testing.T) {
	// With the history opted in, the bundle comes from the store; findings still come from the
	// live log. Both must agree on event IDs so evidence resolves.
	path := lensFixture(t)
	withHistory(t)
	optIn(t, path)
	data, ok, err := BuildLensData(path, "session:claude_code:s1", LensDataOptions{UserMode: true})
	if err != nil || !ok {
		t.Fatalf("BuildLensData = ok %v, err %v", ok, err)
	}
	ids := map[string]bool{}
	for _, event := range data.Trace.Events {
		ids[event.ID] = true
	}
	for _, f := range data.Findings.Items {
		for _, id := range f.EventIDs {
			if !ids[id] {
				t.Fatalf("finding %s cites %s, missing from the history-served bundle %v", f.RuleID, id, ids)
			}
		}
	}
}

// A trace that only the history still holds -- its lines rotated out of the live log -- keeps its
// bundle, but must not claim a clean scan or a coverage verdict it could not compute.
func TestLensDataForARotatedTraceIsNotScanned(t *testing.T) {
	path := lensFixture(t)
	withHistory(t)
	optIn(t, path)
	// Rotate: the live log now holds only an unrelated session.
	other := testSchemaEvent("2026-06-12T10:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-b")
	other.Event.ID = "evt-later"
	other.Session = &schema.SessionInfo{ID: "later"}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	writeTestLog(t, path, marshalEvents(t, other)...)
	if err := os.Remove(path + ".1"); err != nil {
		t.Fatal(err)
	}

	data, ok, err := BuildLensData(path, "session:claude_code:s1", LensDataOptions{UserMode: true})
	if err != nil || !ok {
		t.Fatalf("BuildLensData = ok %v, err %v; the history should still serve the trace", ok, err)
	}
	if len(data.Trace.Events) != 2 {
		t.Fatalf("bundle has %d events, want the 2 the history kept", len(data.Trace.Events))
	}
	if data.Findings != nil {
		t.Fatalf("findings = %#v, want nil (not scanned) for a trace with no live events", data.Findings)
	}
	if data.TokenCoverage != nil {
		t.Fatalf("token coverage = %#v, want nil", data.TokenCoverage)
	}
}

// A session whose hook events and OTLP spans project to different traces is still one session on
// the dashboard, and its lens must see every event the session page lists.
func TestBuildSessionLensDataCoversEveryEventOfTheSession(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	withoutHistory(t)
	hook := testSchemaEvent("2026-06-11T10:00:00Z", "codex_cli", "command.executed", "command", "repo-a")
	hook.Event.ID = "evt-hook"
	hook.Session = &schema.SessionInfo{ID: "codex-1"}
	hook.Command = &schema.CommandInfo{Command: "rm -rf /"}
	span := testSchemaEvent("2026-06-11T10:00:05Z", "codex_cli", "tool.invoked", "tool", "repo-a")
	span.Event.ID = "evt-span"
	span.Session = hook.Session
	span.Trace = &schema.TraceInfo{ID: "otel-trace", SpanID: "span-1"}
	other := testSchemaEvent("2026-06-11T10:00:06Z", "codex_cli", "command.executed", "command", "repo-a")
	other.Event.ID = "evt-other"
	other.Session = &schema.SessionInfo{ID: "codex-10"} // shares a prefix; must not match
	path := newTestLog(t, marshalEvents(t, hook, span, other))

	data, ok, err := BuildSessionLensData(path, "codex-1", LensDataOptions{UserMode: true})
	if err != nil || !ok {
		t.Fatalf("BuildSessionLensData = ok %v, err %v", ok, err)
	}
	var ids []string
	for _, event := range data.Trace.Events {
		ids = append(ids, event.ID)
	}
	if strings.Join(ids, ",") != "evt-hook,evt-span" {
		t.Fatalf("session bundle events = %v, want the hook event and the span", ids)
	}
	if data.Trace.ID != "session:codex_cli:codex-1" || data.Trace.Summary.EventCount != 2 {
		t.Fatalf("bundle = %s with %d events", data.Trace.ID, data.Trace.Summary.EventCount)
	}
	if data.Trace.Range == nil || data.Trace.Range.TotalEvents != 2 || data.Trace.Range.ReturnedEvents != 2 {
		t.Fatalf("range = %#v", data.Trace.Range)
	}
	if data.Findings == nil || len(data.Findings.Items) == 0 {
		t.Fatal("session lens data should carry the rm -rf finding")
	}
	for _, f := range data.Findings.Items {
		for _, id := range f.EventIDs {
			if id == "evt-other" {
				t.Fatal("a finding from another session leaked in")
			}
		}
	}

	// The trace projection of the same session holds only the hook event; that is why the session
	// page asks by session.
	byTrace, ok, err := BuildLensData(path, "session:codex_cli:codex-1", LensDataOptions{UserMode: true})
	if err != nil || !ok || len(byTrace.Trace.Events) != 1 {
		t.Fatalf("trace-scoped lens data = %d events, ok %v, err %v", len(byTrace.Trace.Events), ok, err)
	}

	if _, ok, err := BuildSessionLensData(path, "codex", LensDataOptions{UserMode: true}); ok || err != nil {
		t.Fatalf("a session-id prefix matched: ok %v, err %v", ok, err)
	}
}

func TestLensDataEndpointBySession(t *testing.T) {
	path := lensFixture(t)
	handler, err := Handler(Options{UserMode: true, LogPath: path})
	if err != nil {
		t.Fatal(err)
	}
	get := func(url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		return rec
	}
	rec := get("/api/lens-data?session=s1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var data asymptoteobserve.LensDataV1
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.Trace.ID != "session:claude_code:s1" || len(data.Trace.Events) != 2 {
		t.Fatalf("trace = %s with %d events", data.Trace.ID, len(data.Trace.Events))
	}
	if rec := get("/api/lens-data?session=s1&trace=session:claude_code:s1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("both params status = %d, want 400", rec.Code)
	}
	if rec := get("/api/lens-data?session=nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session status = %d, want 404", rec.Code)
	}
}

// The lens total is the token report's count, computed by the same aggregation, so a lens and
// `beacon token-usage` cannot disagree about one trace.
func TestLensTokenUsageMatchesTheTokenReport(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	withoutHistory(t)
	turn := func(id string, input, output int64, cost float64, model string) schema.Event {
		e := testSchemaEvent("2026-06-11T10:00:00Z", "claude_code", "agent.message", "agent", "repo-a")
		e.Event.ID = id
		e.Session = &schema.SessionInfo{ID: "u1"}
		e.Model = model
		e.GenAI = &schema.GenAIInfo{Usage: &schema.GenAIUsageInfo{InputTokens: int64Ptr(input), OutputTokens: int64Ptr(output), CostUSD: &cost}}
		return e
	}
	events := []schema.Event{turn("a", 100, 10, 0.01, "model-a"), turn("b", 300, 30, 0.03, "model-b")}
	path := newTestLog(t, marshalEvents(t, events...))

	data, ok, err := BuildSessionLensData(path, "u1", LensDataOptions{UserMode: true})
	if err != nil || !ok {
		t.Fatalf("BuildSessionLensData = ok %v, err %v", ok, err)
	}
	want := tokens.Aggregate(events, tokens.Options{})
	got := data.TokenUsage
	if got == nil || got.Totals.InputTokens != want.Totals.InputTokens || got.Totals.OutputTokens != want.Totals.OutputTokens ||
		got.EventsWithUsage != want.EventsWithUsage || len(got.ByModel) != len(want.ByModel) {
		t.Fatalf("token usage = %#v, want the token report's %#v", got, want.Totals)
	}
	if got.Totals.InputTokens != 400 || got.ByModel[0].Model != "model-b" || got.ByModel[0].Events != 1 {
		t.Fatalf("token usage = %#v", got)
	}
}
