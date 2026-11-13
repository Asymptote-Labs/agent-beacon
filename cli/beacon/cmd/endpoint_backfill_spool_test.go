package cmd

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

func spoolSweepEvent(t *testing.T, workspace, sessionID, action string) schema.Event {
	t.Helper()
	event := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: "agent",
		Severity: schema.SeverityInfo,
		Harness:  schema.HarnessInfo{Name: "opencode", CollectionMethod: asymptoteobserve.CollectionMethodPlugin},
		Message:  action + " " + sessionID,
	})
	event.Session = &schema.SessionInfo{ID: sessionID, WorkingDirectory: workspace}
	return event
}

func stageSpoolSweepEvent(t *testing.T, workspace, sessionID string) localingest.Spool {
	t.Helper()
	dir, ok := asymptoteobserve.WorkspaceSpoolDir(workspace, "opencode", sessionID)
	if !ok {
		t.Fatalf("unsafe test session id %q", sessionID)
	}
	line, err := json.Marshal(spoolSweepEvent(t, workspace, sessionID, "prompt.submitted"))
	if err != nil {
		t.Fatal(err)
	}
	spool := localingest.Spool{Dir: dir}
	if dropped, err := spool.Append([][]byte{line}); err != nil || dropped != 0 {
		t.Fatalf("stage: dropped %d, err %v", dropped, err)
	}
	return spool
}

// A user sweep drains the spool of a session Beacon has recorded in that workspace, and leaves a
// spool for a session it never saw -- one a cloned repository could carry -- where it is.
func TestSweepDrainsWorkspaceSpoolsOfKnownSessionsOnly(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	stubBackfill(t, 1<<20, 1<<19)
	workspace := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if _, err := writer.AppendEvent(spoolSweepEvent(t, workspace, "s1", "session.started"), writer.Options{Path: logPath, UserMode: true}); err != nil {
		t.Fatal(err)
	}
	known := stageSpoolSweepEvent(t, workspace, "s1")
	planted := stageSpoolSweepEvent(t, workspace, "invented")

	result := sweepEndpointBackfill(logPath, true, endpointSweep{})

	if result.Events != 1 || len(result.Runtimes) != 1 || result.Runtimes[0].Name != "Workspace spools" || result.Runtimes[0].Events != 1 {
		t.Fatalf("result = %+v, want one drained event under Workspace spools", result)
	}
	if lines, _, err := known.Pending(); err != nil || lines != 0 {
		t.Fatalf("known session's spool holds %d lines (err %v), want it drained", lines, err)
	}
	if lines, _, err := planted.Pending(); err != nil || lines != 1 {
		t.Fatalf("unknown session's spool holds %d lines (err %v), want it left alone", lines, err)
	}
}
