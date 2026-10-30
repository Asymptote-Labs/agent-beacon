package handoff

import (
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

func TestLogSessionsListsByDirectoryNewestFirst(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	write := func(harness, session, cwd, ts, prompt string) {
		t.Helper()
		event := schema.NewEvent(schema.NewEventOptions{Action: "prompt.submitted", Category: "prompt", Harness: schema.HarnessInfo{Name: harness}})
		event.Timestamp = ts
		event.Session = &schema.SessionInfo{ID: session, WorkingDirectory: cwd}
		event.Prompt = &schema.PromptInfo{Text: prompt}
		if _, err := writer.AppendEvent(event, writer.Options{Path: logPath, UserMode: true}); err != nil {
			t.Fatal(err)
		}
	}
	project := filepath.Join(dir, "app")
	write("gemini_cli", "g1", project, "2026-10-01T10:00:00.000000000Z", "first\nprompt")
	write("kiro", "k1", filepath.Join(project, "sub"), "2026-10-01T11:00:00.000000000Z", "second")
	write("kiro", "k2", filepath.Join(dir, "elsewhere"), "2026-10-01T12:00:00.000000000Z", "third")
	write("gemini_cli", "g1", project, "2026-10-01T13:00:00.000000000Z", "later")

	sessions, err := LogSessions(logPath, Filter{Directory: project})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0].ID != "g1" || sessions[1].ID != "k1" {
		t.Fatalf("sessions = %+v", sessions)
	}
	if sessions[0].Title != "first prompt" || sessions[0].Harness != "gemini_cli" {
		t.Fatalf("session = %+v", sessions[0])
	}
	if only, _ := LogSessions(logPath, Filter{Harness: "kiro", Limit: 1}); len(only) != 1 || only[0].ID != "k2" {
		t.Fatalf("kiro sessions = %+v", only)
	}
}

func TestLogSessionsIgnoresBeaconBookkeepingForRecency(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	write := func(action, session, ts string) {
		t.Helper()
		event := schema.NewEvent(schema.NewEventOptions{Action: action, Category: "prompt", Harness: schema.HarnessInfo{Name: "kiro"}})
		event.Timestamp = ts
		event.Session = &schema.SessionInfo{ID: session, WorkingDirectory: dir}
		if _, err := writer.AppendEvent(event, writer.Options{Path: logPath, UserMode: true}); err != nil {
			t.Fatal(err)
		}
	}
	write("prompt.submitted", "older", "2026-10-01T10:00:00.000000000Z")
	write("prompt.submitted", "newer", "2026-10-01T11:00:00.000000000Z")
	write("session.shared", "older", "2026-10-01T12:00:00.000000000Z")
	write("session.commit_linked", "older", "2026-10-01T12:30:00.000000000Z")
	write("session.shared", "bookkeeping-only", "2026-10-01T13:00:00.000000000Z")

	sessions, err := LogSessions(logPath, Filter{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0].ID != "newer" || sessions[1].ID != "older" {
		t.Fatalf("sessions = %+v", sessions)
	}
}

// A directory name may end in a space; the session is listed under that directory, not its
// trimmed sibling, so `beacon share` run there finds it.
func TestLogSessionsKeepsWhitespaceInTheDirectory(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	project := filepath.Join(dir, "app ")
	event := schema.NewEvent(schema.NewEventOptions{Action: "prompt.submitted", Category: "prompt", Harness: schema.HarnessInfo{Name: "kiro"}})
	event.Timestamp = "2026-10-01T10:00:00.000000000Z"
	event.Session = &schema.SessionInfo{ID: "k1", WorkingDirectory: project}
	if _, err := writer.AppendEvent(event, writer.Options{Path: logPath, UserMode: true}); err != nil {
		t.Fatal(err)
	}
	sessions, err := LogSessions(logPath, Filter{Directory: project})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Directory != project {
		t.Fatalf("sessions = %+v, want one in %q", sessions, project)
	}
}
