package handoff

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/cursorsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

var cursorUpdated = time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)

type cursorStores struct {
	dirs      StoreDirs
	workspace string
	cliPath   string
	idePath   string
}

// cursorWorkspace is a directory Cursor's dash-encoded project name decodes back to. The decoding
// splits on dashes, so the path may hold no other punctuation, which rules out macOS and Windows
// temporary directories.
func cursorWorkspace(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows temporary paths do not survive Cursor's project-name encoding")
	}
	root, err := os.MkdirTemp("/tmp", "beaconcursor")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	dir := filepath.Join(root, "work", "api")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// cursorProjectName encodes a workspace path the way Cursor names its project directories.
func cursorProjectName(path string) string {
	return strings.Trim(regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(path, "-"), "-")
}

// writeCursorChatStore lays down the chat store cursor-agent keeps for a chat it started in dir.
func writeCursorChatStore(t *testing.T, dir, id string) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum([]byte(resolved))
	writeFixture(t, filepath.Join(cursorConfigDir(), "chats", hex.EncodeToString(sum[:]), id, "store.db"), "sqlite")
}

// cursorFixture lays down both of Cursor's stores in the layout Cursor writes: Composer records in
// the global storage database, and agent transcripts under the projects directory. cli-1 is a chat
// cursor-agent started, ide-2 an IDE conversation with a subagent, and composer-1 a Composer
// conversation. cli-1 and ide-2 are in the global storage database too.
func cursorFixture(t *testing.T) cursorStores {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	isolateRuntimeEnv(t)
	ws := cursorWorkspace(t)
	f := cursorStores{dirs: StoreDirs{HarnessCursor: filepath.Join(t.TempDir(), "projects")}, workspace: ws}

	transcripts := filepath.Join(f.dirs[HarnessCursor], cursorProjectName(ws), "agent-transcripts")
	f.cliPath = filepath.Join(transcripts, "cli-1", "cli-1.jsonl")
	writeFixture(t, f.cliPath,
		jsonLine(t, map[string]interface{}{"type": "user_message", "id": "u1", "text": "  tidy the\nrouter  ", "timestamp": "2026-09-24T10:58:00Z"})+
			jsonLine(t, map[string]interface{}{"type": "message", "id": "a1", "message": map[string]interface{}{"role": "assistant", "content": []interface{}{map[string]interface{}{"type": "text", "text": "Done."}}}, "timestamp": "2026-09-24T10:59:00Z"}))
	setModTime(t, f.cliPath, cursorUpdated)
	writeCursorChatStore(t, ws, "cli-1")

	f.idePath = filepath.Join(transcripts, "ide-2.jsonl")
	writeFixture(t, f.idePath, jsonLine(t, map[string]interface{}{"type": "user_message", "id": "u1", "text": "plot the latency"}))
	setModTime(t, f.idePath, cursorUpdated.Add(-time.Hour))
	sub := filepath.Join(transcripts, "ide-2", "subagents", "sub-1.jsonl")
	writeFixture(t, sub, jsonLine(t, map[string]interface{}{"type": "user_message", "id": "u1", "text": "read the csv"}))
	setModTime(t, sub, cursorUpdated.Add(-30*time.Minute))

	dbPath := cursorsession.DefaultGlobalDBPath()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	composer := func(id, name string, updated time.Time) string {
		return mustJSON(t, map[string]interface{}{
			"composerId": id, "name": name, "lastUpdatedAt": updated.UnixMilli(),
			"context":                     map[string]interface{}{"path": ws},
			"fullConversationHeadersOnly": []interface{}{map[string]interface{}{"bubbleId": "b1", "type": 1}},
		})
	}
	for key, value := range map[string]string{
		"composerData:composer-1": composer("composer-1", "Investigate the build", cursorUpdated.Add(-2*time.Hour)),
		"bubbleId:composer-1:b1":  `{"type":1,"text":"why is the build red"}`,
		"composerData:cli-1":      composer("cli-1", "Tidy the router", cursorUpdated),
		"bubbleId:cli-1:b1":       `{"type":1,"text":"tidy the router"}`,
		"composerData:ide-2":      composer("ide-2", "Plot the latency", cursorUpdated.Add(-time.Hour)),
		"bubbleId:ide-2:b1":       `{"type":1,"text":"plot the latency"}`,
		// Cursor also keeps a Composer record for a subagent; only its transcript says it is one.
		"composerData:sub-1": composer("sub-1", "read the csv", cursorUpdated.Add(-3*time.Hour)),
		"bubbleId:sub-1:b1":  `{"type":1,"text":"read the csv"}`,
	} {
		if _, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES (?, ?)`, key, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestListReadsCursorSessions(t *testing.T) {
	f := cursorFixture(t)
	sessions, err := List(DefaultSources(f.dirs), Filter{Harness: HarnessCursor, IncludeSubagents: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byID := map[string]Session{}
	for _, s := range sessions {
		byID[s.ID] = s
	}
	if len(sessions) != 4 {
		t.Fatalf("sessions = %v, want each conversation once", sessionKeys(sessions))
	}
	// A chat cursor-agent can reopen is listed as its transcript.
	want := Session{Harness: HarnessCursor, ID: "cli-1", Title: "tidy the router", Directory: f.workspace, SourcePath: f.cliPath, Store: string(cursorsession.SourceTranscript), UpdatedAt: cursorUpdated}
	if !reflect.DeepEqual(byID["cli-1"], want) {
		t.Fatalf("cli session = %+v\nwant %+v", byID["cli-1"], want)
	}
	// An IDE conversation stored twice is listed as its Composer record.
	ide := byID["ide-2"]
	if ide.Store != string(cursorsession.SourceGlobalStorage) || ide.SourcePath != "global:ide-2" || ide.Title != "Plot the latency" || ide.Directory != f.workspace {
		t.Fatalf("ide session = %+v", ide)
	}
	if comp := byID["composer-1"]; comp.Store != string(cursorsession.SourceGlobalStorage) || comp.Title != "Investigate the build" || comp.Subagent {
		t.Fatalf("composer session = %+v", comp)
	}
	if sub := byID["sub-1"]; !sub.Subagent || sub.ParentID != "ide-2" || sub.Title != "read the csv" {
		t.Fatalf("subagent = %+v, want a subagent of ide-2", sub)
	}

	top, err := List(DefaultSources(f.dirs), Filter{Harness: HarnessCursor})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sessionKeys(top), ","); got != "cursor/cli-1,cursor/ide-2,cursor/composer-1" {
		t.Fatalf("top-level sessions = %s, want newest first without the subagent", got)
	}
}

// A transcript's directory is decoded from Cursor's dash-encoded project name, which loses the
// underscore in my_api. The Composer record for the same chat keeps the real path, and that is the
// path cursor-agent keeps the chat under, so the chat is still listed as one it can reopen.
func TestCursorCLIChatTakesItsComposerDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows temporary paths do not survive Cursor's project-name encoding")
	}
	testenv.SetHome(t, t.TempDir())
	isolateRuntimeEnv(t)
	root, err := os.MkdirTemp("/tmp", "beaconcursor")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	ws := filepath.Join(root, "work", "my_api")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(t.TempDir(), "projects")
	path := filepath.Join(projects, cursorProjectName(ws), "agent-transcripts", "cli-9", "cli-9.jsonl")
	writeFixture(t, path, jsonLine(t, map[string]interface{}{"type": "user_message", "id": "u1", "text": "tidy the router"}))
	setModTime(t, path, cursorUpdated)
	writeCursorChatStore(t, ws, "cli-9")

	dbPath := cursorsession.DefaultGlobalDBPath()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"composerData:cli-9": mustJSON(t, map[string]interface{}{
			"composerId": "cli-9", "name": "Tidy the router", "lastUpdatedAt": cursorUpdated.UnixMilli(),
			"context":                     map[string]interface{}{"path": ws},
			"fullConversationHeadersOnly": []interface{}{map[string]interface{}{"bubbleId": "b1", "type": 1}},
		}),
		"bubbleId:cli-9:b1": `{"type":1,"text":"tidy the router"}`,
	} {
		if _, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES (?, ?)`, key, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}

	sessions, err := List(DefaultSources(StoreDirs{HarnessCursor: projects}), Filter{Harness: HarnessCursor})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %v, want the chat once", sessionKeys(sessions))
	}
	chat := sessions[0]
	if chat.Store != string(cursorsession.SourceTranscript) || chat.Directory != ws || chat.SourcePath != path {
		t.Fatalf("chat = %+v, want its transcript in %s", chat, ws)
	}
	plan, err := PlanResume(chat, PlanOptions{LookPath: installed("cursor-agent")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNative || !reflect.DeepEqual(plan.Args, []string{"--resume", "cli-9"}) {
		t.Fatalf("plan = %+v, want a native resume", plan)
	}
}

func TestCursorSessionsBuildABrief(t *testing.T) {
	f := cursorFixture(t)
	sources := DefaultSources(f.dirs)
	for _, tc := range []struct{ id, request string }{
		{"cli-1", "tidy the"},
		// A unique prefix finds the session too.
		{"compose", "why is the build red"},
		{"sub-1", "read the csv"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			session, err := Find(sources, HarnessCursor, tc.id)
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
			if !strings.Contains(brief.Render(), "Cursor") {
				t.Fatalf("the brief should name Cursor:\n%s", brief.Render())
			}
		})
	}
	if session, err := Find(sources, HarnessCursor, "comp"); err == nil {
		t.Fatalf("a prefix under %d characters must not match, got %s", MinPrefixLength, session.ID)
	}
	if session, err := Find(sources, "", "cli-1"); err != nil || session.SourcePath != f.cliPath {
		t.Fatalf("a conversation in both stores must not be ambiguous: %+v, %v", session, err)
	}
}

