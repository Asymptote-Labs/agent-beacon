package clinesession

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

type sweepFixture struct {
	clineDir  string
	statePath string
	logPath   string
}

func newSweepFixture(t *testing.T) sweepFixture {
	t.Helper()
	dir := t.TempDir()
	return sweepFixture{
		clineDir:  filepath.Join(dir, ".cline"),
		statePath: filepath.Join(dir, "state", "cline-sessions.json"),
		logPath:   filepath.Join(dir, "logs", "runtime.jsonl"),
	}
}

func (f sweepFixture) options() CollectOptions {
	return CollectOptions{
		ClineDir:  f.clineDir,
		StatePath: f.statePath,
		Write:     true,
		LogPath:   f.logPath,
		UserMode:  true,
	}
}

func (f sweepFixture) logLines(t *testing.T) []schema.Event {
	t.Helper()
	data, err := os.ReadFile(f.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read runtime log: %v", err)
	}
	var events []schema.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var event schema.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode runtime log line: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func (f sweepFixture) lastLine(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	return lines[len(lines)-1]
}

// writeKanbanCard lays down the pair of files a Cline kanban workspace keeps: the board holding the
// card's prompt and the session record holding whatever the run has produced so far.
func (f sweepFixture) writeKanbanCard(t *testing.T, prompt, finalMessage, updatedAt string) {
	t.Helper()
	f.writeKanbanCardWithWarning(t, prompt, finalMessage, "", updatedAt)
}

func (f sweepFixture) writeKanbanCardWithWarning(t *testing.T, prompt, finalMessage, warning, updatedAt string) {
	t.Helper()
	workspaceDir := filepath.Join(f.clineDir, "kanban", "workspaces", "workspace-1")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(workspaceDir, "board.json"), map[string]interface{}{"columns": []interface{}{
		map[string]interface{}{"cards": []interface{}{map[string]interface{}{
			"id":        "task-1",
			"prompt":    prompt,
			"createdAt": "2026-01-01T00:00:00Z",
		}}},
	}})
	session := map[string]interface{}{
		"workspacePath": "/repo/kanban",
		"modelId":       "claude-sonnet-4-5",
		"updatedAt":     updatedAt,
	}
	if finalMessage != "" {
		session["latestHookActivity"] = map[string]interface{}{"finalMessage": finalMessage}
	}
	if warning != "" {
		session["warningMessage"] = warning
	}
	writeJSON(t, filepath.Join(workspaceDir, "sessions.json"), map[string]interface{}{"task-1": session})
}

func (f sweepFixture) writeHistoryTask(t *testing.T, id string, entries []interface{}) {
	t.Helper()
	taskDir := filepath.Join(f.clineDir, "data", "tasks", id)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(taskDir, "task_metadata.json"), map[string]interface{}{"cwd": "/repo/app"})
	writeJSON(t, filepath.Join(taskDir, historyFileName), entries)
}

func userEntry(timestampMS int64, text string) interface{} {
	return map[string]interface{}{"role": "user", "timestamp": timestampMS, "content": text}
}

func assistantEntry(timestampMS int64, text string) interface{} {
	return map[string]interface{}{
		"role":      "assistant",
		"timestamp": timestampMS,
		"content":   []interface{}{map[string]interface{}{"type": "text", "text": text}},
	}
}

func actionsOf(events []schema.Event) []string {
	actions := make([]string, 0, len(events))
	for _, event := range events {
		actions = append(actions, event.Event.Action)
	}
	return actions
}

