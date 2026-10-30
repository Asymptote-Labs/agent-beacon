package handoff

import (
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
