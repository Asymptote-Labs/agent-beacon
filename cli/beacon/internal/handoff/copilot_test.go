package handoff

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

var (
	copilotNamedUpdated   = time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)
	copilotUnnamedUpdated = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
)

// copilotFixture lays down a Copilot directory in the layout the CLI writes: one directory per
// session under session-state, holding workspace.yaml and events.jsonl. The second session has no
// workspace.yaml yet, so its listing comes from the log's head.
func copilotFixture(t *testing.T) (StoreDirs, string, string) {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	isolateRuntimeEnv(t)
	dirs := StoreDirs{HarnessCopilot: filepath.Join(t.TempDir(), ".copilot")}

	named := filepath.Join(dirs[HarnessCopilot], "session-state", "0cb916db-26aa-40f2-86b5-1ba81b225fd2")
	writeFixture(t, filepath.Join(named, "workspace.yaml"), strings.Join([]string{
		"id: 0cb916db-26aa-40f2-86b5-1ba81b225fd2",
		"cwd: /work/api",
		"git_root: /work/api",
		"repository: acme/api",
		"branch: feat/copilot",
		"name: Fix the\n  flaky test",
		"summary_count: 0",
		"",
	}, "\n"))
	namedPath := filepath.Join(named, "events.jsonl")
	writeFixture(t, namedPath,
		jsonLine(t, map[string]interface{}{"id": "s1", "type": "session.start", "data": map[string]interface{}{
			"sessionId": "0cb916db-26aa-40f2-86b5-1ba81b225fd2", "context": map[string]interface{}{"cwd": "/work/api", "branch": "feat/copilot"},
		}})+
			jsonLine(t, map[string]interface{}{"id": "u1", "type": "user.message", "data": map[string]interface{}{"content": "the payments test is flaky"}})+
			jsonLine(t, map[string]interface{}{"id": "a1", "type": "assistant.message", "data": map[string]interface{}{"content": "Looking."}}))
	setModTime(t, namedPath, copilotNamedUpdated)

	unnamedPath := filepath.Join(dirs[HarnessCopilot], "session-state", "7f3e2a10-0000-4000-8000-000000000001", "events.jsonl")
	writeFixture(t, unnamedPath,
		jsonLine(t, map[string]interface{}{"id": "s1", "type": "session.start", "data": map[string]interface{}{
			"sessionId": "7f3e2a10-0000-4000-8000-000000000001", "context": map[string]interface{}{"cwd": "/work/web", "branch": "main"},
		}})+
			jsonLine(t, map[string]interface{}{"id": "u1", "type": "user.message", "data": map[string]interface{}{"content": "  add a\ndark mode  "}}))
	setModTime(t, unnamedPath, copilotUnnamedUpdated)
	return dirs, namedPath, unnamedPath
}

func TestListReadsCopilotSessions(t *testing.T) {
	dirs, namedPath, unnamedPath := copilotFixture(t)
	sessions, err := List(DefaultSources(dirs), Filter{Harness: HarnessCopilot})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []Session{
		{Harness: HarnessCopilot, ID: "7f3e2a10-0000-4000-8000-000000000001", Title: "add a dark mode", Directory: "/work/web", Branch: "main", SourcePath: unnamedPath, UpdatedAt: copilotUnnamedUpdated},
		{Harness: HarnessCopilot, ID: "0cb916db-26aa-40f2-86b5-1ba81b225fd2", Title: "Fix the flaky test", Directory: "/work/api", Branch: "feat/copilot", SourcePath: namedPath, UpdatedAt: copilotNamedUpdated},
	}
	if !reflect.DeepEqual(sessions, want) {
		t.Fatalf("sessions = %+v\nwant %+v", sessions, want)
	}
}

func TestListWithoutACopilotStore(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	sessions, err := (&copilotSource{dir: filepath.Join(t.TempDir(), "missing")}).List()
	if err != nil || sessions != nil {
		t.Fatalf("List = %v, %v; want nothing and no error", sessions, err)
	}
}

func TestCopilotSessionBuildsABrief(t *testing.T) {
	dirs, _, _ := copilotFixture(t)
	sources := DefaultSources(dirs)
	// A unique prefix finds the session too.
	session, err := Find(sources, HarnessCopilot, "0cb916db")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	events, err := Events(sources, session)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	brief := BuildBrief(session, events, FromSessionStore, time.Now())
	if brief.Prompts != 1 || !strings.Contains(brief.FirstRequest, "payments test is flaky") {
		t.Fatalf("brief prompts=%d first=%q", brief.Prompts, brief.FirstRequest)
	}
	if !strings.Contains(brief.Render(), "GitHub Copilot CLI") {
		t.Fatalf("the brief should name GitHub Copilot CLI:\n%s", brief.Render())
	}
}

func TestCopilotSessionGoneFromItsStore(t *testing.T) {
	dirs, namedPath, _ := copilotFixture(t)
	session, err := Find(DefaultSources(dirs), HarnessCopilot, "0cb916db-26aa-40f2-86b5-1ba81b225fd2")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(namedPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := Events(DefaultSources(dirs), session); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Events = %v, want ErrNotFound", err)
	}
}

func TestPlanResumeReopensACopilotSession(t *testing.T) {
	session := resumableSession(t, HarnessCopilot, "0cb916db-26aa")
	plan, err := PlanResume(session, PlanOptions{LookPath: installed("copilot")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNative || plan.Executable != "/bin/copilot" || !reflect.DeepEqual(plan.Args, []string{"--resume=0cb916db-26aa"}) {
		t.Fatalf("plan = %+v", plan)
	}
	// Copilot's approvals stay on whatever the calling shell exports.
	wantEnv := map[string]string{"COPILOT_ALLOW_ALL": "", "COPILOT_PLAN_THEN_AUTOPILOT": ""}
	if !reflect.DeepEqual(plan.Env, wantEnv) {
		t.Fatalf("env = %v, want %v", plan.Env, wantEnv)
	}

	gone := session
	gone.SourcePath = filepath.Join(t.TempDir(), "missing", "events.jsonl")
	brief := filepath.Join(t.TempDir(), "brief.md")
	plan, err = PlanResume(gone, PlanOptions{BriefPath: brief, LookPath: installed("copilot")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNewSession || plan.Reason != ReasonSessionGone || !reflect.DeepEqual(plan.Args, []string{"-i", NewSessionPrompt(gone, brief)}) {
		t.Fatalf("plan = %+v", plan)
	}
	if !reflect.DeepEqual(plan.Env, wantEnv) {
		t.Fatalf("new-session env = %v, want %v", plan.Env, wantEnv)
	}
}
