package handoff

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

var grokUpdated = time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)

const (
	grokSessionID  = "0199a3c4-5e6f-7a8b-9c0d-1e2f3a4b5c6d"
	grokSubagentID = "0199a3c4-7777-7a8b-9c0d-1e2f3a4b5c6d"
	grokLongID     = "0199a3c4-8888-7a8b-9c0d-1e2f3a4b5c6d"
)

// grokFixture lays down a Grok sessions directory in the layout Grok writes: one directory per
// session, named by its id, under a directory named for the URL-encoded working directory.
func grokFixture(t *testing.T) (StoreDirs, string) {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	isolateRuntimeEnv(t)
	root := filepath.Join(t.TempDir(), "sessions")
	dirs := StoreDirs{HarnessGrok: root}

	main := writeGrokSession(t, root, url.PathEscape("/work/api"), grokSessionID, grokUpdated, map[string]interface{}{
		"generated_title": "Tidy the\nrouter", "head_branch": "feat/grok", "current_model_id": "grok-build",
	}, "/work/api", "tidy the router")
	// A subagent is a child session in the same tree, marked by its session kind.
	writeGrokSession(t, root, url.PathEscape("/work/api"), grokSubagentID, grokUpdated.Add(time.Minute), map[string]interface{}{
		"generated_title": "Read the tests", "session_kind": "subagent",
	}, "/work/api", "read the tests")
	// A working directory too long for one directory name is grouped under a slug; the session's
	// prompt_context.json still records it. With no title yet, the first prompt stands in.
	writeGrokSession(t, root, "work-deep-3f9a1c", grokLongID, grokUpdated.Add(-time.Hour), map[string]interface{}{
		"current_model_id": "grok-build",
	}, "/work/"+strings.Repeat("deep/", 60), "  plot the\nlatency  ")
	return dirs, main
}

func writeGrokSession(t *testing.T, root, group, id string, updated time.Time, summary map[string]interface{}, cwd, prompt string) string {
	t.Helper()
	dir := filepath.Join(root, group, id)
	summary["created_at"] = updated.Add(-time.Minute).Format(time.RFC3339Nano)
	summary["updated_at"] = updated.Format(time.RFC3339Nano)
	writeFixture(t, filepath.Join(dir, "summary.json"), mustJSON(t, summary))
	writeFixture(t, filepath.Join(dir, "prompt_context.json"), mustJSON(t, map[string]interface{}{"working_directory": cwd, "os_name": "linux"}))
	writeFixture(t, filepath.Join(dir, "events.jsonl"),
		jsonLine(t, map[string]interface{}{"ts": updated.Add(-time.Minute).Format(time.RFC3339Nano), "type": "turn_started", "session_id": id, "turn_number": 0}))
	writeFixture(t, filepath.Join(dir, "chat_history.jsonl"),
		jsonLine(t, map[string]interface{}{"type": "user", "content": "<system reminder>", "synthetic_reason": "reminder"})+
			jsonLine(t, map[string]interface{}{"type": "user", "content": []interface{}{map[string]interface{}{"type": "text", "text": prompt}}})+
			jsonLine(t, map[string]interface{}{"type": "assistant", "content": "Done."}))
	setModTime(t, dir, updated.Add(-time.Hour))
	return dir
}

func TestListReadsGrokSessions(t *testing.T) {
	dirs, main := grokFixture(t)
	sessions, err := List(DefaultSources(dirs), Filter{Harness: HarnessGrok, IncludeSubagents: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byID := map[string]Session{}
	for _, s := range sessions {
		byID[s.ID] = s
	}
	want := Session{Harness: HarnessGrok, ID: grokSessionID, Title: "Tidy the router", Directory: "/work/api", Branch: "feat/grok", SourcePath: main, UpdatedAt: grokUpdated}
	if got := byID[grokSessionID]; !reflect.DeepEqual(got, want) {
		t.Fatalf("grok session = %+v\nwant %+v", got, want)
	}
	if sub := byID[grokSubagentID]; !sub.Subagent {
		t.Fatalf("grok subagent = %+v, want a subagent", sub)
	}
	long := byID[grokLongID]
	if long.Directory != "/work/"+strings.Repeat("deep/", 60) || long.Title != "plot the latency" {
		t.Fatalf("long-path session = %+v", long)
	}

	top, err := List(DefaultSources(dirs), Filter{Harness: HarnessGrok})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sessionKeys(top), ","); got != "grok/"+grokSessionID+",grok/"+grokLongID {
		t.Fatalf("top-level sessions = %s, want newest first without the subagent", got)
	}
}

func TestListGrokWithoutAStore(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	sessions, err := (&grokSource{dir: filepath.Join(t.TempDir(), "missing")}).List()
	if err != nil || sessions != nil {
		t.Fatalf("List = %v, %v; want nothing and no error", sessions, err)
	}
}

func TestGrokSessionBuildsABrief(t *testing.T) {
	dirs, _ := grokFixture(t)
	sources := DefaultSources(dirs)
	// A unique prefix finds the session too.
	session, err := Find(sources, HarnessGrok, grokSessionID[:12])
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if session.ID != grokSessionID {
		t.Fatalf("Find = %s", session.ID)
	}
	events, err := Events(sources, session)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	brief := BuildBrief(session, events, FromSessionStore, time.Now())
	if brief.Prompts != 1 || brief.FirstRequest != "tidy the router" || brief.LastMessage != "Done." || brief.Branch != "feat/grok" {
		t.Fatalf("brief prompts=%d first=%q last=%q branch=%q", brief.Prompts, brief.FirstRequest, brief.LastMessage, brief.Branch)
	}
	if !strings.Contains(brief.Render(), "Grok Build") {
		t.Fatalf("the brief should name Grok Build:\n%s", brief.Render())
	}
}

func TestGrokSessionGoneFromItsStore(t *testing.T) {
	dirs, main := grokFixture(t)
	session := Session{Harness: HarnessGrok, ID: grokSessionID, SourcePath: main}
	if err := os.RemoveAll(main); err != nil {
		t.Fatal(err)
	}
	if _, err := Events(DefaultSources(dirs), session); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Events = %v, want ErrNotFound", err)
	}
}