func TestSweepWritesClineActivityIntoTheRuntimeLog(t *testing.T) {
	f := newSweepFixture(t)
	f.writeHistoryTask(t, "task-1", []interface{}{
		userEntry(1700000000000, "<task>Update src/main.go</task>"),
		assistantEntry(1700000001000, "Updated the file"),
	})

	summary, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary.Traces != 1 || summary.TracesChanged != 1 {
		t.Errorf("summary = %+v, want one trace, one changed", summary)
	}

	events := f.logLines(t)
	if len(events) != summary.EventsEmitted {
		t.Fatalf("wrote %d lines for %d emitted events", len(events), summary.EventsEmitted)
	}
	if got := actionsOf(events); len(got) != 3 {
		t.Fatalf("actions = %v, want session.started + prompt.submitted + agent.message", got)
	}
	for _, event := range events {
		if event.Harness.Name != Harness {
			t.Errorf("harness = %q", event.Harness.Name)
		}
		if event.Harness.CollectionMethod != "poll" {
			t.Errorf("%s collection method = %q, want poll", event.Event.Action, event.Harness.CollectionMethod)
		}
		// Without a deterministic id the same Cline record read twice cannot be recognized as one
		// event downstream.
		if event.Event.ID == "" {
			t.Errorf("%s has no event id", event.Event.Action)
		}
	}
}

// The property the cursor exists for. A machine sweeping every minute must not append a task's
// history again every minute.
func TestSecondSweepOverUnchangedTracesWritesNothing(t *testing.T) {
	f := newSweepFixture(t)
	f.writeHistoryTask(t, "task-1", []interface{}{
		userEntry(1700000000000, "<task>Update src/main.go</task>"),
		assistantEntry(1700000001000, "Updated the file"),
	})

	first, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if first.EventsEmitted == 0 {
		t.Fatal("first sweep emitted nothing, so this proves nothing")
	}
	before := len(f.logLines(t))

	second, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if second.EventsEmitted != 0 {
		t.Errorf("second sweep emitted %d events, want 0", second.EventsEmitted)
	}
	if after := len(f.logLines(t)); after != before {
		t.Errorf("runtime log grew from %d to %d lines on a sweep with nothing new", before, after)
	}
}

func TestSweepResumesHistoryFromTheCursor(t *testing.T) {
	f := newSweepFixture(t)
	f.writeHistoryTask(t, "task-1", []interface{}{
		userEntry(1700000000000, "<task>Update src/main.go</task>"),
	})
	if _, err := CollectOnce(f.options()); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	before := len(f.logLines(t))

	f.writeHistoryTask(t, "task-1", []interface{}{
		userEntry(1700000000000, "<task>Update src/main.go</task>"),
		assistantEntry(1700000001000, "Updated the file"),
	})
	second, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if second.EventsEmitted != 1 {
		t.Fatalf("second sweep emitted %d events, want only the new assistant turn", second.EventsEmitted)
	}
	events := f.logLines(t)
	if len(events) != before+1 {
		t.Fatalf("runtime log went from %d to %d lines, want one appended", before, len(events))
	}
	if last := events[len(events)-1]; last.Event.Action != "agent.message" {
		t.Errorf("appended %q, want agent.message", last.Event.Action)
	}
}

