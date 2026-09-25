package handoff

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

var fxUpdated = time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)

const (
	fxSessionA = "1770000000000-1770000000000000000-a1b2c3d4e5f60718"
	fxSessionB = "1770000500000-1770000500000000000-0f1e2d3c4b5a6978"
)

// fxFrame is one events.jsonl line in fx's envelope, fields in fx's own order.
func fxFrame(seq int, kind, payload string) string {
	return fmt.Sprintf(`{"schema_version":1,"log_generation":"1f4b2c8e6d3a5a7c9b613f0d5e8c2a14","seq":%d,"event_id":"%032x","timestamp_ms":%d,"kind":%q,"payload":%s}`+"\n",
		seq, seq, 1770000000000+int64(seq)*1000, kind, payload)
}

func fxStartedFrame(id, workspace string) string {
	return fxFrame(1, "session_started", fmt.Sprintf(`{"id":%q,"created_at_ms":1770000000000,"origin_workspace_root":%q,"workspace_root":%q,"conversation_language":"en","preferences":null,"usage":null}`, id, workspace, workspace))
}

func fxTurnFrame(seq int, prompt, reply string) string {
	return fxFrame(seq, "history_turn_committed", fmt.Sprintf(`{"conversation_language":"en","total_input_tokens":0,"total_output_tokens":0,"work_id":"w",`+
		`"turn":{"kind":"assistant","user":{"text":%q,"images":[]},"assistant":%q,"execution":null}}`, prompt, reply))
}

// fxFixture lays down an fx sessions directory: one session whose manifest records that it was
// rebound to another workspace, and one fx has only just created, with no manifest yet.
func fxFixture(t *testing.T) (StoreDirs, string, string) {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	isolateRuntimeEnv(t)
	sessions := filepath.Join(t.TempDir(), "fx-sessions")
	dirs := StoreDirs{HarnessFx: sessions}

	logA := filepath.Join(sessions, fxSessionA, "events.jsonl")
	writeFixture(t, logA, fxStartedFrame(fxSessionA, "/work/old")+
		fxTurnFrame(2, "  add a\nhealth endpoint  ", "Added GET /healthz.")+
		fxTurnFrame(3, "now run the tests", "They pass."))
	writeFixture(t, filepath.Join(sessions, fxSessionA, "session.json"), fmt.Sprintf(
		`{"schema_version":3,"storage_format":"event_log_v1","id":%q,"log_generation":"1f4b2c8e6d3a5a7c9b613f0d5e8c2a14",`+
			`"created_at_ms":1770000000000,"updated_at_ms":%d,"origin_workspace_root":"/work/old","workspace_root":"/work/api",`+
			`"conversation_language":"en","history_len":2,"total_input_tokens":0,"total_output_tokens":0,"last_event_seq":3,"event_log_bytes":0,"preferences":null}`,
		fxSessionA, fxUpdated.UnixMilli()))

	logB := filepath.Join(sessions, fxSessionB, "events.jsonl")
	writeFixture(t, logB, fxStartedFrame(fxSessionB, "/work/web")+fxTurnFrame(2, "fix the login form", "Fixed."))
	setModTime(t, logB, fxUpdated.Add(-time.Hour))
	return dirs, logA, logB
}

func TestListReadsFxSessions(t *testing.T) {
	dirs, logA, logB := fxFixture(t)
	sessions, err := List(DefaultSources(dirs), Filter{Harness: HarnessFx})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []Session{
		// The manifest's workspace wins over where the session started.
		{Harness: HarnessFx, ID: fxSessionA, Title: "add a health endpoint", Directory: "/work/api", SourcePath: logA, UpdatedAt: fxUpdated},
		// Without a manifest, the session_started event names the workspace and the log's mtime the update.
		{Harness: HarnessFx, ID: fxSessionB, Title: "fix the login form", Directory: "/work/web", SourcePath: logB, UpdatedAt: fxUpdated.Add(-time.Hour)},
	}
	if !reflect.DeepEqual(sessions, want) {
		t.Fatalf("sessions = %+v\nwant %+v", sessions, want)
	}
}

