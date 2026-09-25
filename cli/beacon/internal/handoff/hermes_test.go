package handoff

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

var hermesUpdated = time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)

// hermesFixture writes a Hermes state.db with the columns Hermes 0.19 uses: a titled session with a
// git branch, a compression continuation of it, and a delegate subagent it spawned.
func hermesFixture(t *testing.T) (StoreDirs, string) {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	isolateRuntimeEnv(t)
	path := filepath.Join(t.TempDir(), ".hermes", "state.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := float64(hermesUpdated.Unix())
	for _, stmt := range []string{
		`CREATE TABLE sessions (
			id TEXT PRIMARY KEY, source TEXT NOT NULL, user_id TEXT, model TEXT, model_config TEXT,
			system_prompt TEXT, parent_session_id TEXT, started_at REAL NOT NULL, ended_at REAL,
			end_reason TEXT, message_count INTEGER DEFAULT 0, tool_call_count INTEGER DEFAULT 0,
			input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0, cache_read_tokens INTEGER DEFAULT 0,
			cache_write_tokens INTEGER DEFAULT 0, reasoning_tokens INTEGER DEFAULT 0, cwd TEXT,
			git_branch TEXT, git_repo_root TEXT, estimated_cost_usd REAL, actual_cost_usd REAL,
			cost_status TEXT, cost_source TEXT, title TEXT, archived INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, role TEXT NOT NULL,
			content TEXT, tool_call_id TEXT, tool_calls TEXT, tool_name TEXT, timestamp REAL NOT NULL,
			token_count INTEGER, finish_reason TEXT, reasoning TEXT, reasoning_content TEXT,
			reasoning_details TEXT, platform_message_id TEXT, observed INTEGER DEFAULT 0,
			active INTEGER NOT NULL DEFAULT 1)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		query string
		args  []interface{}
	}{
		// The root was compressed into a continuation, which has no title yet.
		{`INSERT INTO sessions (id, source, model, cwd, git_branch, title, started_at, ended_at, end_reason) VALUES (?, 'cli', 'gpt-5', '/work/api', 'feat/hermes', 'Tidy the router', ?, ?, 'compression')`,
			[]interface{}{"20260924_100000_aaaaaa", at - 3600, at - 1800}},
		{`INSERT INTO sessions (id, source, model, cwd, git_branch, parent_session_id, started_at) VALUES (?, 'cli', 'gpt-5', '/work/api', 'feat/hermes', ?, ?)`,
			[]interface{}{"20260924_103000_bbbbbb", "20260924_100000_aaaaaa", at - 1800}},
		{`INSERT INTO sessions (id, source, cwd, parent_session_id, model_config, started_at) VALUES (?, 'cli', '/work/api', ?, ?, ?)`,
			[]interface{}{"20260924_103500_cccccc", "20260924_103000_bbbbbb", `{"_delegate_from": "20260924_103000_bbbbbb"}`, at - 1500}},
		{`INSERT INTO sessions (id, source, cwd, started_at, archived) VALUES ('20260901_000000_dddddd', 'cli', '/work/old', ?, 1)`,
			[]interface{}{at - 86400}},
		{`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('20260924_100000_aaaaaa', 'user', 'tidy the router', ?)`, []interface{}{at - 3500}},
		{`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('20260924_100000_aaaaaa', 'assistant', 'Reading it now.', ?)`, []interface{}{at - 3400}},
		{`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('20260924_103000_bbbbbb', 'user', ?, ?)`, []interface{}{"  split the\nhandlers  ", at - 1700}},
		{`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('20260924_103000_bbbbbb', 'assistant', 'Done.', ?)`, []interface{}{at}},
		{`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('20260924_103500_cccccc', 'user', 'list the handlers', ?)`, []interface{}{at - 1400}},
	} {
		if _, err := db.Exec(row.query, row.args...); err != nil {
			t.Fatalf("%s: %v", row.query, err)
		}
	}
	return StoreDirs{HarnessHermes: path}, path
}

func TestListReadsHermesSessions(t *testing.T) {
	dirs, path := hermesFixture(t)
	sessions, err := List(DefaultSources(dirs), Filter{Harness: HarnessHermes, IncludeSubagents: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := strings.Join(sessionKeys(sessions), ","); got != "hermes/20260924_103000_bbbbbb,hermes/20260924_103500_cccccc,hermes/20260924_100000_aaaaaa" {
		t.Fatalf("sessions = %s, want newest first and no archived session", got)
	}
	// The continuation has no title of its own, so its first prompt names it. It names a parent, but
	// continues the conversation rather than running a delegated task, so it is not a subagent.
	want := Session{Harness: HarnessHermes, ID: "20260924_103000_bbbbbb", Title: "split the handlers", Directory: "/work/api", Branch: "feat/hermes", SourcePath: path, UpdatedAt: hermesUpdated}
	if !reflect.DeepEqual(sessions[0], want) {
		t.Fatalf("continuation = %+v\nwant %+v", sessions[0], want)
	}
	root := sessions[2]
	if root.Title != "Tidy the router" || root.Subagent || !root.UpdatedAt.Equal(hermesUpdated.Add(-1800*time.Second)) {
		t.Fatalf("root = %+v, want its title and its end time", root)
	}
	sub := sessions[1]
	if !sub.Subagent || sub.ParentID != "20260924_103000_bbbbbb" || sub.Title != "list the handlers" {
		t.Fatalf("delegate = %+v, want a subagent of the continuation", sub)
	}

	top, err := List(DefaultSources(dirs), Filter{Harness: HarnessHermes})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range top {
		if s.Subagent {
			t.Fatalf("subagent %s listed without IncludeSubagents", s.ID)
		}
	}
}

func TestListWithoutAHermesDatabase(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	sessions, err := List(DefaultSources(StoreDirs{}), Filter{Harness: HarnessHermes})
	if err != nil || len(sessions) != 0 {
		t.Fatalf("List = %v, %v; want nothing and no error when Hermes has no state.db", sessions, err)
	}
}

func TestHermesSessionBuildsABrief(t *testing.T) {
	dirs, _ := hermesFixture(t)
	sources := DefaultSources(dirs)
	// A unique prefix finds the session too.
	session, err := Find(sources, HarnessHermes, "20260924_1000")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	events, err := Events(sources, session)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	brief := BuildBrief(session, events, FromSessionStore, time.Now())
	if brief.Prompts != 1 || !strings.Contains(brief.FirstRequest, "tidy the router") {
		t.Fatalf("brief prompts=%d first=%q", brief.Prompts, brief.FirstRequest)
	}
	if rendered := brief.Render(); !strings.Contains(rendered, "Hermes Agent") || !strings.Contains(rendered, "Reading it now.") {
		t.Fatalf("the brief should name Hermes Agent and carry its reply:\n%s", rendered)
	}
}

func TestHermesSessionGoneFromItsStore(t *testing.T) {
	dirs, path := hermesFixture(t)
	sources := DefaultSources(dirs)
	session := Session{Harness: HarnessHermes, ID: "20260924_999999_eeeeee", SourcePath: path}
	if _, err := Events(sources, session); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Events for a deleted session = %v, want ErrNotFound", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	session.ID = "20260924_100000_aaaaaa"
	if _, err := Events(sources, session); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Events without the database = %v, want ErrNotFound", err)
	}
}

func TestPlanResumeReopensAHermesSession(t *testing.T) {
	session := resumableSession(t, HarnessHermes, "20260924_100000_aaaaaa")
	plan, err := PlanResume(session, PlanOptions{LookPath: installed("hermes")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNative || plan.Executable != "/bin/hermes" || !reflect.DeepEqual(plan.Args, []string{"--resume", session.ID}) {
		t.Fatalf("plan = %+v", plan)
	}
	// An inherited HERMES_YOLO_MODE would turn every dangerous-command approval off.
	if !reflect.DeepEqual(plan.Env, map[string]string{"HERMES_YOLO_MODE": ""}) {
		t.Fatalf("env = %v, want HERMES_YOLO_MODE removed", plan.Env)
	}
	for _, arg := range plan.Args {
		if arg == "--yolo" {
			t.Fatal("Beacon never passes --yolo")
		}
	}
}

// Hermes has no interactive start with a first message, so it cannot take over a session from a
// brief; each case says so before looking for a directory or the executable.
func TestPlanResumeHermesCannotStartFromABrief(t *testing.T) {
	brief := filepath.Join(t.TempDir(), "brief.md")
	targets := strings.Join(BriefTargetNames(), ", ")
	for _, tc := range []struct {
		name string
		edit func(*Session, *PlanOptions)
		want string
	}{
		{"another runtime's session", func(s *Session, o *PlanOptions) { s.Harness = HarnessCodex; o.Target = HarnessHermes },
			"Hermes Agent can only reopen its own sessions; it cannot start a new session from a brief (pass --agent with one of: " + targets + ")"},
		{"--new", func(s *Session, o *PlanOptions) { o.ForceNew = true },
			"Hermes Agent cannot start a new session from a brief, which this session needs because a new session was requested; pass --agent to continue in one of: " + targets},
		{"subagent", func(s *Session, o *PlanOptions) { s.Subagent = true },
			"because this runtime's CLI cannot reopen this kind of session"},
		{"database gone", func(s *Session, o *PlanOptions) { s.SourcePath = filepath.Join(t.TempDir(), "state.db") },
			"because the session file is no longer on this machine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := resumableSession(t, HarnessHermes, "20260924_100000_aaaaaa")
			session.Directory = filepath.Join(t.TempDir(), "deleted")
			opts := PlanOptions{BriefPath: brief, LookPath: installed()}
			tc.edit(&session, &opts)
			_, err := PlanResume(session, opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v\nwant it to contain %q", err, tc.want)
			}
		})
	}

	// A Hermes session still continues in another runtime from a brief.
	session := resumableSession(t, HarnessHermes, "20260924_100000_aaaaaa")
	plan, err := PlanResume(session, PlanOptions{Target: HarnessClaude, BriefPath: brief, LookPath: allRuntimes})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNewSession || plan.Executable != "/bin/claude" || !strings.Contains(plan.Args[0], "earlier Hermes Agent session") {
		t.Fatalf("plan = %+v", plan)
	}
}
