package handoff

import (
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/openclawsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

var openClawUpdated = time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)

type openClawPaths struct {
	main, loose, mainShared, workShared string
}

// openClawFixture lays down an OpenClaw state directory with two agent profiles, each a sessions
// directory of transcripts and the sessions.json index that files them under Gateway session keys.
func openClawFixture(t *testing.T) (StoreDirs, openClawPaths) {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	isolateRuntimeEnv(t)
	state := filepath.Join(t.TempDir(), "openclaw-state")
	dirs := StoreDirs{HarnessOpenClaw: state}
	mainDir := filepath.Join(state, "agents", "main", "sessions")
	workDir := filepath.Join(state, "agents", "work", "sessions")
	paths := openClawPaths{
		main:       filepath.Join(mainDir, "oc-sess-1.jsonl"),
		loose:      filepath.Join(mainDir, "oc-loose-2.jsonl"),
		mainShared: filepath.Join(mainDir, "shared-1.jsonl"),
		workShared: filepath.Join(workDir, "shared-1.jsonl"),
	}
	transcript := func(id, cwd, at, prompt, reply string) string {
		lines := jsonLine(t, map[string]interface{}{"type": "session", "id": id, "timestamp": at, "cwd": cwd})
		if prompt != "" {
			lines += jsonLine(t, map[string]interface{}{"type": "message", "id": "u1", "timestamp": at, "message": map[string]interface{}{
				"role": "user", "content": []interface{}{map[string]interface{}{"type": "text", "text": prompt}},
			}})
		}
		return lines + jsonLine(t, map[string]interface{}{"type": "message", "id": "a1", "timestamp": at, "message": map[string]interface{}{
			"role": "assistant", "content": []interface{}{map[string]interface{}{"type": "text", "text": reply}},
		}})
	}
	writeFixture(t, paths.main, transcript("oc-sess-1", "/work/api", "2026-09-24T10:59:00Z", "  plan the\nrelease  ", "Plan ready."))
	writeFixture(t, paths.loose, transcript("oc-loose-2", "/work/api", "2026-09-24T08:00:00Z", "", "Heartbeat ok."))
	// A transcript outside the index is dated by its file.
	setModTime(t, paths.loose, openClawUpdated.Add(-3*time.Hour))
	writeFixture(t, paths.mainShared, transcript("shared-1", "/work/memo", "2026-09-24T07:00:00Z", "draft the memo", "Drafted."))
	writeFixture(t, paths.workShared, transcript("shared-1", "/work/inbox", "2026-09-24T06:00:00Z", "triage the inbox", "Triaged."))
	writeFixture(t, filepath.Join(mainDir, "sessions.json"), mustJSON(t, map[string]interface{}{
		"agent:main:main": map[string]interface{}{"sessionId": "oc-sess-1", "sessionFile": "oc-sess-1.jsonl", "updatedAt": openClawUpdated.Format(time.RFC3339), "workspaceDir": "/work/api"},
		"agent:main:memo": map[string]interface{}{"sessionId": "shared-1", "sessionFile": "shared-1.jsonl", "updatedAt": "2026-09-24T07:00:00Z"},
	}))
	// A legacy index key that names no agent.
	writeFixture(t, filepath.Join(workDir, "sessions.json"), mustJSON(t, map[string]interface{}{
		"discord:channel:9": map[string]interface{}{"sessionId": "shared-1", "sessionFile": paths.workShared, "updatedAt": "2026-09-24T06:00:00Z"},
	}))
	return dirs, paths
}

func TestListReadsOpenClawSessions(t *testing.T) {
	dirs, paths := openClawFixture(t)
	sessions, err := List(DefaultSources(dirs), Filter{Harness: HarnessOpenClaw})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	bySource := map[string]Session{}
	for _, s := range sessions {
		bySource[s.SourcePath] = s
	}
	if len(sessions) != 4 {
		t.Fatalf("sessions = %v, want 4", sessionKeys(sessions))
	}
	want := Session{
		Harness: HarnessOpenClaw, ID: "oc-sess-1", Title: "plan the release", Directory: "/work/api",
		SourcePath: paths.main, Key: "agent:main:main", UpdatedAt: openClawUpdated,
	}
	if got := bySource[paths.main]; !reflect.DeepEqual(got, want) {
		t.Fatalf("main session = %+v\nwant %+v", got, want)
	}
	// A transcript outside the index has no key, and one with no prompt no title.
	if loose := bySource[paths.loose]; loose.ID != "oc-loose-2" || loose.Title != "" || loose.Key != "" {
		t.Fatalf("loose session = %+v", loose)
	}
	if s := bySource[paths.mainShared]; s.Key != "agent:main:memo" || s.Title != "draft the memo" {
		t.Fatalf("main shared session = %+v", s)
	}
	// An index key that names no agent is not passed on.
	if s := bySource[paths.workShared]; s.Key != "" || s.Title != "triage the inbox" || s.Directory != "/work/inbox" {
		t.Fatalf("work shared session = %+v", s)
	}
	if got := sessions[0].ID; got != "oc-sess-1" {
		t.Fatalf("newest session = %s, want oc-sess-1", got)
	}
}

func TestListWithoutAnOpenClawStore(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	isolateRuntimeEnv(t)
	sessions, err := (&openClawSource{dir: filepath.Join(t.TempDir(), "missing")}).List()
	if err != nil || sessions != nil {
		t.Fatalf("List = %v, %v; want nothing", sessions, err)
	}
}

