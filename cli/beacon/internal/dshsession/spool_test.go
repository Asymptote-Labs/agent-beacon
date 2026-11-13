package dshsession

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

// The spool is how a sandboxed DeepSeek Harness hook's events reach the runtime log (#605): the
// hook stages them in the session workspace because the sandbox cannot write ~/.beacon, and
// `beacon endpoint dsh sync` drains them from outside the harness. These tests pin the drain
// contract: staged hook events land as hook events and leave the spool, anything a sandboxed hook
// of the session could not have staged is refused, the sweep's limits apply to drained events, a
// system backfill leaves spools alone, and a hook staging mid-drain loses nothing.

func spoolTestEvent(t *testing.T, sessionID, action string) schema.Event {
	t.Helper()
	event := asymptoteobserve.NewEvent(asymptoteobserve.NewEventOptions{
		Action:   action,
		Category: "session",
		Severity: schema.SeverityInfo,
		Harness: schema.HarnessInfo{
			Name:             Harness,
			CollectionMethod: asymptoteobserve.CollectionMethodHook,
		},
		Message: action + " for " + sessionID,
	})
	event.Session = &schema.SessionInfo{ID: sessionID}
	return event
}

func spoolTestLine(t *testing.T, event schema.Event) []byte {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// stage appends lines to sessionID's spool in workspace the way a sandboxed hook does.
func stage(t *testing.T, workspace, sessionID string, lines ...[]byte) {
	t.Helper()
	dir, ok := asymptoteobserve.WorkspaceSpoolDir(workspace, Harness, sessionID)
	if !ok {
		t.Fatalf("unsafe test session id %q", sessionID)
	}
	stageIn(t, dir, lines...)
}

func stageIn(t *testing.T, dir string, lines ...[]byte) {
	t.Helper()
	if dropped, err := (localingest.Spool{Dir: dir}).Append(lines); err != nil || dropped != 0 {
		t.Fatalf("stage: dropped %d, err %v", dropped, err)
	}
}

// A spool an earlier hook left in .beacon/dsh-spool/<session> is still drained, once.
func TestDrainSessionSpoolDrainsTheLegacyDSHSpool(t *testing.T) {
	workspace := t.TempDir()
	opts := spoolTestOpts(t)
	ref := spoolTestRef("sess-legacy-1", workspace)
	legacy, ok := asymptoteobserve.LegacyDSHSpoolDir(workspace, ref.ID)
	if !ok {
		t.Fatal("unsafe test session id")
	}
	stageIn(t, legacy, spoolTestLine(t, spoolTestEvent(t, ref.ID, "session.started")))
	stage(t, workspace, ref.ID, spoolTestLine(t, spoolTestEvent(t, ref.ID, "prompt.submitted")))

	var summary Summary
	if err := drainSessionSpool(ref, opts, &summary); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if summary.SpoolEvents != 2 || summary.SpoolRejected != 0 {
		t.Fatalf("SpoolEvents/SpoolRejected = %d/%d, want 2/0", summary.SpoolEvents, summary.SpoolRejected)
	}
	if pending := SpoolPendingBytes(ref); pending != 0 {
		t.Errorf("spools still hold %d bytes after the drain", pending)
	}
}

func spoolTestRef(sessionID, workspace string) SessionRef {
	return SessionRef{ID: sessionID, Meta: &SessionMeta{ID: sessionID, CWD: workspace}}
}

func spoolTestOpts(t *testing.T) CollectOptions {
	t.Helper()
	return CollectOptions{
		Write:    true,
		LogPath:  filepath.Join(t.TempDir(), "runtime.jsonl"),
		UserMode: true,
	}
}

func readLogLines(t *testing.T, path string) []schema.Event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []schema.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event schema.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("log line is not an event: %v\n%s", err, line)
		}
		out = append(out, event)
	}
	return out
}