func TestCursorSessionGoneFromItsStore(t *testing.T) {
	f := cursorFixture(t)
	for _, session := range []Session{
		{Harness: HarnessCursor, ID: "cli-1", SourcePath: f.cliPath + ".moved", Store: string(cursorsession.SourceTranscript)},
		{Harness: HarnessCursor, ID: "gone-1", SourcePath: "global:gone-1", Store: string(cursorsession.SourceGlobalStorage)},
	} {
		if _, err := Events(DefaultSources(f.dirs), session); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Events(%s) = %v, want ErrNotFound", session.SourcePath, err)
		}
	}
}

func TestPlanResumeReopensOnlyCursorCLIChats(t *testing.T) {
	f := cursorFixture(t)
	sources := DefaultSources(f.dirs)
	session, err := Find(sources, HarnessCursor, "cli-1")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanResume(session, PlanOptions{LookPath: installed("cursor-agent")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNative || plan.Executable != "/bin/cursor-agent" || plan.Dir != f.workspace || !reflect.DeepEqual(plan.Args, []string{"--resume", "cli-1"}) {
		t.Fatalf("plan = %+v", plan)
	}

	// IDE conversations, whichever store they are read from, and subagent transcripts continue from
	// a brief.
	ide, err := Find(sources, HarnessCursor, "ide-2")
	if err != nil {
		t.Fatal(err)
	}
	ideTranscript := Session{Harness: HarnessCursor, ID: "ide-2", Directory: f.workspace, SourcePath: f.idePath, Store: string(cursorsession.SourceTranscript)}
	sub, err := Find(sources, HarnessCursor, "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	// A chat id cursor-agent keeps for another directory does not resume here.
	elsewhere := session
	elsewhere.Directory = t.TempDir()
	for name, s := range map[string]Session{"global storage": ide, "ide transcript": ideTranscript, "subagent": sub, "other directory": elsewhere} {
		t.Run(name, func(t *testing.T) {
			brief := filepath.Join(t.TempDir(), "brief.md")
			plan, err := PlanResume(s, PlanOptions{BriefPath: brief, LookPath: installed("cursor-agent")})
			if err != nil {
				t.Fatalf("PlanResume: %v", err)
			}
			if plan.Mode != ModeNewSession || plan.Reason != ReasonNotResumable || !reflect.DeepEqual(plan.Args, []string{NewSessionPrompt(s, brief)}) {
				t.Fatalf("plan = %+v", plan)
			}
		})
	}

	// A chat whose transcript is gone continues from a brief too.
	gone := session
	gone.SourcePath = filepath.Join(t.TempDir(), "missing.jsonl")
	brief := filepath.Join(t.TempDir(), "brief.md")
	plan, err = PlanResume(gone, PlanOptions{BriefPath: brief, LookPath: installed("cursor-agent")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNewSession || plan.Reason != ReasonSessionGone {
		t.Fatalf("plan = %+v", plan)
	}

	// cursor-agent finds the chat from the directory it starts in, so --cwd elsewhere cannot reopen
	// it; the same directory by another path still can.
	moved := t.TempDir()
	plan, err = PlanResume(session, PlanOptions{Dir: moved, BriefPath: brief, LookPath: installed("cursor-agent")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNewSession || plan.Reason != ReasonOtherDirectory || plan.Dir != moved {
		t.Fatalf("--cwd elsewhere: plan = %+v", plan)
	}
	if testenv.HasPOSIXFileModes() {
		link := filepath.Join(t.TempDir(), "workspace-link")
		if err := os.Symlink(f.workspace, link); err != nil {
			t.Fatal(err)
		}
		plan, err = PlanResume(session, PlanOptions{Dir: link, BriefPath: brief, LookPath: installed("cursor-agent")})
		if err != nil || plan.Mode != ModeNative {
			t.Fatalf("--cwd through a symlink to the session's directory: plan = %+v, %v", plan, err)
		}
	}
}