// A kanban card is read as a snapshot rather than an append-only log, so the cursor cannot be an
// order high-water mark: a card's summary is rewritten in place as the run progresses. Sweeping a
// card whose content has not changed must still write nothing, even though its updatedAt moves.
func TestKanbanSweepEmitsOnlyChangedCardContent(t *testing.T) {
	f := newSweepFixture(t)
	f.writeKanbanCard(t, "Build the dashboard", "", "2026-01-01T00:01:00Z")

	first, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if got := actionsOf(f.logLines(t)); len(got) != 2 || got[1] != "prompt.submitted" {
		t.Fatalf("first sweep actions = %v, want session.started + prompt.submitted", got)
	}
	if first.EventsEmitted != 2 {
		t.Fatalf("first sweep emitted %d events, want 2", first.EventsEmitted)
	}

	// Cline rewrites sessions.json as the run progresses, so updatedAt moves even when nothing the
	// telemetry carries has changed.
	f.writeKanbanCard(t, "Build the dashboard", "", "2026-01-01T00:02:00Z")
	second, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if second.EventsEmitted != 0 {
		t.Errorf("second sweep emitted %d events, want 0 for an unchanged card", second.EventsEmitted)
	}
	if got := len(f.logLines(t)); got != 2 {
		t.Errorf("runtime log has %d lines after an unchanged sweep, want 2", got)
	}

	f.writeKanbanCard(t, "Build the dashboard", "Dashboard complete", "2026-01-01T00:03:00Z")
	third, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("third sweep: %v", err)
	}
	if third.EventsEmitted != 1 {
		t.Fatalf("third sweep emitted %d events, want only the new summary", third.EventsEmitted)
	}
	events := f.logLines(t)
	if len(events) != 3 {
		t.Fatalf("runtime log has %d lines, want 3", len(events))
	}
	summaryEvent := events[len(events)-1]
	if summaryEvent.Event.Action != "agent.message" {
		t.Fatalf("appended %q, want agent.message", summaryEvent.Event.Action)
	}
	if !strings.Contains(f.lastLine(t), "Dashboard complete") {
		t.Errorf("appended line does not carry the new finalMessage: %s", f.lastLine(t))
	}

	// A rewritten summary is new content, not a repeat, so it is emitted again.
	f.writeKanbanCard(t, "Build the dashboard", "Dashboard complete, tests green", "2026-01-01T00:04:00Z")
	fourth, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("fourth sweep: %v", err)
	}
	if fourth.EventsEmitted != 1 {
		t.Fatalf("fourth sweep emitted %d events, want only the rewritten summary", fourth.EventsEmitted)
	}
}

// The content hashes are keyed by the card's record order, and that order is positional: a record
// that clears frees its order for whatever comes next. A hash left behind by a cleared record would
// silence the same record reappearing later.
func TestKanbanSweepEmitsARecordThatClearsAndReturns(t *testing.T) {
	f := newSweepFixture(t)
	f.writeKanbanCardWithWarning(t, "Build the dashboard", "", "Rate limited", "2026-01-01T00:01:00Z")
	if _, err := CollectOnce(f.options()); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if got := len(f.logLines(t)); got != 3 {
		t.Fatalf("first sweep wrote %d lines, want session.started + prompt + warning", got)
	}

	f.writeKanbanCardWithWarning(t, "Build the dashboard", "", "", "2026-01-01T00:02:00Z")
	if _, err := CollectOnce(f.options()); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if got := len(f.logLines(t)); got != 3 {
		t.Fatalf("runtime log has %d lines after the warning cleared, want 3", got)
	}

	f.writeKanbanCardWithWarning(t, "Build the dashboard", "", "Rate limited", "2026-01-01T00:03:00Z")
	third, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("third sweep: %v", err)
	}
	if third.EventsEmitted != 1 {
		t.Fatalf("third sweep emitted %d events, want the returning warning", third.EventsEmitted)
	}
	if !strings.Contains(f.lastLine(t), "Rate limited") {
		t.Errorf("appended line does not carry the warning that returned: %s", f.lastLine(t))
	}
}

// Cursors and event ids are keyed by kind plus id, so two sources that happen to name a trace the
// same must both sync rather than displacing each other.
func TestSweepKeepsTracesWithTheSameIDAcrossSources(t *testing.T) {
	f := newSweepFixture(t)
	f.writeHistoryTask(t, "shared-1", []interface{}{
		userEntry(1700000000000, "<task>History task</task>"),
	})
	sessionDir := filepath.Join(f.clineDir, "data", "sessions", "shared-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(sessionDir, "shared-1.json"), map[string]interface{}{"task": "Session task", "workspacePath": "/repo"})
	writeJSON(t, filepath.Join(sessionDir, "shared-1.messages.json"), map[string]interface{}{"messages": []interface{}{
		map[string]interface{}{"role": "user", "content": "Session prompt"},
	}})

	summary, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary.Traces != 2 {
		t.Fatalf("summary.Traces = %d, want both the history task and the session", summary.Traces)
	}

	var prompts []string
	for _, event := range f.logLines(t) {
		if event.Prompt != nil {
			prompts = append(prompts, event.Prompt.Text)
		}
	}
	if len(prompts) != 2 {
		t.Fatalf("prompts = %v, want one from each source", prompts)
	}
}