func TestDrainSessionSpoolMovesEventsAsHookEventsAndEmptiesTheSpool(t *testing.T) {
	workspace := t.TempDir()
	opts := spoolTestOpts(t)
	ref := spoolTestRef("sess-drain-1", workspace)
	stage(t, workspace, ref.ID,
		spoolTestLine(t, spoolTestEvent(t, ref.ID, "session.started")),
		spoolTestLine(t, spoolTestEvent(t, ref.ID, "prompt.submitted")),
	)

	var summary Summary
	if err := drainSessionSpool(ref, opts, &summary); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if summary.SpoolEvents != 2 || summary.SpoolRejected != 0 {
		t.Fatalf("SpoolEvents/SpoolRejected = %d/%d, want 2/0", summary.SpoolEvents, summary.SpoolRejected)
	}
	events := readLogLines(t, opts.LogPath)
	if got := actionsOf(events); strings.Join(got, ",") != "session.started,prompt.submitted" {
		t.Fatalf("log actions = %v, want the staged events in order", got)
	}
	for _, event := range events {
		if event.Harness.CollectionMethod != asymptoteobserve.CollectionMethodHook || event.Harness.Name != Harness {
			t.Errorf("%s landed as %s/%s, want %s/hook: a drained spool holds deferred hook events",
				event.Event.Action, event.Harness.Name, event.Harness.CollectionMethod, Harness)
		}
	}
	if pending := SpoolPendingBytes(ref); pending != 0 {
		t.Errorf("spool still holds %d bytes after the drain", pending)
	}
}

// The workspace is writable by the sandboxed agent and, depending on where it lives, by others,
// so a staged line is untrusted. Only a hook event of the session whose workspace holds the spool
// is appended; a forged share request in particular must never reach the log, where Beacon Cloud
// would publish the session on it.
func TestDrainSessionSpoolRejectsWhatASandboxedHookCouldNotHaveStaged(t *testing.T) {
	workspace := t.TempDir()
	opts := spoolTestOpts(t)
	ref := spoolTestRef("sess-trust", workspace)

	otherSession := spoolTestEvent(t, "sess-someone-else", "prompt.submitted")
	otherHarness := spoolTestEvent(t, ref.ID, "prompt.submitted")
	otherHarness.Harness.Name = "claude_code"
	polled := spoolTestEvent(t, ref.ID, "prompt.submitted")
	polled.Harness.CollectionMethod = asymptoteobserve.CollectionMethodPoll
	shareRequest := spoolTestEvent(t, ref.ID, asymptoteobserve.ActionShareRequested)
	stage(t, workspace, ref.ID,
		spoolTestLine(t, otherSession),
		spoolTestLine(t, otherHarness),
		spoolTestLine(t, polled),
		spoolTestLine(t, shareRequest),
		[]byte(`{"timestamp":"2026-09-23T0`), // a torn line from a killed hook
		spoolTestLine(t, spoolTestEvent(t, ref.ID, "session.started")),
	)

	var summary Summary
	if err := drainSessionSpool(ref, opts, &summary); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if summary.SpoolEvents != 1 || summary.SpoolRejected != 5 {
		t.Fatalf("SpoolEvents/SpoolRejected = %d/%d, want 1/5", summary.SpoolEvents, summary.SpoolRejected)
	}
	if got := actionsOf(readLogLines(t, opts.LogPath)); len(got) != 1 || got[0] != "session.started" {
		t.Fatalf("log actions = %v, want only the genuine staged event", got)
	}
	if pending := SpoolPendingBytes(ref); pending != 0 {
		t.Errorf("rejected lines were left staged (%d bytes); they would be refused on every sweep", pending)
	}
}

// --print promises no writes and no side effects; the drain honors the same promise.
func TestDrainSessionSpoolIsInertInPrintMode(t *testing.T) {
	workspace := t.TempDir()
	opts := spoolTestOpts(t)
	opts.Print = true
	opts.Write = false
	ref := spoolTestRef("sess-print", workspace)
	stage(t, workspace, ref.ID, spoolTestLine(t, spoolTestEvent(t, ref.ID, "session.started")))

	var summary Summary
	if err := drainSessionSpool(ref, opts, &summary); err != nil {
		t.Fatalf("drain in print mode: %v", err)
	}
	if summary.SpoolEvents != 0 || SpoolPendingBytes(ref) == 0 {
		t.Fatalf("print mode drained %d events; the spool must be left as it was", summary.SpoolEvents)
	}
}

