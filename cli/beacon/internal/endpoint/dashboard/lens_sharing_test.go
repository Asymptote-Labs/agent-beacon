package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestSessionLensSharingMatchesTrace(t *testing.T) {
	event := func(id, timestamp, action, category string) schema.Event {
		e := testSchemaEvent("2026-06-11T10:"+timestamp+"Z", "claude_code", action, category, "repo-a")
		e.Event.ID = id
		e.Session = &schema.SessionInfo{ID: "s1"}
		return e
	}
	prompt := event("prompt", "00:00", "prompt.submitted", "prompt")
	prompt.Prompt = &schema.PromptInfo{Text: "clean up the project"}
	work := event("work", "02:00", "command.executed", "command")
	work.Command = &schema.CommandInfo{Command: "rm -rf /"}
	work.Model = "test-model"
	work.GenAI = &schema.GenAIInfo{Usage: &schema.GenAIUsageInfo{InputTokens: int64Ptr(100), OutputTokens: int64Ptr(25)}}
	share := func(id, timestamp, through string) schema.Event {
		e := event(id, timestamp, "session.shared", "session")
		e.Raw = map[string]interface{}{"shared_url": "https://beacon.sh/s/s1", "visibility": "direct"}
		if through != "" {
			e.Raw["shared_through"] = "2026-06-11T10:" + through + "Z"
		}
		return e
	}
	for _, tc := range []struct {
		name   string
		events []schema.Event
		state  string
	}{
		{"activity after share", []schema.Event{prompt, share("share", "01:00", "00:30"), work}, "stale"},
		{"activity during upload", []schema.Event{prompt, work, share("share", "03:00", "01:00")}, "stale"},
		{"refresh covers activity", []schema.Event{prompt, share("share", "01:00", "00:30"), work, share("refresh", "03:00", "02:00")}, "shared"},
		{"bookkeeping only", []schema.Event{prompt, share("share", "01:00", "00:30"), event("request", "02:00", "session.share_requested", "session"), event("link", "03:00", "session.commit_linked", "session")}, "shared"},
		{"unshared", []schema.Event{prompt, work}, "local_only"},
		{"legacy current", []schema.Event{prompt, work, share("share", "03:00", "")}, "shared"},
		{"legacy stale", []schema.Event{prompt, share("share", "01:00", ""), work}, "stale"},
		{"out of timestamp order", []schema.Event{prompt, work, share("share", "01:00", "00:30")}, "stale"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testenv.SetHome(t, t.TempDir())
			withoutHistory(t)
			path := newTestLog(t, marshalEvents(t, tc.events...))
			data := checkSessionLensSharing(t, path, "s1", tc.state)
			if tc.state != "local_only" {
				if sharing := sessionSharing(t, path, EventQuery{})["s1"]; sharing == nil || sharing.State != tc.state || sharing.RefreshCommand != data.Trace.Summary.Sharing.RefreshCommand {
					t.Errorf("session sharing = %+v, lens sharing = %+v", sharing, data.Trace.Summary.Sharing)
				}
			}
			if len(data.Trace.Events) != len(tc.events) || data.Trace.Summary.EventCount != len(tc.events) {
				t.Fatalf("events = %d, summary count = %d, want %d", len(data.Trace.Events), data.Trace.Summary.EventCount, len(tc.events))
			}
			expected := append([]schema.Event(nil), tc.events...)
			sort.SliceStable(expected, func(i, j int) bool { return expected[i].Timestamp < expected[j].Timestamp })
			for i, e := range data.Trace.Events {
				if e.ID != expected[i].Event.ID || e.Number != i+1 {
					t.Errorf("event %d = %q, number %d; want %q, number %d", i, e.ID, e.Number, expected[i].Event.ID, i+1)
				}
			}
			if data.Trace.Range == nil || data.Trace.Range.TotalEvents != len(tc.events) || data.Trace.Range.ReturnedEvents != len(tc.events) || data.Truncated {
				t.Fatalf("range = %+v, truncated = %v", data.Trace.Range, data.Truncated)
			}
		})
	}
}

