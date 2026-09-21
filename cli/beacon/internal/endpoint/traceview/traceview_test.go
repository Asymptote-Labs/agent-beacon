package traceview

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
)

type fakeStore struct {
	traces      []dashboard.TraceSummaryV1
	events      []dashboard.TraceEventV1
	listQueries []string
	showFilters [][]string
}

func (s *fakeStore) List(query string) (dashboard.TraceListResultV1, error) {
	s.listQueries = append(s.listQueries, query)
	return dashboard.TraceListResultV1{
		Traces:       s.traces,
		TotalMatched: len(s.traces),
		Returned:     len(s.traces),
	}, nil
}

func (s *fakeStore) Show(id string, eventTypes []string) (dashboard.TraceShowResultV1, bool, error) {
	s.showFilters = append(s.showFilters, append([]string(nil), eventTypes...))
	return dashboard.TraceShowResultV1{
		Trace:  s.traces[0],
		Events: s.events,
		Range:  dashboard.TraceRangeV1{TotalEvents: len(s.events), ReturnedEvents: len(s.events)},
	}, true, nil
}

func fixtureStore() *fakeStore {
	return &fakeStore{
		traces: []dashboard.TraceSummaryV1{{
			ID:         "session:cursor:s1",
			Title:      "Build the trace browser",
			UpdatedAt:  "2026-09-21T08:00:00Z",
			EventCount: 2,
			Harness:    dashboard.TraceHarnessV1{Name: "cursor"},
			Session:    &dashboard.TraceSessionV1{WorkingDirectory: "/work/beacon"},
			TokenUsage: &dashboard.TraceUsageV1{InputTokens: 120, OutputTokens: 30, CostUSD: 0.0042},
			Content:    dashboard.TraceContentSummaryV1{Retention: "full", HasRedactions: true},
		}},
		events: []dashboard.TraceEventV1{
			{Number: 1, Type: "user_message", Action: "prompt.submitted", Content: &dashboard.TraceContentV1{Text: "Create a TUI"}},
			{Number: 2, Type: "command", Action: "command.executed", Command: &dashboard.TraceCommandV1{Command: "go test ./..."}},
		},
	}
}

func runCmd(t *testing.T, model Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return model
	}
	next, followup := model.Update(cmd())
	model = next.(Model)
	if followup != nil {
		next, _ = model.Update(followup())
		model = next.(Model)
	}
	return model
}

func key(value string) tea.KeyMsg {
	switch value {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
	}
}