// A system backfill runs as root over the console user's home. The drain writes and removes files
// under a workspace path the user's session store names, so under confinement it does nothing and
// the user's own sync drains the spool later.
func TestDrainSessionSpoolLeavesSpoolsAloneUnderBackfillConfinement(t *testing.T) {
	workspace := t.TempDir()
	opts := spoolTestOpts(t)
	ref := spoolTestRef("sess-confined", workspace)
	stage(t, workspace, ref.ID, spoolTestLine(t, spoolTestEvent(t, ref.ID, "session.started")))
	before := SpoolPendingBytes(ref)

	confined, err := sessionfs.Confine(t.TempDir(), -1, sessionfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var summary Summary
	drainErr := drainSessionSpool(ref, opts, &summary)
	confined.Release()
	if drainErr != nil {
		t.Fatalf("drain under confinement: %v", drainErr)
	}
	if summary.SpoolEvents != 0 || len(readLogLines(t, opts.LogPath)) != 0 {
		t.Fatalf("a confined sweep drained %d events", summary.SpoolEvents)
	}
	if after := SpoolPendingBytes(ref); after != before {
		t.Fatalf("spool changed under confinement: %d -> %d bytes", before, after)
	}
}

// A hook that fires while a sweep drains its session waits only for the spool's short lock, not
// for the sweep's appends, and its event survives the drain for the next sweep to deliver.
func TestDrainSessionSpoolKeepsAnEventStagedWhileItWasSending(t *testing.T) {
	workspace := t.TempDir()
	opts := spoolTestOpts(t)
	ref := spoolTestRef("sess-concurrent", workspace)
	stage(t, workspace, ref.ID, spoolTestLine(t, spoolTestEvent(t, ref.ID, "session.started")))

	orig := emitEvent
	t.Cleanup(func() { emitEvent = orig })
	staged := false
	emitEvent = func(event schema.Event, opts CollectOptions) error {
		if !staged {
			staged = true
			stage(t, workspace, ref.ID, spoolTestLine(t, spoolTestEvent(t, ref.ID, "prompt.submitted")))
		}
		return orig(event, opts)
	}

	var summary Summary
	if err := drainSessionSpool(ref, opts, &summary); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := drainSessionSpool(ref, opts, &summary); err != nil {
		t.Fatalf("second drain: %v", err)
	}
	if got := actionsOf(readLogLines(t, opts.LogPath)); strings.Join(got, ",") != "session.started,prompt.submitted" {
		t.Fatalf("log actions = %v, want both events: the one staged mid-drain must not be lost", got)
	}
	if pending := SpoolPendingBytes(ref); pending != 0 {
		t.Errorf("spool still holds %d bytes", pending)
	}
}

// End to end through CollectOnce: a store session whose workspace holds a spool comes out with the
// staged hook events AND the poll mapping in the log, the spool is emptied, and the sweep report
// counts the drained events separately so an operator can see the drain work.
func TestCollectOnceDrainsTheWorkspaceSpoolAlongsideTheStore(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	writeSession(t, filepath.Join(root, "sessions", "s-collect"), SessionFileJSON,
		record("session", map[string]interface{}{"id": "s-collect", "cwd": workspace}),
		record("user/message", map[string]interface{}{"message": map[string]interface{}{"content": "hello"}}),
	)
	stage(t, workspace, "s-collect",
		spoolTestLine(t, spoolTestEvent(t, "s-collect", "session.started")),
		spoolTestLine(t, spoolTestEvent(t, "s-collect", "prompt.submitted")),
	)

	opts := CollectOptions{
		DSHHome:   root,
		StatePath: filepath.Join(t.TempDir(), "state.json"),
		Write:     true,
		LogPath:   filepath.Join(t.TempDir(), "runtime.jsonl"),
		UserMode:  true,
	}
	summary, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary.SpoolEvents != 2 {
		t.Fatalf("SpoolEvents = %d, want 2", summary.SpoolEvents)
	}
	if summary.EventsEmitted == 0 {
		t.Fatal("poll mapping emitted nothing; the fixture session was not collected")
	}
	if pending := SpoolPendingBytes(spoolTestRef("s-collect", workspace)); pending != 0 {
		t.Fatalf("spool still holds %d bytes after the sweep", pending)
	}
	var hookEvents, pollEvents int
	for _, event := range readLogLines(t, opts.LogPath) {
		switch event.Harness.CollectionMethod {
		case asymptoteobserve.CollectionMethodHook:
			hookEvents++
		case asymptoteobserve.CollectionMethodPoll:
			pollEvents++
		}
	}
	if hookEvents != 2 {
		t.Errorf("hook events in log = %d, want 2 (the drained spool)", hookEvents)
	}
	if pollEvents == 0 {
		t.Errorf("poll events in log = 0, want the store mapping too")
	}
}

// Drained events are bounded like mapped ones: an install-time backfill's byte budget stops the
// sweep at the first staged event that does not fit, and leaves it staged for a later sync.
func TestCollectOnceStopsTheDrainWhenTheBudgetIsSpent(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	writeSession(t, filepath.Join(root, "sessions", "s-budget"), SessionFileJSON,
		record("session", map[string]interface{}{"id": "s-budget", "cwd": workspace}),
	)
	stage(t, workspace, "s-budget", spoolTestLine(t, spoolTestEvent(t, "s-budget", "session.started")))

	opts := CollectOptions{
		DSHHome:   root,
		StatePath: filepath.Join(t.TempDir(), "state.json"),
		Write:     true,
		LogPath:   filepath.Join(t.TempDir(), "runtime.jsonl"),
		UserMode:  true,
		Budget:    writer.NewBudget(1),
	}
	summary, err := CollectOnce(opts)
	if !errors.Is(err, writer.ErrBudgetSpent) {
		t.Fatalf("CollectOnce err = %v, want the spent budget", err)
	}
	if summary.SpoolEvents != 0 || summary.Errors != 0 {
		t.Fatalf("SpoolEvents/Errors = %d/%d, want 0/0: a spent budget is a stop, not a failure", summary.SpoolEvents, summary.Errors)
	}
	if len(readLogLines(t, opts.LogPath)) != 0 {
		t.Fatal("the drain wrote past a spent budget")
	}
	if SpoolPendingBytes(spoolTestRef("s-budget", workspace)) == 0 {
		t.Fatal("the event that did not fit was taken out of the spool")
	}
}

// A staged event the writer would refuse as invalid is rejected and removed, never left at the
// head of the spool to fail every later drain; the events around it are delivered. (A writer error,
// such as a spent budget, still stops the drain with the rest staged.)
func TestCollectOnceRejectsAnInvalidSpoolEventWithoutBlockingTheRest(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	writeSession(t, filepath.Join(root, "sessions", "s-partial-drain"), SessionFileJSON,
		record("session", map[string]interface{}{"id": "s-partial-drain", "cwd": workspace}),
	)
	invalid := spoolTestEvent(t, "s-partial-drain", "prompt.submitted")
	invalid.Severity = ""
	stage(t, workspace, "s-partial-drain",
		spoolTestLine(t, spoolTestEvent(t, "s-partial-drain", "session.started")),
		spoolTestLine(t, invalid),
		spoolTestLine(t, spoolTestEvent(t, "s-partial-drain", "session.ended")),
	)

	opts := CollectOptions{
		DSHHome:   root,
		StatePath: filepath.Join(t.TempDir(), "state.json"),
		Write:     true,
		LogPath:   filepath.Join(t.TempDir(), "runtime.jsonl"),
		UserMode:  true,
	}
	summary, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary.SpoolEvents != 2 || summary.SpoolRejected != 1 {
		t.Fatalf("SpoolEvents/SpoolRejected = %d/%d, want 2/1", summary.SpoolEvents, summary.SpoolRejected)
	}
	if SpoolPendingBytes(spoolTestRef("s-partial-drain", workspace)) != 0 {
		t.Fatal("the invalid event was left in the spool")
	}
}

// The --workspace / --session-id filters decide which sessions are considered at all; a
// filtered-out session's spool is left untouched rather than drained behind the operator's filter.
func TestCollectOnceLeavesASpoolWhenItsSessionIsFilteredOut(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	writeSession(t, filepath.Join(root, "sessions", "s-filtered"), SessionFileJSON,
		record("session", map[string]interface{}{"id": "s-filtered", "cwd": workspace}),
	)
	stage(t, workspace, "s-filtered", spoolTestLine(t, spoolTestEvent(t, "s-filtered", "session.started")))

	opts := CollectOptions{
		DSHHome:   root,
		StatePath: filepath.Join(t.TempDir(), "state.json"),
		Write:     true,
		LogPath:   filepath.Join(t.TempDir(), "runtime.jsonl"),
		UserMode:  true,
		SessionID: "someone-else",
	}
	summary, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary.SpoolEvents != 0 {
		t.Fatalf("filtered sweep drained %d events, want 0", summary.SpoolEvents)
	}
	if SpoolPendingBytes(spoolTestRef("s-filtered", workspace)) == 0 {
		t.Fatal("filtered session's spool was consumed")
	}
}

func actionsOf(events []schema.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Event.Action)
	}
	return out
}
