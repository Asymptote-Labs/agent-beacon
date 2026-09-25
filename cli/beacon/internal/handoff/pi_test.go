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

var (
	piUpdated    = time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	primeUpdated = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
)

// piFixture lays down a Pi sessions directory and a Prime Agent agent directory, in the layout each
// runtime writes.
func piFixture(t *testing.T) (StoreDirs, string, string) {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	isolateRuntimeEnv(t)
	root := t.TempDir()
	dirs := StoreDirs{
		HarnessPi:    filepath.Join(root, "pi-sessions"),
		HarnessPrime: filepath.Join(root, "prime-agent"),
	}

	piPath := filepath.Join(dirs[HarnessPi], "--work-api--", "2026-09-24T09-00-00_pi-sess-1.jsonl")
	writeFixture(t, piPath,
		jsonLine(t, map[string]interface{}{"type": "session", "id": "pi-sess-1", "cwd": "/work/api", "git": map[string]interface{}{"branch": "feat/pi"}})+
			jsonLine(t, map[string]interface{}{"type": "model_change", "modelId": "gpt-5"})+
			jsonLine(t, map[string]interface{}{"type": "message", "message": map[string]interface{}{
				"role": "user", "content": []interface{}{map[string]interface{}{"text": "  tidy the\nrouter  "}},
			}})+
			jsonLine(t, map[string]interface{}{"type": "message", "message": map[string]interface{}{
				"role": "assistant", "content": []interface{}{map[string]interface{}{"type": "text", "text": "Done."}},
			}}))
	setModTime(t, piPath, piUpdated)

	primePath := filepath.Join(dirs[HarnessPrime], "sessions", "prime-sess-1.jsonl")
	writeFixture(t, primePath,
		jsonLine(t, map[string]interface{}{"type": "session", "id": "prime-sess-1", "cwd": "/work/web"})+
			jsonLine(t, map[string]interface{}{"type": "message", "message": map[string]interface{}{"role": "user", "content": "plot the latency"}}))
	setModTime(t, primePath, primeUpdated)

	artifactPath := filepath.Join(dirs[HarnessPrime], "session-artifacts", "prime-sess-1", "prime-sub-1.jsonl")
	writeFixture(t, artifactPath,
		jsonLine(t, map[string]interface{}{"type": "session", "id": "prime-sub-1", "cwd": "/work/web", "parentSession": "prime-sess-1"})+
			jsonLine(t, map[string]interface{}{"type": "message", "message": map[string]interface{}{"role": "user", "content": "read the csv"}}))
	setModTime(t, artifactPath, primeUpdated.Add(time.Minute))
	return dirs, piPath, primePath
}

