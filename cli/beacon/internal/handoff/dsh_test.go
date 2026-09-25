package handoff

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

var dshUpdated = time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)

// dshRecord is one line of a DeepSeek Harness session record file.
func dshRecord(t *testing.T, kind string, data map[string]interface{}) string {
	return jsonLine(t, map[string]interface{}{"type": kind, "time": "2026-09-24T10:59:00Z", "data": data})
}

// writeDSHSession writes a session record file as DSH does: one zstd frame per append, in the
// session's own directory under $DSH_HOME/sessions, beside its lock file.
func writeDSHSession(t *testing.T, dir string, updated time.Time, lines ...string) string {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	var frames []byte
	for _, line := range lines {
		frames = enc.EncodeAll([]byte(line), frames)
	}
	path := filepath.Join(dir, "session.v3.jsonl.zstd")
	writeFixture(t, filepath.Join(dir, "session.lock"), "")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, frames, 0o644); err != nil {
		t.Fatal(err)
	}
	setModTime(t, path, updated)
	return path
}

// dshFixture lays down a DSH_HOME holding a titled session, a subagent it spawned, and a fork of it.
func dshFixture(t *testing.T) (StoreDirs, string) {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	isolateRuntimeEnv(t)
	home := filepath.Join(t.TempDir(), "dsh-home")
	dirs := StoreDirs{HarnessDSH: home}
	sessions := filepath.Join(home, "sessions", "--work-api--")

	main := writeDSHSession(t, filepath.Join(sessions, "dsh-sess-1"), dshUpdated,
		dshRecord(t, "session", map[string]interface{}{"id": "dsh-sess-1", "cwd": "/work/api", "isSeeded": false, "delegationDepth": 0}),
		dshRecord(t, "user/message", map[string]interface{}{"message": map[string]interface{}{"content": "add a rate limiter"}}),
		dshRecord(t, "session/title", map[string]interface{}{"title": "Add a\nrate limiter"}),
		dshRecord(t, "assistant/message", map[string]interface{}{"message": map[string]interface{}{
			"content": []interface{}{map[string]interface{}{"type": "text", "text": "Added a token bucket."}},
		}}))

	writeDSHSession(t, filepath.Join(sessions, "dsh-sub-1"), dshUpdated.Add(time.Minute),
		dshRecord(t, "session", map[string]interface{}{"id": "dsh-sub-1", "cwd": "/work/api", "parentSession": "dsh-sess-1", "origin": "subagent", "isSeeded": false, "delegationDepth": 1}),
		dshRecord(t, "user/message", map[string]interface{}{"message": map[string]interface{}{"content": "find the request handlers"}}))

	// A fork also names its parent session, but it is a session of its own.
	writeDSHSession(t, filepath.Join(sessions, "dsh-fork-1"), dshUpdated.Add(2*time.Minute),
		dshRecord(t, "session", map[string]interface{}{"id": "dsh-fork-1", "cwd": "/work/api", "parentSession": "dsh-sess-1", "isSeeded": true, "delegationDepth": 0}),
		dshRecord(t, "user/message", map[string]interface{}{"message": map[string]interface{}{"content": "try a sliding window instead"}}))
	return dirs, main
}