func TestFxStoreMissingListsNothing(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	sessions, err := (&fxSource{dir: filepath.Join(t.TempDir(), "absent")}).List()
	if err != nil || sessions != nil {
		t.Fatalf("List = %v, %v; want nil, nil", sessions, err)
	}
	// The default store is under HOME, which here has none.
	sessions, err = (&fxSource{}).List()
	if err != nil || sessions != nil {
		t.Fatalf("default List = %v, %v; want nil, nil", sessions, err)
	}
}

func TestFxSessionBuildsABrief(t *testing.T) {
	dirs, _, _ := fxFixture(t)
	sources := DefaultSources(dirs)
	// A unique prefix finds the session too.
	session, err := Find(sources, HarnessFx, fxSessionA[:20])
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if session.ID != fxSessionA {
		t.Fatalf("Find = %s, want %s", session.ID, fxSessionA)
	}
	events, err := Events(sources, session)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	brief := BuildBrief(session, events, FromSessionStore, time.Now())
	if brief.Prompts != 2 || !strings.Contains(brief.FirstRequest, "health endpoint") {
		t.Fatalf("brief prompts=%d first=%q", brief.Prompts, brief.FirstRequest)
	}
	rendered := brief.Render()
	if !strings.Contains(rendered, "They pass.") || !strings.Contains(rendered, RuntimeLabel(HarnessFx)) {
		t.Fatalf("brief is missing the last reply or the runtime:\n%s", rendered)
	}
}

func TestFxSessionGoneFromItsStore(t *testing.T) {
	dirs, logA, _ := fxFixture(t)
	for _, session := range []Session{
		{Harness: HarnessFx, ID: fxSessionA, SourcePath: logA + ".moved"},
		{Harness: HarnessFx, ID: "1770000900000-gone", SourcePath: logA},
	} {
		if _, err := Events(DefaultSources(dirs), session); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Events(%s) = %v, want ErrNotFound", session.ID, err)
		}
	}
}

func TestPlanResumeReopensAnFxSessionInItsWorkspace(t *testing.T) {
	session := resumableSession(t, HarnessFx, fxSessionA)
	plan, err := PlanResume(session, PlanOptions{LookPath: installed("fx")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	// fx scopes sessions to a workspace, so it must start in the session's.
	if plan.Mode != ModeNative || plan.Executable != "/bin/fx" || !reflect.DeepEqual(plan.Args, []string{"--resume", fxSessionA}) || plan.Dir != session.Directory {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanResumeRefusesToStartANewFxSession(t *testing.T) {
	brief := filepath.Join(t.TempDir(), "brief.md")
	fxSession := resumableSession(t, HarnessFx, fxSessionA)
	gone := fxSession
	gone.SourcePath = filepath.Join(t.TempDir(), "missing", "events.jsonl")
	for _, tc := range []struct {
		name    string
		session Session
		opts    PlanOptions
		reason  string
	}{
		{"another runtime's session", resumableSession(t, HarnessClaude, "claude-1"), PlanOptions{Target: HarnessFx}, ReasonOtherRuntime},
		{"a new session requested", fxSession, PlanOptions{ForceNew: true}, ReasonRequested},
		{"the session file is gone", gone, PlanOptions{}, ReasonSessionGone},
		{"known only from the runtime log", fxSession, PlanOptions{FromRuntimeLog: true}, ReasonFromRuntimeLog},
		// fx resumes only sessions of the workspace it starts in.
		{"--cwd outside its workspace", fxSession, PlanOptions{Dir: t.TempDir()}, ReasonOtherDirectory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.BriefPath, tc.opts.LookPath = brief, allRuntimes
			plan, err := PlanResume(tc.session, tc.opts)
			if err == nil {
				t.Fatalf("PlanResume = %+v, want an error: fx cannot start a session from a brief", plan)
			}
			if !strings.Contains(err.Error(), "fx cannot start a new session from a brief") || !strings.Contains(err.Error(), ReasonText(tc.reason)) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	// fx's own sessions still continue in a runtime that can start one.
	plan, err := PlanResume(fxSession, PlanOptions{Target: HarnessClaude, BriefPath: brief, LookPath: allRuntimes})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNewSession || plan.Executable != "/bin/claude" || !reflect.DeepEqual(plan.Args, []string{NewSessionPrompt(fxSession, brief)}) {
		t.Fatalf("plan = %+v", plan)
	}
}
