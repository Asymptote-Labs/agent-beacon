package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTraceSharingStateAndRefreshCommand(t *testing.T) {
	path := newTestLog(t, parityFixture())
	withoutHistory(t)
	list, err := ReadTraceList(path, TraceQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]TraceSummaryV1{}
	for _, trace := range list.Traces {
		byID[trace.ID] = trace
	}
	stale := byID["session:claude_code:s7"].Sharing
	if stale.State != "stale" || stale.URL != "https://beacon.sh/s/s7trace" || stale.RefreshCommand != "beacon share refresh claude_code:s7" {
		t.Fatalf("s7 sharing = %+v", stale)
	}
	current := byID["session:claude_code:s8"].Sharing
	if current.State != "shared" || current.Visibility != "private" || current.RefreshCommand == "" {
		t.Fatalf("s8 sharing = %+v (a commit link must not make it stale)", current)
	}
	if local := byID["session:opencode:s5"].Sharing; local.State != "local_only" || local.RefreshCommand != "" {
		t.Fatalf("s5 sharing = %+v", local)
	}
}

func TestSessionsCarrySharingState(t *testing.T) {
	path := newTestLog(t, parityFixture())
	result, err := ReadSessions(path, EventQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*SessionSharing{}
	for _, s := range result.Sessions {
		got[s.ID] = s.Sharing
	}
	if s := got["s7"]; s == nil || s.State != "stale" || s.RefreshCommand != "beacon share refresh claude_code:s7" {
		t.Fatalf("s7 = %+v", s)
	}
	if s := got["s8"]; s == nil || s.State != "shared" || s.Visibility != "private" {
		t.Fatalf("s8 = %+v", s)
	}
	if got["s5"] != nil {
		t.Fatalf("s5 = %+v, want no sharing", got["s5"])
	}
	detail, ok, err := ReadSessionDetail(path, "s7")
	if err != nil || !ok || detail.Session.Sharing == nil || detail.Session.Sharing.State != "stale" {
		t.Fatalf("detail = %+v %v %v", detail.Session.Sharing, ok, err)
	}
}

func sharingLines(lines ...string) [][]byte {
	out := make([][]byte, len(lines))
	for i, l := range lines {
		out[i] = []byte(l)
	}
	return out
}

func sessionSharing(t *testing.T, path string, query EventQuery) map[string]*SessionSharing {
	t.Helper()
	query.Limit = 50
	result, err := ReadSessions(path, query)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*SessionSharing{}
	for _, s := range result.Sessions {
		got[s.ID] = s.Sharing
	}
	return got
}

// A session's sharing state comes from all of its events, not the ones the filters kept: a share
// record outside the time range or another model's events still marks it shared, and activity
// outside them still makes the share stale.
func TestSessionSharingIgnoresFilters(t *testing.T) {
	path := newTestLog(t, sharingLines(
		// sh1: shared at 10:01, nothing after.
		`{"timestamp":"2026-06-11T10:00:00Z","event":{"id":"a1","action":"prompt.submitted","category":"prompt"},"harness":{"name":"claude_code"},"model":"opus","session":{"id":"sh1"},"prompt":{"text":"one"}}`,
		`{"timestamp":"2026-06-11T10:01:00Z","event":{"id":"a2","action":"session.shared","category":"session","fidelity":"inferred"},"harness":{"name":"claude_code"},"session":{"id":"sh1"},"raw":{"shared_url":"https://beacon.sh/s/sh1","visibility":"direct","shared_at":"2026-06-11T10:01:00Z","last_refreshed_at":"2026-06-11T10:01:00Z"}}`,
		// sh2: shared at 10:01, then activity at 12:00 on another model.
		`{"timestamp":"2026-06-11T10:00:00Z","event":{"id":"b1","action":"prompt.submitted","category":"prompt"},"harness":{"name":"claude_code"},"model":"opus","session":{"id":"sh2"},"prompt":{"text":"two"}}`,
		`{"timestamp":"2026-06-11T10:01:00Z","event":{"id":"b2","action":"session.shared","category":"session","fidelity":"inferred"},"harness":{"name":"claude_code"},"session":{"id":"sh2"},"raw":{"shared_url":"https://beacon.sh/s/sh2","visibility":"direct","shared_at":"2026-06-11T10:01:00Z","last_refreshed_at":"2026-06-11T10:01:00Z"}}`,
		`{"timestamp":"2026-06-11T12:00:00Z","event":{"id":"b3","action":"command.executed","category":"command"},"harness":{"name":"claude_code"},"model":"sonnet","session":{"id":"sh2"}}`,
	))
	withoutHistory(t)
	for name, query := range map[string]EventQuery{
		"time range": {Since: mustTime(t, "2026-06-11T09:59:00Z"), Until: mustTime(t, "2026-06-11T10:00:30Z")},
		"model":      {Model: "opus"},
		"none":       {},
	} {
		got := sessionSharing(t, path, query)
		if s := got["sh1"]; s == nil || s.State != "shared" || s.URL != "https://beacon.sh/s/sh1" {
			t.Fatalf("%s: sh1 = %+v, want shared", name, s)
		}
		if s := got["sh2"]; s == nil || s.State != "stale" {
			t.Fatalf("%s: sh2 = %+v, want stale from activity the filter hides", name, s)
		}
	}
}

// A share record says what its upload covered (shared_through, when the session was read): an
// event logged between that read and the record was not uploaded, so the share is stale even
// though the record is newer. A session followed with refreshes after its last events is not.
func TestSharingStalenessUsesSharedThrough(t *testing.T) {
	path := newTestLog(t, sharingLines(
		// st1: read at 10:01, event at 10:01:30 during the upload, record at 10:02.
		`{"timestamp":"2026-06-11T10:00:00Z","event":{"id":"c1","action":"prompt.submitted","category":"prompt"},"harness":{"name":"claude_code"},"session":{"id":"st1"},"prompt":{"text":"one"}}`,
		`{"timestamp":"2026-06-11T10:01:30Z","event":{"id":"c2","action":"agent.message","category":"agent"},"harness":{"name":"claude_code"},"session":{"id":"st1"}}`,
		`{"timestamp":"2026-06-11T10:02:00Z","event":{"id":"c3","action":"session.shared","category":"session","fidelity":"inferred"},"harness":{"name":"claude_code"},"session":{"id":"st1"},"raw":{"shared_url":"https://beacon.sh/s/st1","visibility":"direct","shared_at":"2026-06-11T10:02:00Z","last_refreshed_at":"2026-06-11T10:02:00Z","shared_through":"2026-06-11T10:01:00Z"}}`,
		// st2: followed; each upload's refresh covers the events before it.
		`{"timestamp":"2026-06-11T11:00:00Z","event":{"id":"d1","action":"prompt.submitted","category":"prompt"},"harness":{"name":"claude_code"},"session":{"id":"st2"},"prompt":{"text":"two"}}`,
		`{"timestamp":"2026-06-11T11:00:05Z","event":{"id":"d2","action":"session.shared","category":"session","fidelity":"inferred"},"harness":{"name":"claude_code"},"session":{"id":"st2"},"raw":{"shared_url":"https://beacon.sh/s/st2","visibility":"direct","shared_at":"2026-06-11T11:00:05Z","last_refreshed_at":"2026-06-11T11:00:05Z","shared_through":"2026-06-11T11:00:04Z"}}`,
		`{"timestamp":"2026-06-11T11:00:20Z","event":{"id":"d3","action":"agent.message","category":"agent"},"harness":{"name":"claude_code"},"session":{"id":"st2"}}`,
		`{"timestamp":"2026-06-11T11:00:31Z","event":{"id":"d4","action":"session.shared","category":"session","fidelity":"inferred"},"harness":{"name":"claude_code"},"session":{"id":"st2"},"raw":{"shared_url":"https://beacon.sh/s/st2","visibility":"direct","last_refreshed_at":"2026-06-11T11:00:31Z","shared_through":"2026-06-11T11:00:30Z"}}`,
	))
	withoutHistory(t)
	got := sessionSharing(t, path, EventQuery{})
	if s := got["st1"]; s == nil || s.State != "stale" {
		t.Fatalf("st1 = %+v, want stale: an event came in during the upload", s)
	}
	if s := got["st2"]; s == nil || s.State != "shared" {
		t.Fatalf("st2 = %+v, want shared: the follow's refresh covered every event", s)
	}
	list, err := ReadTraceList(path, TraceQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]TraceSummaryV1{}
	for _, trace := range list.Traces {
		byID[trace.ID] = trace
	}
	if s := byID["session:claude_code:st1"].Sharing.State; s != "stale" {
		t.Fatalf("trace st1 sharing = %q, want stale", s)
	}
	if s := byID["session:claude_code:st2"].Sharing.State; s != "shared" {
		t.Fatalf("trace st2 sharing = %q, want shared", s)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A share made from inside the session is followed by the runtime's events for the `beacon share`
// command and the agent's reply about it; those do not make it stale. A later unrelated tool call
// does.
func TestShareFromInsideTheSessionIsNotStale(t *testing.T) {
	inside := []string{
		`{"timestamp":"2026-06-11T10:00:00Z","event":{"id":"e1","action":"prompt.submitted","category":"prompt"},"harness":{"name":"claude_code"},"session":{"id":"in1"},"prompt":{"id":"p1","text":"share this session"}}`,
		`{"timestamp":"2026-06-11T10:00:05Z","event":{"id":"e2","action":"tool.invoked","category":"tool"},"harness":{"name":"claude_code"},"session":{"id":"in1"},"prompt":{"id":"p1"},"tool":{"name":"Bash","command":"beacon share --current"},"gen_ai":{"tool":{"call":{"id":"call-share"}}}}`,
		`{"timestamp":"2026-06-11T10:00:09Z","event":{"id":"e3","action":"session.shared","category":"session","fidelity":"inferred"},"harness":{"name":"claude_code"},"session":{"id":"in1"},"raw":{"shared_url":"https://beacon.sh/s/in1","visibility":"direct","shared_at":"2026-06-11T10:00:09Z","last_refreshed_at":"2026-06-11T10:00:09Z","shared_through":"2026-06-11T10:00:06Z"}}`,
		`{"timestamp":"2026-06-11T10:00:10Z","event":{"id":"e4","action":"tool.completed","category":"tool"},"harness":{"name":"claude_code"},"session":{"id":"in1"},"prompt":{"id":"p1"},"tool":{"name":"Bash"},"gen_ai":{"tool":{"call":{"id":"call-share"}}}}`,
		`{"timestamp":"2026-06-11T10:00:11Z","event":{"id":"e5","action":"command.executed","category":"command"},"harness":{"name":"claude_code"},"session":{"id":"in1"},"prompt":{"id":"p1"},"command":{"command":"beacon share --current"}}`,
		`{"timestamp":"2026-06-11T10:00:15Z","event":{"id":"e6","action":"agent.message","category":"agent"},"harness":{"name":"claude_code"},"session":{"id":"in1"},"prompt":{"id":"p1"},"message":"Shared: https://beacon.sh/s/in1"}`,
		// Claude Code's Stop hook ends the turn.
		`{"timestamp":"2026-06-11T10:00:16Z","event":{"id":"e6s","action":"tool.completed","category":"tool"},"harness":{"name":"claude_code","collection_method":"hook"},"session":{"id":"in1"},"prompt":{"id":"p1"},"message":"Agent response completed"}`,
	}
	check := func(t *testing.T, lines []string, want string) {
		t.Helper()
		path := newTestLog(t, sharingLines(lines...))
		withoutHistory(t)
		if s := sessionSharing(t, path, EventQuery{})["in1"]; s == nil || s.State != want {
			t.Fatalf("session sharing = %+v, want %s", s, want)
		}
		list, err := ReadTraceList(path, TraceQuery{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		for _, trace := range list.Traces {
			if trace.ID == "session:claude_code:in1" && trace.Sharing.State != want {
				t.Fatalf("trace sharing = %+v, want %s", trace.Sharing, want)
			}
		}
	}
	t.Run("share and its reply", func(t *testing.T) { check(t, inside, "shared") })
	t.Run("without turn ids", func(t *testing.T) {
		var bare []string
		for _, line := range inside {
			bare = append(bare, strings.ReplaceAll(line, `"prompt":{"id":"p1"},`, ""))
		}
		check(t, bare, "shared")
	})
	t.Run("later tool call", func(t *testing.T) {
		check(t, append(append([]string{}, inside...),
			`{"timestamp":"2026-06-11T10:05:00Z","event":{"id":"e7","action":"tool.invoked","category":"tool"},"harness":{"name":"claude_code"},"session":{"id":"in1"},"prompt":{"id":"p1"},"tool":{"name":"Edit","path":"main.go"},"gen_ai":{"tool":{"call":{"id":"call-edit"}}}}`,
		), "stale")
	})
	t.Run("turn end after a later prompt", func(t *testing.T) {
		check(t, append(append([]string{}, inside...),
			`{"timestamp":"2026-06-11T10:05:00Z","event":{"id":"e9","action":"prompt.submitted","category":"prompt"},"harness":{"name":"claude_code"},"session":{"id":"in1"},"prompt":{"id":"p3","text":"one more thing"}}`,
			`{"timestamp":"2026-06-11T10:05:30Z","event":{"id":"e10","action":"tool.completed","category":"tool"},"harness":{"name":"claude_code","collection_method":"hook"},"session":{"id":"in1"},"prompt":{"id":"p3"},"message":"Agent response completed"}`,
		), "stale")
	})
	t.Run("reply after later work", func(t *testing.T) {
		check(t, append(append([]string{}, inside...),
			`{"timestamp":"2026-06-11T10:05:00Z","event":{"id":"e8","action":"prompt.submitted","category":"prompt"},"harness":{"name":"claude_code"},"session":{"id":"in1"},"prompt":{"id":"p2","text":"now fix the bug"}}`,
		), "stale")
	})
}

// A share made from outside the session (another terminal, --follow) does not hide the agent's
// later output: it was not uploaded, so the share is stale.
func TestShareFromOutsideTheSessionGoesStaleOnAReply(t *testing.T) {
	path := newTestLog(t, sharingLines(
		`{"timestamp":"2026-06-11T10:00:00Z","event":{"id":"o1","action":"prompt.submitted","category":"prompt"},"harness":{"name":"claude_code"},"session":{"id":"out1"},"prompt":{"text":"work"}}`,
		`{"timestamp":"2026-06-11T10:00:09Z","event":{"id":"o2","action":"session.shared","category":"session","fidelity":"inferred"},"harness":{"name":"claude_code"},"session":{"id":"out1"},"raw":{"shared_url":"https://beacon.sh/s/out1","visibility":"direct","shared_at":"2026-06-11T10:00:09Z","last_refreshed_at":"2026-06-11T10:00:09Z","shared_through":"2026-06-11T10:00:06Z"}}`,
		`{"timestamp":"2026-06-11T10:00:15Z","event":{"id":"o3","action":"agent.message","category":"agent"},"harness":{"name":"claude_code"},"session":{"id":"out1"},"message":"Here is the answer."}`,
	))
	withoutHistory(t)
	if s := sessionSharing(t, path, EventQuery{})["out1"]; s == nil || s.State != "stale" {
		t.Fatalf("session sharing = %+v, want stale", s)
	}
	list, err := ReadTraceList(path, TraceQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, trace := range list.Traces {
		if trace.ID == "session:claude_code:out1" && trace.Sharing.State != "stale" {
			t.Fatalf("trace sharing = %+v, want stale", trace.Sharing)
		}
	}
}

// Sharing state folds a session's events in append order across rotated archives, so the session
// list, the session page and the trace list agree after a rotation.
func TestSharingStateAcrossARotatedLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.jsonl")
	archived := []string{
		`{"timestamp":"2026-06-11T10:00:00Z","event":{"id":"r1","action":"prompt.submitted","category":"prompt"},"harness":{"name":"claude_code"},"session":{"id":"rot1"},"prompt":{"text":"share this"}}`,
		`{"timestamp":"2026-06-11T10:00:05Z","event":{"id":"r2","action":"tool.invoked","category":"tool"},"harness":{"name":"claude_code"},"session":{"id":"rot1"},"tool":{"name":"Bash","command":"beacon share"},"gen_ai":{"tool":{"call":{"id":"call-r"}}}}`,
		`{"timestamp":"2026-06-11T10:00:09Z","event":{"id":"r3","action":"session.shared","category":"session","fidelity":"inferred"},"harness":{"name":"claude_code"},"session":{"id":"rot1"},"raw":{"shared_url":"https://beacon.sh/s/rot1","visibility":"direct","shared_at":"2026-06-11T10:00:09Z","last_refreshed_at":"2026-06-11T10:00:09Z","shared_through":"2026-06-11T10:00:06Z"}}`,
	}
	active := []string{
		`{"timestamp":"2026-06-11T10:00:10Z","event":{"id":"r4","action":"tool.completed","category":"tool"},"harness":{"name":"claude_code"},"session":{"id":"rot1"},"tool":{"name":"Bash"},"gen_ai":{"tool":{"call":{"id":"call-r"}}}}`,
		`{"timestamp":"2026-06-11T10:00:15Z","event":{"id":"r5","action":"agent.message","category":"agent"},"harness":{"name":"claude_code"},"session":{"id":"rot1"},"message":"Shared."}`,
	}
	if err := os.WriteFile(path+".1", []byte(strings.Join(archived, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(active, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withoutHistory(t)
	if s := sessionSharing(t, path, EventQuery{})["rot1"]; s == nil || s.State != "shared" {
		t.Fatalf("ReadSessions sharing = %+v, want shared", s)
	}
	detail, ok, err := ReadSessionDetail(path, "rot1")
	if err != nil || !ok || detail.Session.Sharing == nil || detail.Session.Sharing.State != "shared" {
		t.Fatalf("ReadSessionDetail sharing = %+v (%v %v), want shared", detail.Session.Sharing, ok, err)
	}
	list, err := ReadTraceList(path, TraceQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, trace := range list.Traces {
		if trace.ID == "session:claude_code:rot1" {
			found = true
			if trace.Sharing.State != "shared" {
				t.Fatalf("trace sharing = %+v, want shared", trace.Sharing)
			}
		}
	}
	if !found {
		t.Fatal("trace list missed rot1")
	}
}

// "stale" is a kind of "shared": --state shared lists a shared session with newer activity, and
// --state stale narrows to it, on the JSONL scan and on the history alike.
func TestStateSharedIncludesStale(t *testing.T) {
	path := newTestLog(t, parityFixture())
	ids := func(state string) map[string]bool {
		t.Helper()
		list, err := ReadTraceList(path, TraceQuery{State: state, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, trace := range list.Traces {
			got[trace.ID] = true
		}
		return got
	}
	check := func(name string) {
		t.Helper()
		shared, stale, upper := ids("shared"), ids("stale"), ids("SHARED")
		if !shared["session:claude_code:s7"] || !shared["session:claude_code:s8"] {
			t.Fatalf("%s: --state shared = %v, want the stale s7 and the current s8", name, shared)
		}
		if !stale["session:claude_code:s7"] || stale["session:claude_code:s8"] {
			t.Fatalf("%s: --state stale = %v, want only s7", name, stale)
		}
		if len(upper) != len(shared) {
			t.Fatalf("%s: --state SHARED = %v, want the same as shared", name, upper)
		}
		if local := ids("local_only"); local["session:claude_code:s7"] || local["session:claude_code:s8"] {
			t.Fatalf("%s: --state local_only = %v", name, local)
		}
	}
	withoutHistory(t)
	check("jsonl")
	withHistory(t)
	optIn(t, path)
	check("history")
}

// OpenCode ends a turn with agent.response and an idle session.status. Neither makes a share run
// from inside the session stale (the projection skips status events, so a refresh would send
// nothing); a later real tool call does.
func TestOpenCodeShareFromInsideTheSessionIsNotStale(t *testing.T) {
	inside := []string{
		`{"timestamp":"2026-06-11T10:00:00Z","event":{"id":"q1","action":"prompt.submitted","category":"prompt"},"harness":{"name":"opencode","collection_method":"plugin"},"session":{"id":"oc1"},"prompt":{"text":"share this session"}}`,
		`{"timestamp":"2026-06-11T10:00:05Z","event":{"id":"q2","action":"command.invoked","category":"command"},"harness":{"name":"opencode","collection_method":"plugin"},"session":{"id":"oc1"},"command":{"command":"beacon share --current --harness opencode --json"},"gen_ai":{"tool":{"call":{"id":"oc-call"}}}}`,
		`{"timestamp":"2026-06-11T10:00:09Z","event":{"id":"q3","action":"session.shared","category":"session","fidelity":"inferred"},"harness":{"name":"opencode"},"session":{"id":"oc1"},"raw":{"shared_url":"https://beacon.sh/s/oc1","visibility":"direct","shared_at":"2026-06-11T10:00:09Z","last_refreshed_at":"2026-06-11T10:00:09Z","shared_through":"2026-06-11T10:00:06Z"}}`,
		`{"timestamp":"2026-06-11T10:00:12Z","event":{"id":"q4","action":"agent.response","category":"session"},"harness":{"name":"opencode","collection_method":"plugin"},"session":{"id":"oc1"},"message":"opencode assistant response captured"}`,
		`{"timestamp":"2026-06-11T10:00:13Z","event":{"id":"q5","action":"session.status","category":"session"},"harness":{"name":"opencode","collection_method":"plugin"},"session":{"id":"oc1"},"message":"idle"}`,
	}
	check := func(t *testing.T, lines []string, want string) {
		t.Helper()
		path := newTestLog(t, sharingLines(lines...))
		withoutHistory(t)
		if s := sessionSharing(t, path, EventQuery{})["oc1"]; s == nil || s.State != want {
			t.Fatalf("session sharing = %+v, want %s", s, want)
		}
		list, err := ReadTraceList(path, TraceQuery{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		for _, trace := range list.Traces {
			if trace.ID == "session:opencode:oc1" && trace.Sharing.State != want {
				t.Fatalf("trace sharing = %+v, want %s", trace.Sharing, want)
			}
		}
	}
	t.Run("share, reply and idle", func(t *testing.T) { check(t, inside, "shared") })
	t.Run("later tool call", func(t *testing.T) {
		check(t, append(append([]string{}, inside...),
			`{"timestamp":"2026-06-11T10:04:00Z","event":{"id":"q6","action":"tool.invoked","category":"tool"},"harness":{"name":"opencode","collection_method":"plugin"},"session":{"id":"oc1"},"tool":{"name":"edit","path":"main.go"},"gen_ai":{"tool":{"call":{"id":"oc-edit"}}}}`,
		), "stale")
	})
}

func TestIsBeaconShareCommand(t *testing.T) {
	for command, want := range map[string]bool{
		`beacon share --current`:                             true,
		`cd x && beacon share refresh --harness claude_code`: true,
		`/opt/beacon/bin/beacon share`:                       true,
		`BEACON_X=1 beacon share`:                            true,
		`./beacon share --json`:                              true,
		`C:\Tools\beacon.exe share`:                          true,
		`sudo beacon share --session abc`:                    true,
		"go test ./...\nbeacon share":                        true,
		// Captures cut short: the share call before the cut still counts.
		"beacon share --current --json 'unterminated":             true,
		"beacon share --current && cat <<EOF\nlong text":          true,
		"git commit -m 'beacon share":                             false,
		`& "C:\Tools\beacon.exe" share`:                           false, // PowerShell, not bash
		`rg "beacon share"`:                                       false,
		`git commit -m 'beacon share now records shared_through'`: false,
		`git commit -m 'fix; beacon share'`:                       false,
		`echo beacon share`:                                       false,
		`beacon shares`:                                           false,
		`beacon git setup --share-sessions`:                       false,
		``:                                                        false,
	} {
		if got := isBeaconShareCommand(command); got != want {
			t.Errorf("isBeaconShareCommand(%q) = %v, want %v", command, got, want)
		}
	}
}

// The session list buffers records for the sharing state only for sessions that were shared, and
// the session-state filter does no sharing work at all.
func TestSharingBuffersOnlySharedSessions(t *testing.T) {
	buffered := 0
	prev := sharingRecordBuffered
	sharingRecordBuffered = func() { buffered++ }
	t.Cleanup(func() { sharingRecordBuffered = prev })
	withoutHistory(t)

	unshared := newTestLog(t, sharingLines(
		`{"timestamp":"2026-06-11T10:00:00Z","event":{"id":"u1","action":"prompt.submitted","category":"prompt"},"harness":{"name":"claude_code"},"session":{"id":"nu1"},"prompt":{"text":"hello"}}`,
		`{"timestamp":"2026-06-11T10:00:05Z","event":{"id":"u2","action":"command.executed","category":"command"},"harness":{"name":"claude_code"},"session":{"id":"nu1"},"command":{"command":"echo beacon share"}}`,
	))
	if got := sessionSharing(t, unshared, EventQuery{}); got["nu1"] != nil {
		t.Fatalf("unshared session sharing = %+v", got["nu1"])
	}
	if buffered != 0 {
		t.Fatalf("buffered %d records for a log with no shares", buffered)
	}

	path := newTestLog(t, parityFixture())
	if _, err := sessionIDsForState(path, EventQuery{}, "captured"); err != nil {
		t.Fatal(err)
	}
	if buffered != 0 {
		t.Fatalf("the session-state filter buffered %d records", buffered)
	}
	got := sessionSharing(t, path, EventQuery{})
	if got["s7"] == nil || got["s8"] == nil {
		t.Fatalf("shared sessions missing: %+v", got)
	}
	shared := 0
	for _, line := range parityFixture() {
		if strings.Contains(string(line), `"id":"s7"`) || strings.Contains(string(line), `"id":"s8"`) {
			shared++
		}
	}
	if buffered != shared {
		t.Fatalf("buffered %d records, want only the shared sessions' %d", buffered, shared)
	}
}

// Events with one timestamp are ordered by their writer's sequence, not by their place in the file,
// in the session list as on the session page: the slim records the list folds keep the sequence.
// Here the share call, its record and the agent's reply share a timestamp but were written in the
// reverse order; by sequence the reply follows the record and is the share's own, so the session is
// shared, not stale, in both views.
func TestSessionSharingOrdersTiesBySequenceInBothViews(t *testing.T) {
	path := newTestLog(t, sharingLines(
		`{"timestamp":"2026-06-11T10:00:00Z","event":{"id":"q1","action":"prompt.submitted","category":"prompt"},"harness":{"name":"claude_code"},"session":{"id":"seq1"},"prompt":{"text":"share this session"}}`,
		`{"timestamp":"2026-06-11T10:00:09Z","sequence":3,"event":{"id":"q4","action":"agent.message","category":"agent"},"harness":{"name":"claude_code"},"session":{"id":"seq1"},"message":"Shared: https://beacon.sh/s/seq1"}`,
		`{"timestamp":"2026-06-11T10:00:09Z","sequence":2,"event":{"id":"q3","action":"session.shared","category":"session","fidelity":"inferred"},"harness":{"name":"claude_code"},"session":{"id":"seq1"},"raw":{"shared_url":"https://beacon.sh/s/seq1","visibility":"direct","shared_at":"2026-06-11T10:00:09Z","last_refreshed_at":"2026-06-11T10:00:09Z","shared_through":"2026-06-11T10:00:05Z"}}`,
		`{"timestamp":"2026-06-11T10:00:09Z","sequence":1,"event":{"id":"q2","action":"tool.invoked","category":"tool"},"harness":{"name":"claude_code"},"session":{"id":"seq1"},"tool":{"name":"Bash","command":"beacon share --current"},"gen_ai":{"tool":{"call":{"id":"call-seq"}}}}`,
	))
	withoutHistory(t)
	listed := sessionSharing(t, path, EventQuery{})["seq1"]
	detail, ok, err := ReadSessionDetail(path, "seq1")
	if err != nil || !ok {
		t.Fatalf("detail: ok=%v err=%v", ok, err)
	}
	if listed == nil || detail.Session.Sharing == nil || listed.State != detail.Session.Sharing.State || listed.State != "shared" {
		t.Fatalf("list sharing = %+v, page sharing = %+v, want both shared", listed, detail.Session.Sharing)
	}
}
