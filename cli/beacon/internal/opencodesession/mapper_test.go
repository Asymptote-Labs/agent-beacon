package opencodesession

import "testing"

func TestMapTraceMarksPollProvenanceAndClassifiesCommand(t *testing.T) {
	ref := TraceRef{ID: "ses_1", Kind: SourceSQLite, Directory: "/repo", UpdatedAtUnixMS: 1770000000000}
	records := []Record{
		{Order: 1, NativeID: "user", TimestampMS: 1770000000100, Type: "user_message", Content: "run tests"},
		{Order: 2, NativeID: "tool", TimestampMS: 1770000000200, Type: "tool_result", ToolName: "bash", CallID: "call_1", Args: map[string]interface{}{"command": "go test ./..."}, Output: map[string]interface{}{"output": "ok", "metadata": map[string]interface{}{"exit": 0}}, Status: "completed"},
	}
	mapped := MapTrace(ref, records, MapOptions{})
	if len(mapped) != 2 {
		t.Fatalf("mapped = %d, want 2", len(mapped))
	}
	for _, item := range mapped {
		if item.Event.Harness.Name != Harness {
			t.Fatalf("harness = %q", item.Event.Harness.Name)
		}
		if item.Event.Harness.CollectionMethod != "poll" {
			t.Fatalf("collection_method = %q, want poll", item.Event.Harness.CollectionMethod)
		}
		if item.Event.Event.Fidelity != "observed" {
			t.Fatalf("fidelity = %q, want observed", item.Event.Event.Fidelity)
		}
	}
	command := mapped[1].Event
	if command.Event.Action != "command.executed" {
		t.Fatalf("action = %q, want command.executed", command.Event.Action)
	}
	if command.Command == nil || command.Command.Command != "go test ./..." {
		t.Fatalf("command fields = %+v", command.Command)
	}
	if command.GenAI == nil || command.GenAI.Tool == nil || command.GenAI.Tool.Call == nil || command.GenAI.Tool.Call.ID != "call_1" {
		t.Fatalf("gen_ai tool call = %+v", command.GenAI)
	}
	if command.Event.ID == "" || command.Event.ID != MapTrace(ref, records, MapOptions{})[1].Event.Event.ID {
		t.Fatalf("event id was not deterministic: %q", command.Event.ID)
	}
}

func TestMapTraceGivesAToolCallAndItsCompletionDifferentIdentities(t *testing.T) {
	ref := TraceRef{ID: "ses_1", Kind: SourceSQLite}
	pending := Record{Order: 1, NativeID: "msg:part", Type: "tool_call", ToolName: "bash", CallID: "call_1", Args: map[string]interface{}{"command": "go test ./..."}, Status: "running"}
	invoked := MapTrace(ref, []Record{pending}, MapOptions{})
	if len(invoked) != 1 || invoked[0].Event.Event.Action != "tool.invoked" {
		t.Fatalf("invocation = %+v", invoked)
	}

	// OpenCode rewrites the same part when the call returns. The completion is what carries the
	// output, exit code and diff, so it has to reach the log as its own event rather than being
	// mistaken for the invocation Beacon already wrote.
	done := pending
	done.Type = "tool_result"
	done.Status = "completed"
	done.Output = map[string]interface{}{"output": "ok", "metadata": map[string]interface{}{"exit": 0}}
	finished := MapTrace(ref, []Record{done}, MapOptions{})
	if len(finished) != 1 {
		t.Fatalf("completion = %+v", finished)
	}
	if finished[0].Event.Event.Action != "command.executed" {
		t.Fatalf("action = %q, want command.executed", finished[0].Event.Event.Action)
	}
	if finished[0].Event.Command == nil || finished[0].Event.Command.ExitCode == nil || *finished[0].Event.Command.ExitCode != 0 {
		t.Fatalf("command = %+v", finished[0].Event.Command)
	}
	if finished[0].Event.Event.ID == invoked[0].Event.Event.ID {
		t.Fatalf("invocation and completion share event id %q", finished[0].Event.Event.ID)
	}
}

func TestMapTraceEventIDSurvivesAShiftedPosition(t *testing.T) {
	ref := TraceRef{ID: "ses_1", Kind: SourceSQLite}
	record := Record{NativeID: "msg:part", Type: "user_message", Content: "run tests"}

	first := record
	first.Order = 1
	shifted := record
	shifted.Order = 7

	before := MapTrace(ref, []Record{first}, MapOptions{})
	after := MapTrace(ref, []Record{shifted}, MapOptions{})
	if len(before) != 1 || len(after) != 1 {
		t.Fatalf("mapped = %d, %d, want 1 each", len(before), len(after))
	}
	if before[0].Event.Event.ID != after[0].Event.Event.ID {
		t.Fatalf("event id moved with position: %q vs %q", before[0].Event.Event.ID, after[0].Event.Event.ID)
	}
}

func TestApplyCommandRejectsAnExitCodeThatIsNotOne(t *testing.T) {
	ref := TraceRef{ID: "ses_1", Kind: SourceSQLite}
	for name, metadata := range map[string]map[string]interface{}{
		"string status": {"status": "completed"},
		"out of range":  {"exit": float64(int64(1) << 40)},
	} {
		t.Run(name, func(t *testing.T) {
			records := []Record{{Order: 1, NativeID: "msg:part", Type: "tool_result", ToolName: "bash", Status: "completed",
				Args:   map[string]interface{}{"command": "go test ./..."},
				Output: map[string]interface{}{"output": "ok", "metadata": metadata}}}
			mapped := MapTrace(ref, records, MapOptions{})
			if len(mapped) != 1 {
				t.Fatalf("mapped = %d, want 1", len(mapped))
			}
			if command := mapped[0].Event.Command; command == nil || command.ExitCode != nil {
				t.Fatalf("exit code = %+v, want none", command)
			}
		})
	}
}
