package dshsession

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

// The spool is how a sandboxed DeepSeek Harness hook's events reach the runtime log (#605):
// the hook stages them in the session workspace because the sandbox cannot write ~/.beacon,
// and `beacon endpoint dsh sync` drains them from outside the harness. These tests pin the
// drain contract: events land verbatim as hook events, files are deleted once emptied, a
// crash between append and delete cannot double-deliver, and a hook mid-append blocks
// instead of losing its event.

// spoolTestEvent builds an event the way the hook stages one, event.id included: derived by
// EventIDForLine over the event's own map-marshalled bytes, which is what VerifiedEventID
// re-derives on drain.
func spoolTestEvent(t *testing.T, sessionID, action string) schema.Event {
	t.Helper()
	event := asymptoteobserve.NewEvent(asymptoteobserve.NewEventOptions{
		Action:   action,
		Category: "session",
		Severity: schema.SeverityInfo,
		Harness: schema.HarnessInfo{
			Name:             dshsessionHarnessForTest,
			CollectionMethod: asymptoteobserve.CollectionMethodHook,
		},
		Message: action + " for " + sessionID,
	})
	event.Session = &schema.SessionInfo{ID: sessionID}
	return stampHookEventID(t, event)
}

func stampHookEventID(t *testing.T, event schema.Event) schema.Event {
	t.Helper()
	event.Event.ID = ""
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var asMap map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&asMap); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(asMap)
	if err != nil {
		t.Fatal(err)
	}
	event.Event.ID = asymptoteobserve.EventIDForLine(canonical)
	return event
}

// dshsessionHarnessForTest is the canonical harness name the drain must preserve.
const dshsessionHarnessForTest = "deepseek_harness"

func writeSpoolFile(t *testing.T, path string, events ...schema.Event) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(data)
		out.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
		t.Fatal(err)
	}
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

func TestDrainSessionSpoolMovesEventsAsHookEventsAndDeletesTheFile(t *testing.T) {
	workspace := t.TempDir()
	opts := spoolTestOpts(t)
	ref := SessionRef{ID: "sess-drain-1", Meta: &SessionMeta{ID: "sess-drain-1", CWD: workspace}}
	base, ok := asymptoteobserve.DSHSpoolPath(workspace, ref.ID)
	if !ok {
		t.Fatal("safe id produced no spool path")
	}
	writeSpoolFile(t, base,
		spoolTestEvent(t, ref.ID, "session.started"),
		spoolTestEvent(t, ref.ID, "prompt.submitted"),
	)

	drained, err := drainSessionSpool(ref, opts, loadDrainedIDs(opts.LogPath))
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained.Drained != 2 {
		t.Fatalf("drained = %d, want 2", drained.Drained)
	}
	events := readLogLines(t, opts.LogPath)
	if len(events) != 2 {
		t.Fatalf("log has %d events, want 2", len(events))
	}
	for _, event := range events {
		if event.Harness.CollectionMethod != asymptoteobserve.CollectionMethodHook {
			t.Errorf("action %s landed with collection_method=%q, want hook: a drained spool holds deferred hook events",
				event.Event.Action, event.Harness.CollectionMethod)
		}
		if event.Harness.Name != dshsessionHarnessForTest {
			t.Errorf("harness.name = %q, want %s", event.Harness.Name, dshsessionHarnessForTest)
		}
	}
	if pending := asymptoteobserve.DSHSpoolPendingBytes(workspace, ref.ID); pending != 0 {
		t.Errorf("spool still holds %d bytes after drain", pending)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Errorf("drained spool file still exists: %v", err)
	}
}