func TestSessionLensSharingExcludesTheShareCommandAndReply(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	withoutHistory(t)
	lines := sharingLines(
		`{"timestamp":"2026-06-11T10:00:00Z","event":{"id":"prompt","action":"prompt.submitted","category":"prompt"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"prompt":{"id":"p1","text":"share this session"}}`,
		`{"timestamp":"2026-06-11T10:00:05Z","event":{"id":"call","action":"tool.invoked","category":"tool"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"prompt":{"id":"p1"},"tool":{"name":"Bash","command":"beacon share --current"},"gen_ai":{"tool":{"call":{"id":"call-share"}}}}`,
		`{"timestamp":"2026-06-11T10:00:09Z","event":{"id":"share","action":"session.shared","category":"session","fidelity":"inferred"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"raw":{"shared_url":"https://beacon.sh/s/s1","visibility":"direct","shared_through":"2026-06-11T10:00:06Z"}}`,
		`{"timestamp":"2026-06-11T10:00:10Z","event":{"id":"completion","action":"tool.completed","category":"tool"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"prompt":{"id":"p1"},"tool":{"name":"Bash"},"gen_ai":{"tool":{"call":{"id":"call-share"}}}}`,
		`{"timestamp":"2026-06-11T10:00:15Z","event":{"id":"reply","action":"agent.message","category":"agent"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"prompt":{"id":"p1"},"message":"Shared: https://beacon.sh/s/s1"}`,
	)
	path := newTestLog(t, lines)
	checkSessionLensSharing(t, path, "s1", "shared")
	lines = append(lines, []byte(`{"timestamp":"2026-06-11T10:05:00Z","event":{"id":"next","action":"tool.invoked","category":"tool"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"tool":{"name":"Edit","path":"main.go"}}`))
	writeTestLog(t, path, lines...)
	checkSessionLensSharing(t, path, "s1", "stale")
}

func checkSessionLensSharing(t *testing.T, path, session, state string) LensDataV1 {
	t.Helper()
	opts := LensDataOptions{UserMode: true}
	data, ok, err := BuildSessionLensData(path, session, opts)
	if err != nil || !ok {
		t.Fatalf("BuildSessionLensData = ok %v, err %v", ok, err)
	}
	trace, ok, err := BuildLensData(path, "session:claude_code:"+session, opts)
	if err != nil || !ok {
		t.Fatalf("BuildLensData = ok %v, err %v", ok, err)
	}
	if sharing := data.Trace.Summary.Sharing; sharing.State != state || !reflect.DeepEqual(sharing, trace.Trace.Summary.Sharing) {
		t.Errorf("session lens sharing = %+v; trace lens sharing = %+v, want state %q", sharing, trace.Trace.Summary.Sharing, state)
	}
	if state != "local_only" && data.Trace.Summary.Sharing.RefreshCommand != "beacon share refresh claude_code:"+session {
		t.Errorf("refresh command = %q", data.Trace.Summary.Sharing.RefreshCommand)
	}
	// Sharing is a local summary overlay; the projected content and derived lens data agree.
	if !reflect.DeepEqual(data.Trace.Events, trace.Trace.Events) || !reflect.DeepEqual(data.Trace.Spans, trace.Trace.Spans) || !reflect.DeepEqual(data.Trace.Range, trace.Trace.Range) {
		t.Error("session lens changed the trace's events, spans or range")
	}
	if !reflect.DeepEqual(data.TokenCoverage, trace.TokenCoverage) || !reflect.DeepEqual(data.TokenUsage, trace.TokenUsage) || !reflect.DeepEqual(data.Findings, trace.Findings) {
		t.Error("session lens changed the trace's coverage, usage or findings")
	}
	return data
}

func TestSessionLensSharingEndpoint(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	withoutHistory(t)
	path := newTestLog(t, sharingLines(
		`{"timestamp":"2026-06-11T10:00:00Z","event":{"id":"prompt","action":"prompt.submitted","category":"prompt"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"prompt":{"text":"fix the project"}}`,
		`{"timestamp":"2026-06-11T10:01:00Z","event":{"id":"share","action":"session.shared","category":"session"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"raw":{"shared_url":"https://beacon.sh/s/s1","visibility":"direct","shared_through":"2026-06-11T10:00:30Z"}}`,
		`{"timestamp":"2026-06-11T10:02:00Z","event":{"id":"work","action":"command.executed","category":"command"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"command":{"command":"go test ./..."}}`,
	))
	handler, err := Handler(Options{UserMode: true, LogPath: path})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/lens-data?session=s1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var data asymptoteobserve.LensDataV1
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if sharing := data.Trace.Summary.Sharing; sharing.State != "stale" || sharing.RefreshCommand != "beacon share refresh claude_code:s1" {
		t.Fatalf("sharing = %+v, want stale with refresh command", sharing)
	}
}