func TestModelLoadsAndRendersLocalTraceList(t *testing.T) {
	store := fixtureStore()
	model := NewModel(store)
	model.width, model.height = 100, 30
	model = runCmd(t, model, model.Init())

	view := model.View()
	for _, want := range []string{"B E A C O N", "Build the trace browser", "cursor", "/work/beacon", "full, redacted", "nothing is sent anywhere"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if len(store.listQueries) != 1 || store.listQueries[0] != "" {
		t.Fatalf("list queries = %#v", store.listQueries)
	}
}

func TestModelSearchesAndOpensTrace(t *testing.T) {
	store := fixtureStore()
	model := NewModel(store)
	model.width, model.height = 100, 30
	model = runCmd(t, model, model.Init())

	next, _ := model.Update(key("/"))
	model = next.(Model)
	for _, r := range "browser" {
		next, _ = model.Update(key(string(r)))
		model = next.(Model)
	}
	next, cmd := model.Update(key("enter"))
	model = runCmd(t, next.(Model), cmd)
	if got := store.listQueries[len(store.listQueries)-1]; got != "browser" {
		t.Fatalf("search query = %q, want browser", got)
	}

	next, cmd = model.Update(key("enter"))
	model = runCmd(t, next.(Model), cmd)
	if model.mode != detailMode || len(model.detail.Events) != 2 {
		t.Fatalf("detail state = mode %v events %d", model.mode, len(model.detail.Events))
	}
	for _, want := range []string{"Build the trace browser", "150 tokens", "$0.0042", "#1", "user_message", "Create a TUI"} {
		if view := model.View(); !strings.Contains(view, want) {
			t.Fatalf("detail view missing %q:\n%s", want, view)
		}
	}
}

func TestSearchBlocksOpeningStaleRowsUntilResultsLoad(t *testing.T) {
	store := fixtureStore()
	model := NewModel(store)
	model.width, model.height = 100, 30
	model = runCmd(t, model, model.Init())

	next, _ := model.Update(key("/"))
	model = next.(Model)
	for _, r := range "filtered" {
		next, _ = model.Update(key(string(r)))
		model = next.(Model)
	}
	next, pending := model.Update(key("enter"))
	model = next.(Model)
	if !model.loading || model.mode != listMode {
		t.Fatalf("search state = loading %t mode %v", model.loading, model.mode)
	}

	next, command := model.Update(key("enter"))
	model = next.(Model)
	if command != nil || model.mode != listMode {
		t.Fatalf("enter during search load opened stale trace: command=%v mode=%v", command != nil, model.mode)
	}

	next, _ = model.Update(pending())
	model = next.(Model)
	if model.loading || model.mode != listMode || model.query != "filtered" {
		t.Fatalf("loaded search state = loading %t mode %v query %q", model.loading, model.mode, model.query)
	}
	if got := store.listQueries[len(store.listQueries)-1]; got != "filtered" {
		t.Fatalf("search query = %q", got)
	}
}

func TestDetailFilterReloadsTrace(t *testing.T) {
	store := fixtureStore()
	model := NewModel(store)
	model.width, model.height = 100, 30
	model = runCmd(t, model, model.Init())
	next, cmd := model.Update(key("enter"))
	model = runCmd(t, next.(Model), cmd)

	next, cmd = model.Update(key("3"))
	model = runCmd(t, next.(Model), cmd)
	got := strings.Join(store.showFilters[len(store.showFilters)-1], ",")
	if got != "tool_call,tool_result,command,file,mcp" {
		t.Fatalf("tool filter = %q", got)
	}
	if model.filterLabel != "tools" {
		t.Fatalf("filter label = %q", model.filterLabel)
	}
}

func TestLeavingDetailIgnoresInFlightLoad(t *testing.T) {
	store := fixtureStore()
	model := NewModel(store)
	model.width, model.height = 100, 30
	model = runCmd(t, model, model.Init())

	next, pending := model.Update(key("enter"))
	model = next.(Model)
	if !model.loading || model.mode != detailMode {
		t.Fatalf("expected pending detail load, got loading=%t mode=%v", model.loading, model.mode)
	}
	next, _ = model.Update(key("esc"))
	model = next.(Model)
	if model.loading || model.mode != listMode {
		t.Fatalf("leaving detail = loading %t mode %v", model.loading, model.mode)
	}

	next, _ = model.Update(pending())
	model = next.(Model)
	if model.loading || model.mode != listMode || len(model.detail.Events) != 0 || model.err != nil {
		t.Fatalf("stale detail load changed list state: %#v", model)
	}
}

func TestLocalStorePagesThroughAllTracesAndEvents(t *testing.T) {
	originalList, originalShow := readTraceList, showTrace
	t.Cleanup(func() {
		readTraceList, showTrace = originalList, originalShow
	})

	var pages []int
	readTraceList = func(_ string, query dashboard.TraceQuery) (dashboard.TraceListResultV1, error) {
		pages = append(pages, query.Page)
		trace := dashboard.TraceSummaryV1{ID: string(rune('a' + query.Page - 1))}
		return dashboard.TraceListResultV1{
			Traces:       []dashboard.TraceSummaryV1{trace},
			TotalMatched: 2,
			Returned:     1,
			Limit:        loadLimit,
			Page:         query.Page,
			Truncated:    query.Page == 1,
		}, nil
	}

	var offsets []int
	showTrace = func(_ string, _ string, query dashboard.TraceQuery) (dashboard.TraceShowResultV1, bool, error) {
		offsets = append(offsets, query.Offset)
		return dashboard.TraceShowResultV1{
			Trace:  dashboard.TraceSummaryV1{ID: "a"},
			Events: []dashboard.TraceEventV1{{Number: query.Offset}},
			Range: dashboard.TraceRangeV1{
				TotalEvents:    2,
				ReturnedEvents: 1,
				Offset:         query.Offset,
				Limit:          loadLimit,
			},
		}, true, nil
	}

	store := localStore{logPath: "ignored"}
	list, err := store.List("")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if got := strings.Trim(strings.Join([]string{list.Traces[0].ID, list.Traces[1].ID}, ""), " "); got != "ab" {
		t.Fatalf("trace IDs = %q", got)
	}
	if len(pages) != 2 || pages[0] != 1 || pages[1] != 2 || list.Truncated {
		t.Fatalf("pages = %#v result = %#v", pages, list)
	}

	show, ok, err := store.Show("a", nil)
	if err != nil || !ok {
		t.Fatalf("Show = ok %t err %v", ok, err)
	}
	if len(show.Events) != 2 || show.Events[0].Number != 1 || show.Events[1].Number != 2 {
		t.Fatalf("events = %#v", show.Events)
	}
	if len(offsets) != 2 || offsets[0] != 1 || offsets[1] != 2 || show.Range.ReturnedEvents != 2 {
		t.Fatalf("offsets = %#v range = %#v", offsets, show.Range)
	}
}

func TestListResultAppliedWhileInDetailMode(t *testing.T) {
	store := fixtureStore()
	model := NewModel(store)
	model.width, model.height = 100, 30
	model = runCmd(t, model, model.Init())

	next, _ := model.Update(key("/"))
	model = next.(Model)
	for _, r := range "test" {
		next, _ = model.Update(key(string(r)))
		model = next.(Model)
	}
	next, pendingList := model.Update(key("enter"))
	model = next.(Model)
	if !model.loading || model.query != "test" {
		t.Fatalf("expected loading search, got loading=%t query=%q", model.loading, model.query)
	}

	// enter should be blocked while the list is loading.
	next, cmd := model.Update(key("enter"))
	model = next.(Model)
	if model.mode != listMode || cmd != nil {
		t.Fatalf("enter while loading should be a no-op, got mode=%v cmd=%v", model.mode, cmd)
	}

	// Simulate the list load completing after the user opened a trace through
	// some other path (e.g. the load finishes, user quickly opens a trace,
	// then a second search fires).  The important invariant is that a list
	// result delivered in detail mode is still applied.
	model.mode = detailMode
	model.loading = true
	pendingMsg := pendingList()
	next, _ = model.Update(pendingMsg)
	model = next.(Model)

	// The traces should be updated even though we were in detail mode.
	if len(model.traces) != len(store.traces) {
		t.Fatalf("expected traces to be updated, got %d", len(model.traces))
	}
	// loading should remain true because the detail load is still in progress.
	if !model.loading {
		t.Fatalf("expected loading to remain true in detail mode")
	}

	// Returning to list mode should show the search results, not stale data.
	model.mode = listMode
	model.loading = false
	if model.query != "test" || model.totalMatched != len(store.traces) {
		t.Fatalf("list state inconsistent: query=%q totalMatched=%d", model.query, model.totalMatched)
	}
}

func TestEventDetailsIncludesStructuredData(t *testing.T) {
	event := dashboard.TraceEventV1{
		Number: 7,
		Type:   "tool_call",
		Action: "tool.invoked",
		Tool: &dashboard.TraceToolV1{
			Name:      "shell",
			Arguments: map[string]interface{}{"command": "go test ./..."},
			Result:    map[string]interface{}{"exit_code": float64(0)},
		},
		Usage: &dashboard.TraceUsageV1{InputTokens: 12, OutputTokens: 4, CostUSD: 0.001},
	}
	rendered := strings.Join(eventDetails(event, 80), "\n")
	for _, want := range []string{"shell", "go test ./...", "exit_code", "input 12", "$0.001000"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("event details missing %q:\n%s", want, rendered)
		}
	}
}