func TestPlanResumeReopensAGrokSessionByItsID(t *testing.T) {
	session := resumableSession(t, HarnessGrok, grokSessionID)
	plan, err := PlanResume(session, PlanOptions{LookPath: installed("grok")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	// Grok resolves the id under the directory it runs in, so the plan runs in the session's own.
	want := []string{"--permission-mode", "default", "--resume", grokSessionID}
	if plan.Mode != ModeNative || plan.Executable != "/bin/grok" || plan.Dir != session.Directory || !reflect.DeepEqual(plan.Args, want) {
		t.Fatalf("plan = %+v", plan)
	}

	brief := filepath.Join(t.TempDir(), "brief.md")
	for _, tc := range []struct {
		name   string
		edit   func(*Session)
		reason string
	}{
		{"subagent", func(s *Session) { s.Subagent = true }, ReasonNotResumable},
		{"gone", func(s *Session) { s.SourcePath = filepath.Join(t.TempDir(), "missing") }, ReasonSessionGone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := session
			tc.edit(&s)
			plan, err := PlanResume(s, PlanOptions{BriefPath: brief, LookPath: installed("grok")})
			if err != nil {
				t.Fatalf("PlanResume: %v", err)
			}
			want := []string{"--permission-mode", "default", NewSessionPrompt(s, brief)}
			if plan.Mode != ModeNewSession || plan.Reason != tc.reason || !reflect.DeepEqual(plan.Args, want) {
				t.Fatalf("plan = %+v", plan)
			}
		})
	}
}

// Grok's config can make always-approve the default. No command Beacon runs may inherit it.
func TestEveryGrokCommandAsksBeforeRunningTools(t *testing.T) {
	command, _ := commandFor(HarnessGrok)
	resume, _ := command.Resume(Session{ID: grokSessionID})
	for _, args := range [][]string{resume, command.NewSession("go")} {
		if len(args) < 2 || args[0] != "--permission-mode" || args[1] != "default" {
			t.Fatalf("args %q do not pin the asking permission mode", args)
		}
		for _, arg := range args {
			if arg == "--always-approve" || arg == "--yolo" || arg == "bypassPermissions" {
				t.Fatalf("args %q approve tools on Beacon's say-so", args)
			}
		}
	}
}

func TestParseHarnessAcceptsGrokNames(t *testing.T) {
	for _, name := range []string{"grok", "grok-build", "GROK"} {
		if got, err := ParseHarness(name); err != nil || got != HarnessGrok {
			t.Fatalf("ParseHarness(%q) = %q, %v", name, got, err)
		}
	}
	// Grok Bot is a different product and is not a runtime handoff can read.
	if _, err := ParseHarness("grok_bot"); err == nil {
		t.Fatal("grok_bot must not resolve to Grok Build")
	}
	// A Grok session continued elsewhere is named in the new session's marker.
	if prompt := NewSessionPrompt(Session{Harness: HarnessGrok, ID: grokSessionID}, "/tmp/b.md"); !strings.HasSuffix(prompt, "[beacon-handoff from=grok session="+grokSessionID+"]") {
		t.Fatalf("prompt %q does not end with the Grok marker", prompt)
	}
}

// A child session whose summary names only its kind and branch is still a subagent.
func TestListKeepsAGrokSummaryThatOnlyNamesItsKind(t *testing.T) {
	dirs, _ := grokFixture(t)
	childID := "0199a3c4-9999-7a8b-9c0d-1e2f3a4b5c6d"
	writeGrokSession(t, dirs[HarnessGrok], url.PathEscape("/work/api"), childID, grokUpdated, map[string]interface{}{
		"session_kind": "subagent", "head_branch": "feat/child",
	}, "/work/api", "inspect the logs")
	sessions, err := List(DefaultSources(dirs), Filter{Harness: HarnessGrok, IncludeSubagents: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.ID == childID {
			if !s.Subagent || s.Branch != "feat/child" || s.Title != "inspect the logs" {
				t.Fatalf("child = %+v, want a subagent on feat/child titled by its first prompt", s)
			}
			return
		}
	}
	t.Fatalf("child session not listed: %v", sessionKeys(sessions))
}