func TestListReadsDSHSessions(t *testing.T) {
	dirs, main := dshFixture(t)
	sessions, err := List(DefaultSources(dirs), Filter{Harness: HarnessDSH, IncludeSubagents: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byID := map[string]Session{}
	for _, s := range sessions {
		byID[s.ID] = s
	}
	want := Session{Harness: HarnessDSH, ID: "dsh-sess-1", Title: "Add a rate limiter", Directory: "/work/api", SourcePath: main, UpdatedAt: dshUpdated}
	if got := byID["dsh-sess-1"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("dsh session = %+v\nwant %+v", got, want)
	}
	if sub := byID["dsh-sub-1"]; !sub.Subagent || sub.ParentID != "dsh-sess-1" || sub.Title != "find the request handlers" {
		t.Fatalf("dsh subagent = %+v, want a subagent of dsh-sess-1", sub)
	}
	if fork := byID["dsh-fork-1"]; fork.Subagent || fork.ParentID != "" || fork.Title != "try a sliding window instead" {
		t.Fatalf("dsh fork = %+v, want a top-level session", fork)
	}

	top, err := List(DefaultSources(dirs), Filter{Harness: HarnessDSH})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sessionKeys(top), ","); got != "deepseek_harness/dsh-fork-1,deepseek_harness/dsh-sess-1" {
		t.Fatalf("top-level sessions = %s, want the fork and the session, newest first", got)
	}
}

func TestListWithoutADSHStore(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	isolateRuntimeEnv(t)
	sessions, err := (&dshSource{dir: filepath.Join(t.TempDir(), "missing")}).List()
	if err != nil || sessions != nil {
		t.Fatalf("List = %v, %v; want nothing and no error", sessions, err)
	}
}

func TestDSHHomeComesFromTheEnvironment(t *testing.T) {
	dirs, main := dshFixture(t)
	t.Setenv("DSH_HOME", dirs[HarnessDSH])
	session, err := Find(DefaultSources(StoreDirs{}), HarnessDSH, "dsh-sess-1")
	if err != nil || session.SourcePath != main {
		t.Fatalf("Find = %+v, %v", session, err)
	}
}

func TestDSHSessionBuildsABrief(t *testing.T) {
	dirs, _ := dshFixture(t)
	sources := DefaultSources(dirs)
	// A unique prefix finds the session too.
	session, err := Find(sources, HarnessDSH, "dsh-ses")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	events, err := Events(sources, session)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	brief := BuildBrief(session, events, FromSessionStore, time.Now())
	if brief.Prompts != 1 || !strings.Contains(brief.FirstRequest, "add a rate limiter") || !strings.Contains(brief.LastMessage, "token bucket") {
		t.Fatalf("brief prompts=%d first=%q last=%q", brief.Prompts, brief.FirstRequest, brief.LastMessage)
	}
	if !strings.Contains(brief.Render(), "DeepSeek Harness") {
		t.Fatalf("the brief should name DeepSeek Harness:\n%s", brief.Render())
	}
}

func TestDSHSessionGoneFromItsStore(t *testing.T) {
	dirs, main := dshFixture(t)
	session := Session{Harness: HarnessDSH, ID: "dsh-sess-1", SourcePath: main + ".moved"}
	if _, err := Events(DefaultSources(dirs), session); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Events = %v, want ErrNotFound", err)
	}
}

func TestPlanResumeContinuesADSHSessionElsewhere(t *testing.T) {
	session := resumableSession(t, HarnessDSH, "dsh-1")
	// Beacon cannot start dsh, so its sessions need --agent.
	_, err := PlanResume(session, PlanOptions{BriefPath: filepath.Join(t.TempDir(), "b.md"), LookPath: allRuntimes})
	if err == nil || !strings.Contains(err.Error(), "cannot start DeepSeek Harness") || !strings.Contains(err.Error(), "--agent") {
		t.Fatalf("resume without --agent err = %v", err)
	}
	if _, err := PlanResume(resumableSession(t, HarnessClaude, "s-1"), PlanOptions{Target: HarnessDSH, LookPath: allRuntimes}); err == nil || !strings.Contains(err.Error(), "DeepSeek Harness cannot be started") {
		t.Fatalf("--agent dsh err = %v", err)
	}

	brief := filepath.Join(t.TempDir(), "brief.md")
	plan, err := PlanResume(session, PlanOptions{Target: HarnessClaude, BriefPath: brief, LookPath: allRuntimes})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNewSession || plan.Reason != ReasonOtherRuntime || plan.Executable != "/bin/claude" || len(plan.Args) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	if !strings.HasSuffix(plan.Args[0], "[beacon-handoff from=deepseek_harness session=dsh-1]") {
		t.Fatalf("the prompt should carry the handoff marker: %q", plan.Args[0])
	}
	if info, ok := asymptoteobserve.ParseHandoffMarker(plan.Args[0]); !ok || info.SourceHarness != HarnessDSH || info.SourceSessionID != "dsh-1" {
		t.Fatalf("marker = %+v, %v", info, ok)
	}
}

func TestParseHarnessAcceptsDSHNames(t *testing.T) {
	for _, name := range []string{"dsh", "deepseek_harness", "DeepSeek-Harness"} {
		if got, err := ParseHarness(name); err != nil || got != HarnessDSH {
			t.Fatalf("ParseHarness(%q) = %q, %v", name, got, err)
		}
	}
	// Bare "deepseek" names the vendor. A log row stamped with it stays as it is rather than being
	// read as DeepSeek Harness, and it is not accepted as a runtime name.
	if got := canonicalHarness("deepseek"); got != "deepseek" {
		t.Fatalf("canonicalHarness(deepseek) = %q, want it left alone", got)
	}
	if got, err := ParseHarness("deepseek"); err == nil {
		t.Fatalf("ParseHarness(deepseek) = %q, want an error", got)
	}
	if strings.Contains(strings.Join(StartableNames(), ","), "dsh") {
		t.Fatalf("StartableNames = %v, want no dsh", StartableNames())
	}
}
