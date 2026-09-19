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

func TestMapTraceHonorsMinOrder(t *testing.T) {
	ref := TraceRef{ID: "ses_1", Kind: SourceSQLite}
	records := []Record{
		{Order: 1, NativeID: "one", Type: "user_message", Content: "first"},
		{Order: 2, NativeID: "two", Type: "user_message", Content: "second"},
	}
	mapped := MapTrace(ref, records, MapOptions{MinOrder: 1})
	if len(mapped) != 1 {
		t.Fatalf("mapped = %d, want 1", len(mapped))
	}
	if mapped[0].SourceOrder != 2 {
		t.Fatalf("source order = %d, want 2", mapped[0].SourceOrder)
	}
}