// A crash between "appended" and "deleted" leaves the file in place; the next drain
// redelivers it. The id guard keeps that from landing the same events twice -- most
// importantly the lifecycle events (session.started, prompt.submitted), which sit outside
// the writer's own call-id dedupe allowlist and would otherwise always double-count.
func TestDrainSessionSpoolRedeliveryAfterCrashDoesNotDoubleCount(t *testing.T) {
	workspace := t.TempDir()
	opts := spoolTestOpts(t)
	ref := SessionRef{ID: "sess-redrain", Meta: &SessionMeta{ID: "sess-redrain", CWD: workspace}}
	base, _ := asymptoteobserve.DSHSpoolPath(workspace, ref.ID)
	events := []schema.Event{
		spoolTestEvent(t, ref.ID, "session.started"),
		spoolTestEvent(t, ref.ID, "prompt.submitted"),
	}
	writeSpoolFile(t, base, events...)

	// Simulate the crash: both events reached the log, the delete never ran.
	for _, event := range events {
		if _, err := writer.AppendEvent(event, writer.Options{Path: opts.LogPath, UserMode: opts.UserMode}); err != nil {
			t.Fatal(err)
		}
	}
	seen := loadDrainedIDs(opts.LogPath)
	drained, err := drainSessionSpool(ref, opts, seen)
	if err != nil {
		t.Fatalf("redrain: %v", err)
	}
	if drained.Drained != 0 {
		t.Fatalf("redrain appended %d events, want 0 (ids already in the log)", drained.Drained)
	}
	if got := len(readLogLines(t, opts.LogPath)); got != 2 {
		t.Fatalf("log has %d events after redrain, want 2", got)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatalf("redrain did not delete the drained file: %v", err)
	}
}

// --print promises no writes and no side effects; the drain honors the same promise.
func TestDrainSessionSpoolIsInertInPrintMode(t *testing.T) {
	workspace := t.TempDir()
	opts := spoolTestOpts(t)
	opts.Print = true
	opts.Write = false
	ref := SessionRef{ID: "sess-print", Meta: &SessionMeta{ID: "sess-print", CWD: workspace}}
	base, _ := asymptoteobserve.DSHSpoolPath(workspace, ref.ID)
	writeSpoolFile(t, base, spoolTestEvent(t, ref.ID, "session.started"))

	drained, err := drainSessionSpool(ref, opts, nil)
	if err != nil {
		t.Fatalf("drain in print mode: %v", err)
	}
	if drained.Drained != 0 {
		t.Fatalf("print mode drained.Drained %d events, want 0", drained.Drained)
	}
	if pending := asymptoteobserve.DSHSpoolPendingBytes(workspace, ref.ID); pending == 0 {
		t.Fatal("print mode consumed the spool file")
	}
}

// Rotated archives hold older events than the live file; the drain empties them oldest
// first so the runtime log receives the session's history in order.
func TestDrainSessionSpoolEmptiesRotatedArchivesBeforeTheLiveFile(t *testing.T) {
	workspace := t.TempDir()
	opts := spoolTestOpts(t)
	ref := SessionRef{ID: "sess-arch", Meta: &SessionMeta{ID: "sess-arch", CWD: workspace}}
	base, _ := asymptoteobserve.DSHSpoolPath(workspace, ref.ID)
	writeSpoolFile(t, base+".1", spoolTestEvent(t, ref.ID, "session.started"))
	writeSpoolFile(t, base, spoolTestEvent(t, ref.ID, "prompt.submitted"))

	drained, err := drainSessionSpool(ref, opts, loadDrainedIDs(opts.LogPath))
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained.Drained != 2 {
		t.Fatalf("drained = %d, want 2", drained.Drained)
	}
	events := readLogLines(t, opts.LogPath)
	if len(events) != 2 || events[0].Event.Action != "session.started" || events[1].Event.Action != "prompt.submitted" {
		t.Fatalf("order in log = %+v, want the archive's event first", actionsOf(events))
	}
	for _, path := range []string{base, base + ".1"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived the drain: %v", path, err)
		}
	}
}