func TestListReadsPiAndPrimeSessions(t *testing.T) {
	dirs, piPath, primePath := piFixture(t)
	sessions, err := List(DefaultSources(dirs), Filter{IncludeSubagents: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byKey := map[string]Session{}
	for _, s := range sessions {
		byKey[s.Harness+"/"+s.ID] = s
	}
	pi, ok := byKey["pi_cli/pi-sess-1"]
	if !ok {
		t.Fatalf("sessions = %v, want the Pi session", sessionKeys(sessions))
	}
	want := Session{Harness: HarnessPi, ID: "pi-sess-1", Title: "tidy the router", Directory: "/work/api", Branch: "feat/pi", SourcePath: piPath, UpdatedAt: piUpdated}
	if !reflect.DeepEqual(pi, want) {
		t.Fatalf("pi session = %+v\nwant %+v", pi, want)
	}
	prime := byKey["prime_agent/prime-sess-1"]
	if prime.Title != "plot the latency" || prime.Directory != "/work/web" || prime.SourcePath != primePath || prime.Subagent || !prime.UpdatedAt.Equal(primeUpdated) {
		t.Fatalf("prime session = %+v", prime)
	}
	sub := byKey["prime_agent/prime-sub-1"]
	if !sub.Subagent || sub.ParentID != "prime-sess-1" || sub.Title != "read the csv" {
		t.Fatalf("prime subagent = %+v, want a subagent of prime-sess-1", sub)
	}

	top, err := List(DefaultSources(dirs), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range top {
		if s.Subagent {
			t.Fatalf("subagent %s listed without IncludeSubagents", s.ID)
		}
	}
	if got := strings.Join(sessionKeys(top), ","); got != "prime_agent/prime-sess-1,pi_cli/pi-sess-1" {
		t.Fatalf("top-level sessions = %s, want newest first", got)
	}
}

func TestPiAndPrimeSessionsBuildABrief(t *testing.T) {
	dirs, _, _ := piFixture(t)
	sources := DefaultSources(dirs)
	for _, tc := range []struct {
		harness, id, request string
	}{
		{HarnessPi, "pi-sess", "tidy the"},
		{HarnessPrime, "prime-sess-1", "plot the latency"},
	} {
		t.Run(tc.harness, func(t *testing.T) {
			// A unique prefix finds the session too.
			session, err := Find(sources, tc.harness, tc.id)
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			events, err := Events(sources, session)
			if err != nil {
				t.Fatalf("Events: %v", err)
			}
			brief := BuildBrief(session, events, FromSessionStore, time.Now())
			if brief.Prompts != 1 || !strings.Contains(brief.FirstRequest, tc.request) {
				t.Fatalf("brief prompts=%d first=%q", brief.Prompts, brief.FirstRequest)
			}
			if !strings.Contains(brief.Render(), RuntimeLabel(tc.harness)) {
				t.Fatalf("the brief should name %s:\n%s", RuntimeLabel(tc.harness), brief.Render())
			}
		})
	}
}

func TestPiSessionGoneFromItsStore(t *testing.T) {
	dirs, piPath, _ := piFixture(t)
	session := Session{Harness: HarnessPi, ID: "pi-sess-1", SourcePath: piPath + ".moved"}
	if _, err := Events(DefaultSources(dirs), session); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Events = %v, want ErrNotFound", err)
	}
}

func TestPlanResumeReopensAPiSessionByItsFile(t *testing.T) {
	session := resumableSession(t, HarnessPi, "pi-1")
	plan, err := PlanResume(session, PlanOptions{LookPath: installed("pi")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNative || plan.Executable != "/bin/pi" || !reflect.DeepEqual(plan.Args, []string{"--session", session.SourcePath}) {
		t.Fatalf("plan = %+v", plan)
	}

	gone := session
	gone.SourcePath = filepath.Join(t.TempDir(), "missing.jsonl")
	brief := filepath.Join(t.TempDir(), "brief.md")
	plan, err = PlanResume(gone, PlanOptions{BriefPath: brief, LookPath: installed("pi")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	// -- keeps a prompt that starts with @ or - from being read as a file or a flag.
	if plan.Mode != ModeNewSession || plan.Reason != ReasonSessionGone || !reflect.DeepEqual(plan.Args, []string{"--", NewSessionPrompt(gone, brief)}) {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanResumeReopensAPrimeSessionByItsFile(t *testing.T) {
	session := resumableSession(t, HarnessPrime, "prime-1")
	plan, err := PlanResume(session, PlanOptions{LookPath: installed("prime-agent")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNative || plan.Executable != "/bin/prime-agent" || !reflect.DeepEqual(plan.Args, []string{"--resume", session.SourcePath}) {
		t.Fatalf("plan = %+v", plan)
	}

	// A subagent transcript continues from a brief, in a new top-level session.
	sub := session
	sub.Subagent, sub.ParentID = true, "prime-0"
	brief := filepath.Join(t.TempDir(), "brief.md")
	plan, err = PlanResume(sub, PlanOptions{BriefPath: brief, LookPath: installed("prime-agent")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNewSession || plan.Reason != ReasonNotResumable || !reflect.DeepEqual(plan.Args, []string{"--", NewSessionPrompt(sub, brief)}) {
		t.Fatalf("plan = %+v", plan)
	}
}
