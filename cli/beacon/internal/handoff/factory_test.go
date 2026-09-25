package handoff

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

var factoryUpdated = time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)

// factoryFixture lays down a Factory Droid sessions directory in the layout droid writes: one
// directory per working directory, holding each session's transcript and settings.
func factoryFixture(t *testing.T) (StoreDirs, string) {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	isolateRuntimeEnv(t)
	dirs := StoreDirs{HarnessFactory: filepath.Join(t.TempDir(), "sessions")}

	path := filepath.Join(dirs[HarnessFactory], "-work-cli", "factory-sess-1.jsonl")
	writeFixture(t, path,
		jsonLine(t, map[string]interface{}{"type": "session_start", "id": "factory-sess-1", "title": "  Fix the\nflaky test  ", "cwd": "/work/cli"})+
			jsonLine(t, map[string]interface{}{"type": "message", "id": "u1", "timestamp": "2026-09-24T10:59:00.000Z", "message": map[string]interface{}{
				"role": "user", "content": []interface{}{map[string]interface{}{"type": "text", "text": "<user_query>\nfix the flaky test\n</user_query>"}},
			}})+
			jsonLine(t, map[string]interface{}{"type": "message", "id": "a1", "timestamp": "2026-09-24T10:59:30.000Z", "message": map[string]interface{}{
				"role": "assistant", "content": []interface{}{map[string]interface{}{"type": "text", "text": "Fixed."}},
			}}))
	writeFixture(t, filepath.Join(dirs[HarnessFactory], "-work-cli", "factory-sess-1.settings.json"), `{"model":"gpt-5"}`)
	setModTime(t, path, factoryUpdated)
	return dirs, path
}

func TestListReadsFactorySessions(t *testing.T) {
	dirs, path := factoryFixture(t)
	sessions, err := List(DefaultSources(dirs), Filter{Harness: HarnessFactory})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []Session{{Harness: HarnessFactory, ID: "factory-sess-1", Title: "Fix the flaky test", Directory: "/work/cli", SourcePath: path, UpdatedAt: factoryUpdated}}
	if !reflect.DeepEqual(sessions, want) {
		t.Fatalf("sessions = %+v\nwant %+v", sessions, want)
	}
}

func TestParseHarnessNamesFactoryDroid(t *testing.T) {
	for _, name := range []string{"factory", "droid", "Factory-Droid"} {
		if got, err := ParseHarness(name); err != nil || got != HarnessFactory {
			t.Errorf("ParseHarness(%q) = %q, %v; want %s", name, got, err, HarnessFactory)
		}
	}
}

func TestFactorySessionBuildsABrief(t *testing.T) {
	dirs, _ := factoryFixture(t)
	sources := DefaultSources(dirs)
	// A unique prefix finds the session too.
	session, err := Find(sources, HarnessFactory, "factory-sess")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	events, err := Events(sources, session)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	brief := BuildBrief(session, events, FromSessionStore, time.Now())
	if brief.Prompts != 1 || brief.FirstRequest != "fix the flaky test" {
		t.Fatalf("brief prompts=%d first=%q", brief.Prompts, brief.FirstRequest)
	}
	if !strings.Contains(brief.Render(), "Factory Droid") {
		t.Fatalf("the brief should name Factory Droid:\n%s", brief.Render())
	}
}

func TestFactorySessionGoneFromItsStore(t *testing.T) {
	dirs, path := factoryFixture(t)
	session := Session{Harness: HarnessFactory, ID: "factory-sess-1", SourcePath: path + ".moved"}
	if _, err := Events(DefaultSources(dirs), session); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Events = %v, want ErrNotFound", err)
	}
}

func TestPlanResumeReopensAFactorySessionByItsID(t *testing.T) {
	session := resumableSession(t, HarnessFactory, "factory-1")
	plan, err := PlanResume(session, PlanOptions{LookPath: installed("droid")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNative || plan.Executable != "/bin/droid" || !reflect.DeepEqual(plan.Args, []string{"--resume", "factory-1"}) || len(plan.Env) != 0 {
		t.Fatalf("plan = %+v", plan)
	}

	gone := session
	gone.SourcePath = filepath.Join(t.TempDir(), "missing.jsonl")
	brief := filepath.Join(t.TempDir(), "brief.md")
	plan, err = PlanResume(gone, PlanOptions{BriefPath: brief, LookPath: installed("droid")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	// The prompt is droid's only argument: no --auto, so the new session keeps asking.
	if plan.Mode != ModeNewSession || plan.Reason != ReasonSessionGone || !reflect.DeepEqual(plan.Args, []string{NewSessionPrompt(gone, brief)}) {
		t.Fatalf("plan = %+v", plan)
	}
}