// A hook killed mid-append leaves a torn tail line. It is not an event and must not block
// the valid events around it from draining.
func TestDrainSessionSpoolDropsTornTailWithoutBlocking(t *testing.T) {
	workspace := t.TempDir()
	opts := spoolTestOpts(t)
	ref := SessionRef{ID: "sess-torn", Meta: &SessionMeta{ID: "sess-torn", CWD: workspace}}
	base, _ := asymptoteobserve.DSHSpoolPath(workspace, ref.ID)
	writeSpoolFile(t, base, spoolTestEvent(t, ref.ID, "session.started"))
	f, err := os.OpenFile(base, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"timestamp":"2026-09-23T0`); err != nil { // torn, no newline
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	drained, err := drainSessionSpool(ref, opts, loadDrainedIDs(opts.LogPath))
	if err != nil {
		t.Fatalf("drain with a torn tail: %v", err)
	}
	if drained.Drained != 1 {
		t.Fatalf("drained.Drained = %d, want 1 valid event", drained.Drained)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatalf("file with a torn tail survived: %v", err)
	}
}

// The drain takes the same lock every hook append takes. A hook firing mid-drain waits for
// the sweep and then writes its event -- blocking is the filelock contract, dropping is
// the bug #605 exists to prevent.
func TestDrainSessionSpoolBlocksWhileAHookHoldsTheSpoolLock(t *testing.T) {
	workspace := t.TempDir()
	opts := spoolTestOpts(t)
	ref := SessionRef{ID: "sess-lock", Meta: &SessionMeta{ID: "sess-lock", CWD: workspace}}
	base, _ := asymptoteobserve.DSHSpoolPath(workspace, ref.ID)
	writeSpoolFile(t, base, spoolTestEvent(t, ref.ID, "session.started"))

	hookLock, err := os.OpenFile(base+".lock", os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	held, err := filelock.Exclusive(hookLock)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var drained spoolDrain
	var drainErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		drained, drainErr = drainSessionSpool(ref, opts, loadDrainedIDs(opts.LogPath))
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("drain completed while a hook held the spool lock; it must block")
	case <-time.After(100 * time.Millisecond):
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if err := hookLock.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if drainErr != nil {
		t.Fatalf("drain after lock release: %v", drainErr)
	}
	if drained.Drained != 1 {
		t.Fatalf("drained.Drained = %d, want 1", drained.Drained)
	}
}

// End to end through CollectOnce: a store session whose workspace holds a spool comes out
// with the staged hook events AND the poll mapping in the log, the spool is gone, and the
// sweep report counts the drained events separately so an operator can see the drain work.
func TestCollectOnceDrainsTheWorkspaceSpoolAlongsideTheStore(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	writeSession(t, filepath.Join(root, "sessions", "s-collect"), SessionFileJSON,
		record("session", map[string]interface{}{"id": "s-collect", "cwd": workspace}),
		record("user/message", map[string]interface{}{"message": map[string]interface{}{"content": "hello"}}),
	)
	base, _ := asymptoteobserve.DSHSpoolPath(workspace, "s-collect")
	writeSpoolFile(t, base,
		spoolTestEvent(t, "s-collect", "session.started"),
		spoolTestEvent(t, "s-collect", "prompt.submitted"),
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
	if pending := asymptoteobserve.DSHSpoolPendingBytes(workspace, "s-collect"); pending != 0 {
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

func TestCollectOnceCountsSpoolEventsAppendedBeforeDrainFailure(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	writeSession(t, filepath.Join(root, "sessions", "s-partial-drain"), SessionFileJSON,
		record("session", map[string]interface{}{"id": "s-partial-drain", "cwd": workspace}),
	)
	base, _ := asymptoteobserve.DSHSpoolPath(workspace, "s-partial-drain")
	valid := spoolTestEvent(t, "s-partial-drain", "session.started")
	invalid := spoolTestEvent(t, "s-partial-drain", "prompt.submitted")
	invalid.Vendor = ""
	writeSpoolFile(t, base, valid, invalid)

	opts := CollectOptions{
		DSHHome:   root,
		StatePath: filepath.Join(t.TempDir(), "state.json"),
		Write:     true,
		LogPath:   filepath.Join(t.TempDir(), "runtime.jsonl"),
		UserMode:  true,
	}
	summary, err := CollectOnce(opts)
	if err == nil {
		t.Fatal("CollectOnce succeeded despite the invalid second spool event")
	}
	if summary.SpoolEvents != 1 {
		t.Fatalf("SpoolEvents = %d, want 1 event appended before the drain failed", summary.SpoolEvents)
	}
}

// The --workspace / --session-id filters decide which sessions are considered at all; a
// filtered-out session's spool is left untouched rather than half-drained behind the
// operator's filter.
func TestCollectOnceLeavesASpoolWhenItsSessionIsFilteredOut(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	writeSession(t, filepath.Join(root, "sessions", "s-filtered"), SessionFileJSON,
		record("session", map[string]interface{}{"id": "s-filtered", "cwd": workspace}),
	)
	base, _ := asymptoteobserve.DSHSpoolPath(workspace, "s-filtered")
	writeSpoolFile(t, base, spoolTestEvent(t, "s-filtered", "session.started"))

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
	if pending := asymptoteobserve.DSHSpoolPendingBytes(workspace, "s-filtered"); pending == 0 {
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

// Anything in the workspace can write a spool, the sandboxed agent included, and the drain
// runs outside the sandbox. A staged line lands only when it is a DeepSeek Harness hook event
// for the session whose spool holds it, and never as a record only Beacon itself writes.
func TestDrainSessionSpoolRefusesLinesItsOwnHookCouldNotHaveStaged(t *testing.T) {
	workspace := t.TempDir()
	opts := spoolTestOpts(t)
	ref := SessionRef{ID: "sess-forge", Meta: &SessionMeta{ID: "sess-forge", CWD: workspace}}
	base, _ := asymptoteobserve.DSHSpoolPath(workspace, ref.ID)

	genuine := spoolTestEvent(t, ref.ID, "prompt.submitted")
	otherSession := spoolTestEvent(t, "sess-victim", "approval.granted")
	otherHarness := spoolTestEvent(t, ref.ID, "approval.granted")
	otherHarness.Harness.Name = "claude_code"
	otherHarness = stampHookEventID(t, otherHarness)
	pollLabel := spoolTestEvent(t, ref.ID, "session.started")
	pollLabel.Harness.CollectionMethod = asymptoteobserve.CollectionMethodPoll
	pollLabel = stampHookEventID(t, pollLabel)
	noSession := spoolTestEvent(t, ref.ID, "session.started")
	noSession.Session = nil
	noSession = stampHookEventID(t, noSession)
	inventory := spoolTestEvent(t, ref.ID, "inventory.snapshot")
	commitLink := spoolTestEvent(t, ref.ID, "session.commit_linked")
	writeSpoolFile(t, base, otherSession, otherHarness, pollLabel, noSession, inventory, commitLink, genuine)

	drained, err := drainSessionSpool(ref, opts, loadDrainedIDs(opts.LogPath))
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained.Drained != 1 || drained.Rejected != 6 {
		t.Fatalf("drained=%d rejected=%d, want 1 and 6", drained.Drained, drained.Rejected)
	}
	events := readLogLines(t, opts.LogPath)
	if len(events) != 1 || events[0].Event.ID != genuine.Event.ID {
		t.Fatalf("log = %+v, want only the genuine event", events)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatalf("refused lines kept the spool file: %v", err)
	}
}

// The redelivery guard skips a staged event whose id is already recorded. A staged line that
// claims an id it did not derive must not be able to use that guard to suppress a genuine
// event: its id is dropped, so the genuine event after it still lands.
func TestDrainSessionSpoolDoesNotTrustAnIDTheLineDidNotDerive(t *testing.T) {
	workspace := t.TempDir()
	opts := spoolTestOpts(t)
	ref := SessionRef{ID: "sess-idforge", Meta: &SessionMeta{ID: "sess-idforge", CWD: workspace}}
	base, _ := asymptoteobserve.DSHSpoolPath(workspace, ref.ID)

	genuine := spoolTestEvent(t, ref.ID, "prompt.submitted")
	forged := spoolTestEvent(t, ref.ID, "session.started")
	forged.Event.ID = genuine.Event.ID
	writeSpoolFile(t, base, forged, genuine)

	drained, err := drainSessionSpool(ref, opts, loadDrainedIDs(opts.LogPath))
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained.Drained != 2 {
		t.Fatalf("drained = %d, want 2 (the forged id must not suppress the genuine event)", drained.Drained)
	}
	events := readLogLines(t, opts.LogPath)
	if len(events) != 2 {
		t.Fatalf("log has %d events, want 2", len(events))
	}
	if events[0].Event.ID == genuine.Event.ID || events[0].Event.ID == "" {
		t.Errorf("forged line kept its claimed id %q", events[0].Event.ID)
	}
	if events[1].Event.ID != genuine.Event.ID {
		t.Errorf("genuine event id = %q, want %q", events[1].Event.ID, genuine.Event.ID)
	}
}

// The drain runs outside the sandbox over paths sandboxed code can create, so it never
// follows a symlink: not for the spool directory, a data file, or the lock.
func TestDrainSessionSpoolNeverFollowsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	secret := filepath.Join(t.TempDir(), "outside.jsonl")
	outsideEvent := spoolTestEvent(t, "sess-link", "prompt.submitted")
	writeSpoolFile(t, secret, outsideEvent)

	t.Run("directory", func(t *testing.T) {
		workspace := t.TempDir()
		target := t.TempDir()
		if err := os.MkdirAll(filepath.Join(target, "dsh-spool"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, filepath.Join(target, "dsh-spool", "sess-link.jsonl")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(workspace, ".beacon")); err != nil {
			t.Fatal(err)
		}
		opts := spoolTestOpts(t)
		ref := SessionRef{ID: "sess-link", Meta: &SessionMeta{ID: "sess-link", CWD: workspace}}
		if _, err := drainSessionSpool(ref, opts, map[string]struct{}{}); err == nil {
			t.Fatal("drain followed a symlinked .beacon directory without reporting it")
		}
		if events := readLogLines(t, opts.LogPath); len(events) != 0 {
			t.Fatalf("drain read through the symlink: %d events", len(events))
		}
	})

	t.Run("data file", func(t *testing.T) {
		workspace := t.TempDir()
		base, _ := asymptoteobserve.DSHSpoolPath(workspace, "sess-link")
		if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, base); err != nil {
			t.Fatal(err)
		}
		opts := spoolTestOpts(t)
		ref := SessionRef{ID: "sess-link", Meta: &SessionMeta{ID: "sess-link", CWD: workspace}}
		if _, err := drainSessionSpool(ref, opts, map[string]struct{}{}); err != nil {
			t.Fatalf("drain: %v", err)
		}
		if events := readLogLines(t, opts.LogPath); len(events) != 0 {
			t.Fatalf("drain read through a symlinked spool file: %d events", len(events))
		}
		if _, err := os.Stat(secret); err != nil {
			t.Fatalf("link target disturbed: %v", err)
		}
	})

	t.Run("lock", func(t *testing.T) {
		workspace := t.TempDir()
		base, _ := asymptoteobserve.DSHSpoolPath(workspace, "sess-link")
		writeSpoolFile(t, base, spoolTestEvent(t, "sess-link", "prompt.submitted"))
		planted := filepath.Join(t.TempDir(), "created-by-drain")
		if err := os.Symlink(planted, base+".lock"); err != nil {
			t.Fatal(err)
		}
		opts := spoolTestOpts(t)
		ref := SessionRef{ID: "sess-link", Meta: &SessionMeta{ID: "sess-link", CWD: workspace}}
		if _, err := drainSessionSpool(ref, opts, map[string]struct{}{}); err == nil {
			t.Fatal("drain opened a symlinked lock without reporting it")
		}
		if _, err := os.Lstat(planted); !os.IsNotExist(err) {
			t.Fatalf("drain created a file through the lock symlink: %v", err)
		}
	})
}

// A sweep reports refused lines in its summary, separately from errors.
func TestCollectOnceCountsRefusedSpoolLines(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	writeSession(t, filepath.Join(root, "sessions", "s-refused"), SessionFileJSON,
		record("session", map[string]interface{}{"id": "s-refused", "cwd": workspace}),
	)
	base, _ := asymptoteobserve.DSHSpoolPath(workspace, "s-refused")
	writeSpoolFile(t, base,
		spoolTestEvent(t, "s-refused", "session.started"),
		spoolTestEvent(t, "someone-else", "approval.granted"),
	)
	summary, err := CollectOnce(CollectOptions{
		DSHHome:   root,
		StatePath: filepath.Join(t.TempDir(), "state.json"),
		Write:     true,
		LogPath:   filepath.Join(t.TempDir(), "runtime.jsonl"),
		UserMode:  true,
	})
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary.SpoolEvents != 1 || summary.SpoolRejected != 1 || summary.Errors != 0 {
		t.Fatalf("summary = %+v, want 1 drained, 1 refused, 0 errors", summary)
	}
}
