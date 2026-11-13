package workspacespool

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

func testEvent(harness, sessionID, workspace, action string) schema.Event {
	event := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: "agent",
		Severity: schema.SeverityInfo,
		Harness:  schema.HarnessInfo{Name: harness, CollectionMethod: asymptoteobserve.CollectionMethodHook},
		Message:  action,
	})
	event.Session = &schema.SessionInfo{ID: sessionID, WorkingDirectory: workspace}
	return event
}

func line(t *testing.T, event schema.Event) []byte {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func stageAt(t *testing.T, dir string, lines ...[]byte) localingest.Spool {
	t.Helper()
	spool := localingest.Spool{Dir: dir}
	if dropped, err := spool.Append(lines); err != nil || dropped != 0 {
		t.Fatalf("stage: dropped %d, err %v", dropped, err)
	}
	return spool
}

func stage(t *testing.T, s Session, lines ...[]byte) localingest.Spool {
	t.Helper()
	dir, ok := asymptoteobserve.WorkspaceSpoolDir(s.Workspace, s.Harness, s.ID)
	if !ok {
		t.Fatalf("unsafe test session %+v", s)
	}
	return stageAt(t, dir, lines...)
}

func pending(t *testing.T, spool localingest.Spool) int {
	t.Helper()
	lines, _, err := spool.Pending()
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

func collect(events *[]schema.Event) Emit {
	return func(event schema.Event) error {
		*events = append(*events, event)
		return nil
	}
}

func TestDrainSessionAdmitsOnlyTheSessionsOwnHookEvents(t *testing.T) {
	ws := t.TempDir()
	s := Session{Workspace: ws, Harness: "opencode", ID: "s1"}
	beaconOnly := testEvent("opencode", "s1", ws, "session.share_requested")
	poll := testEvent("opencode", "s1", ws, "prompt.submitted")
	poll.Harness.CollectionMethod = schema.CollectionMethodPoll
	spool := stage(t, s,
		line(t, testEvent("opencode", "s1", ws, "session.started")),
		line(t, testEvent("opencode", "other", ws, "prompt.submitted")),
		line(t, testEvent("codex", "s1", ws, "prompt.submitted")),
		line(t, beaconOnly),
		line(t, poll),
		[]byte(`{"torn":`),
	)

	var events []schema.Event
	var summary Summary
	if err := DrainSession(s, collect(&events), &summary); err != nil {
		t.Fatal(err)
	}
	if summary != (Summary{Sessions: 1, Events: 1, Rejected: 5}) || len(events) != 1 || events[0].Event.Action != "session.started" {
		t.Fatalf("summary %+v events %d, want one admitted and five refused", summary, len(events))
	}
	if pending(t, spool) != 0 || PendingBytes(s) != 0 {
		t.Fatal("drained and refused lines were left staged")
	}
}

func TestDrainSessionStopsOnEmitErrorAndKeepsTheRest(t *testing.T) {
	ws := t.TempDir()
	s := Session{Workspace: ws, Harness: "opencode", ID: "s1"}
	spool := stage(t, s,
		line(t, testEvent("opencode", "s1", ws, "session.started")),
		line(t, testEvent("opencode", "s1", ws, "prompt.submitted")),
	)
	calls := 0
	emit := func(schema.Event) error {
		calls++
		if calls == 2 {
			return writer.ErrBudgetSpent
		}
		return nil
	}
	var summary Summary
	err := Drain([]Session{s, {Workspace: ws, Harness: "opencode", ID: "s2"}}, emit, &summary)
	if !errors.Is(err, writer.ErrBudgetSpent) || summary.Events != 1 || pending(t, spool) != 1 {
		t.Fatalf("err %v, summary %+v, pending %d; want the refused event left staged", err, summary, pending(t, spool))
	}
}

// A line the gate admits but the log writer would refuse (here, no schema_version) is rejected
// and removed, not left at the head of the spool for every later drain to stop at.
func TestDrainSessionRejectsAnInvalidEventWithoutBlockingTheRest(t *testing.T) {
	ws := t.TempDir()
	s := Session{Workspace: ws, Harness: "opencode", ID: "s1"}
	invalid := testEvent("opencode", "s1", ws, "session.started")
	invalid.SchemaVersion = ""
	spool := stage(t, s, line(t, invalid), line(t, testEvent("opencode", "s1", ws, "prompt.submitted")))
	var emitted []string
	emit := func(e schema.Event) error {
		if err := e.Validate(); err != nil {
			return err
		}
		emitted = append(emitted, e.Event.Action)
		return nil
	}
	var summary Summary
	if err := DrainSession(s, emit, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Rejected != 1 || len(emitted) != 1 || emitted[0] != "prompt.submitted" || pending(t, spool) != 0 {
		t.Fatalf("summary %+v, emitted %v, pending %d; want the invalid line rejected and the next one drained", summary, emitted, pending(t, spool))
	}
}

func TestDrainSessionDrainsTheLegacyDSHSpool(t *testing.T) {
	ws := t.TempDir()
	s := Session{Workspace: ws, Harness: legacyDSHHarness, ID: "d1"}
	dir, _ := asymptoteobserve.LegacyDSHSpoolDir(ws, "d1")
	legacy := stageAt(t, dir, line(t, testEvent(legacyDSHHarness, "d1", ws, "session.started")))
	if PendingBytes(s) == 0 {
		t.Fatal("PendingBytes misses the legacy spool")
	}
	var events []schema.Event
	var summary Summary
	if err := DrainSession(s, collect(&events), &summary); err != nil || summary.Events != 1 || pending(t, legacy) != 0 {
		t.Fatalf("err %v summary %+v", err, summary)
	}
}

// A link planted for a known session's spool directory, pointing at another spool (the account's
// own, say), is never read: draining it would remove the lines it refuses from the target.
func TestDrainSessionSkipsALinkedSpoolDirectory(t *testing.T) {
	ws := t.TempDir()
	target := stageAt(t, filepath.Join(t.TempDir(), "account-spool"), []byte(`{"not":"this session"}`))
	s := Session{Workspace: ws, Harness: "opencode", ID: "s1"}
	dir, _ := asymptoteobserve.WorkspaceSpoolDir(ws, s.Harness, s.ID)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target.Dir, dir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	var events []schema.Event
	var summary Summary
	if err := DrainSession(s, collect(&events), &summary); err != nil {
		t.Fatal(err)
	}
	if summary != (Summary{}) || pending(t, target) != 1 || PendingBytes(s) != 0 {
		t.Fatalf("summary %+v, target holds %d lines; the linked spool was read", summary, pending(t, target))
	}
}

// Spool.Append never lets a spool outgrow DefaultSpoolMaxBytes, so a larger pending.jsonl was
// written by hand -- millions of junk lines, say, that would hold the sweep lock for hours. It is
// left unread, and not reported as pending.
func TestDrainSessionSkipsAnOversizeSpool(t *testing.T) {
	ws := t.TempDir()
	s := Session{Workspace: ws, Harness: "opencode", ID: "s1"}
	spool := stage(t, s, line(t, testEvent("opencode", "s1", ws, "session.started")))
	junk := bytes.Repeat([]byte("x\n"), int(localingest.DefaultSpoolMaxBytes/2)+1)
	if err := os.WriteFile(spool.FilePath(), junk, 0o600); err != nil {
		t.Fatal(err)
	}
	var events []schema.Event
	var summary Summary
	if err := DrainSession(s, collect(&events), &summary); err != nil {
		t.Fatal(err)
	}
	if summary != (Summary{}) || PendingBytes(s) != 0 {
		t.Fatalf("summary %+v, pending %d; an oversize spool was read", summary, PendingBytes(s))
	}
	if info, err := os.Stat(spool.FilePath()); err != nil || info.Size() != int64(len(junk)) {
		t.Fatalf("the oversize spool changed: %v, %v", info, err)
	}
}

func writeLog(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestKnownSessionsReadsTheLogAndItsArchive(t *testing.T) {
	ws := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if got, err := KnownSessions(logPath); err != nil || got != nil {
		t.Fatalf("missing log: %v %v", got, err)
	}
	writeLog(t, logPath+".1", string(line(t, testEvent("codex", "a1", ws, "session.started"))))
	writeLog(t, logPath,
		string(line(t, testEvent("opencode", "s1", ws, "session.started"))),
		string(line(t, testEvent("opencode", "s1", ws, "prompt.submitted"))),
		string(line(t, testEvent("opencode", "rel", "relative/ws", "prompt.submitted"))),
		string(line(t, testEvent("opencode", "../up", ws, "prompt.submitted"))),
		"not json",
	)
	got, err := KnownSessions(logPath)
	want := []Session{{Workspace: ws, Harness: "codex", ID: "a1"}, {Workspace: ws, Harness: "opencode", ID: "s1"}}
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("KnownSessions = %+v, %v; want %+v", got, err, want)
	}
}

func TestReadTailSkipsThePartialFirstLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	writeLog(t, path, "first-line", "second", "third")
	data, size, err := readTail(path, 10)
	if err != nil || size != 24 || string(data) != "third\n" {
		t.Fatalf("readTail = %q %d %v", data, size, err)
	}
}

func TestRootSessionsListsRealSpoolDirectoriesOnly(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws1")
	stage(t, Session{Workspace: ws, Harness: "opencode", ID: "s1"}, []byte("x"))
	legacyDir, _ := asymptoteobserve.LegacyDSHSpoolDir(ws, "d1")
	stageAt(t, legacyDir, []byte("x"))
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(elsewhere, "s9"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(ws, asymptoteobserve.WorkspaceSpoolRootRel, "codex")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := RootSessions([]string{filepath.Join(root, "*"), filepath.Join(root, "[")})
	want := []Session{{Workspace: ws, Harness: "opencode", ID: "s1"}, {Workspace: ws, Harness: legacyDSHHarness, ID: "d1"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("RootSessions = %+v, want %+v", got, want)
	}
}

func TestDiscoverMergesAndSorts(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	stage(t, Session{Workspace: ws, Harness: "opencode", ID: "s1"}, []byte("x"))
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeLog(t, logPath,
		string(line(t, testEvent("opencode", "s1", ws, "session.started"))),
		string(line(t, testEvent("codex", "c1", ws, "session.started"))),
	)
	got, err := Discover(logPath, []string{filepath.Join(root, "*")}, nil)
	if err != nil || len(got) != 2 || got[0].Harness != "codex" || got[1].Harness != "opencode" {
		t.Fatalf("Discover = %+v, %v", got, err)
	}
}

// A workspace named on the command line is that directory, even when its name reads as a glob:
// "proj[1]" must not match "proj1", or a drain reports nothing staged and empties another
// workspace's spool.
func TestNamedWorkspaceIsLiteral(t *testing.T) {
	root := t.TempDir()
	literal := filepath.Join(root, "proj[1]")
	lookalike := filepath.Join(root, "proj1")
	stage(t, Session{Workspace: literal, Harness: "opencode", ID: "s1"}, []byte("x"))
	stage(t, Session{Workspace: lookalike, Harness: "opencode", ID: "s2"}, []byte("x"))
	got, err := Discover(filepath.Join(t.TempDir(), "runtime.jsonl"), nil, []string{literal})
	if want := (Session{Workspace: literal, Harness: "opencode", ID: "s1"}); len(got) != 1 || got[0] != want {
		t.Fatalf("Discover(--workspace %s) = %+v, %v; want only %+v", literal, got, err, want)
	}
}