func TestOpenClawSessionsBuildABrief(t *testing.T) {
	dirs, _ := openClawFixture(t)
	sources := DefaultSources(dirs)
	// A unique prefix finds the session too.
	session, err := Find(sources, HarnessOpenClaw, "oc-sess")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	events, err := Events(sources, session)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	brief := BuildBrief(session, events, FromSessionStore, time.Now())
	if brief.Prompts != 1 || !strings.Contains(brief.FirstRequest, "plan the") {
		t.Fatalf("brief prompts=%d first=%q", brief.Prompts, brief.FirstRequest)
	}
	if !strings.Contains(brief.Render(), "OpenClaw Gateway") {
		t.Fatalf("the brief should name OpenClaw Gateway:\n%s", brief.Render())
	}
}

func TestOpenClawIDsAreUniqueOnlyWithinAProfile(t *testing.T) {
	dirs, paths := openClawFixture(t)
	sources := DefaultSources(dirs)
	var ambiguous *AmbiguousError
	if _, err := Find(sources, HarnessOpenClaw, "shared-1"); !errors.As(err, &ambiguous) || len(ambiguous.Candidates) != 2 {
		t.Fatalf("Find = %v, want both profiles' shared-1", err)
	}
	for path, prompt := range map[string]string{paths.mainShared: "draft the memo", paths.workShared: "triage the inbox"} {
		events, err := Events(sources, Session{Harness: HarnessOpenClaw, ID: "shared-1", SourcePath: path})
		if err != nil {
			t.Fatalf("Events(%s): %v", path, err)
		}
		brief := BuildBrief(Session{Harness: HarnessOpenClaw, ID: "shared-1"}, events, FromSessionStore, time.Now())
		if brief.FirstRequest != prompt {
			t.Fatalf("%s first request = %q, want %q", path, brief.FirstRequest, prompt)
		}
	}
}

func TestOpenClawSessionGoneFromItsStore(t *testing.T) {
	dirs, paths := openClawFixture(t)
	session := Session{Harness: HarnessOpenClaw, ID: "oc-sess-1", SourcePath: paths.main + ".moved"}
	if _, err := Events(DefaultSources(dirs), session); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Events = %v, want ErrNotFound", err)
	}
}

func TestPlanResumeReopensAnOpenClawSessionByItsKey(t *testing.T) {
	session := resumableSession(t, HarnessOpenClaw, "oc-1")
	session.Key = "agent:main:discord:channel:9"
	plan, err := PlanResume(session, PlanOptions{LookPath: installed("openclaw")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNative || plan.Executable != "/bin/openclaw" || !reflect.DeepEqual(plan.Args, []string{"tui", "--session", "agent:main:discord:channel:9"}) {
		t.Fatalf("plan = %+v", plan)
	}

	// Without a key the Gateway has no name for the conversation, so it continues from a brief.
	keyless := session
	keyless.Key = ""
	brief := filepath.Join(t.TempDir(), "brief.md")
	plan, err = PlanResume(keyless, PlanOptions{BriefPath: brief, LookPath: installed("openclaw")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	prompt := NewSessionPrompt(keyless, brief)
	if plan.Mode != ModeNewSession || plan.Reason != ReasonNotResumable || !reflect.DeepEqual(plan.Args, []string{"tui", "--session", openClawNewSessionKey(prompt), "--message", prompt}) {
		t.Fatalf("plan = %+v", plan)
	}
	for _, arg := range plan.Args {
		if arg == "--deliver" {
			t.Fatal("a handoff must not deliver replies through the Gateway's channels")
		}
	}

	gone := session
	gone.SourcePath = filepath.Join(t.TempDir(), "missing.jsonl")
	plan, err = PlanResume(gone, PlanOptions{BriefPath: brief, LookPath: installed("openclaw")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNewSession || plan.Reason != ReasonSessionGone {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestOpenClawNewSessionKeyIsFreshPerBrief(t *testing.T) {
	session := Session{Harness: HarnessCodex, ID: "019a-thread"}
	first := openClawNewSessionKey(NewSessionPrompt(session, "/h/codex_cli-019a-thread-20260924T090000.000Z.md"))
	second := openClawNewSessionKey(NewSessionPrompt(session, "/h/codex_cli-019a-thread-20260924T090000.001Z.md"))
	if first == second {
		t.Fatalf("two briefs of one session share the key %s", first)
	}
	// Lower case, so the Gateway's key normalisation leaves it as written, and never the shared
	// main session.
	if !regexp.MustCompile(`^beacon-handoff-[0-9a-f]{16}$`).MatchString(first) {
		t.Fatalf("key = %q", first)
	}
}

func TestOpenClawSessionKeyNeedsItsOwnAgent(t *testing.T) {
	for _, tc := range []struct {
		key, profile, want string
	}{
		{"agent:main:main", "main", "agent:main:main"},
		{"agent:work:discord:channel:9", "work", "agent:work:discord:channel:9"},
		{"agent:other:main", "main", ""},
		{"main", "main", ""},
		{"discord:channel:9", "work", ""},
		{"agent:main:", "main", ""},
		{"", "main", ""},
	} {
		ref := openclawsession.TraceRef{SessionKey: tc.key, Profile: tc.profile}
		if got := openClawSessionKey(ref); got != tc.want {
			t.Errorf("openClawSessionKey(%q in %s) = %q, want %q", tc.key, tc.profile, got, tc.want)
		}
	}
}
