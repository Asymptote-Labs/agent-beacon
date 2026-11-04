package asymptoteobserve

import (
	"encoding/json"
	"strings"
	"testing"
)

// metadataOnlyFixture is an event with every content field StripToMetadata removes set to text
// containing "secret", and metadata beside each.
func metadataOnlyFixture() Event {
	event := shareEventAt("2026-10-01T10:00:00Z", "claude_code", CollectionMethodHook, "command.executed", "command")
	event.Message = "secret message"
	event.Raw = map[string]interface{}{"tool_input": map[string]interface{}{"command": "secret raw"}}
	event.Prompt = &PromptInfo{ID: "p1", Text: "secret prompt"}
	event.Command = &CommandInfo{Command: "go test ./...", ExitCode: intPtr(1), DurationMS: 42, Output: "secret output"}
	event.File = &FileInfo{Path: "main.go", Operation: "modify", DiffHash: "sha256:d", DiffBytes: 11, Diff: "secret diff"}
	event.Approval = &ApprovalInfo{Required: true, Decision: "deny", Reason: "secret approval reason"}
	event.Policy = &PolicyInfo{ID: "pol-1", Decision: "deny", Enforcement: "enforce", Reason: "secret policy reason"}
	event.Content = &ContentInfo{Retention: ContentRetentionFull, Included: true, Hash: "sha256:c", Bytes: 13}
	event.Model = "claude-sonnet-5-5"
	event.GenAI = &GenAIInfo{
		Input:                  &GenAIInputInfo{Messages: TextInputMessages("secret input")},
		Output:                 &GenAIOutputInfo{Messages: TextOutputMessages("secret answer")},
		SystemInstructions:     SystemInstructionParts("secret system prompt"),
		SystemInstructionsText: "secret system prompt",
		Retrieval:              &GenAIRetrievalInfo{QueryText: "secret query", Documents: []interface{}{"secret document"}},
		Tool: &GenAIToolInfo{
			Name: "Bash", Type: "function", Description: "secret tool description", Definitions: []interface{}{"secret definition"},
			Call: &GenAIToolCallInfo{ID: "toolu_1", Arguments: map[string]interface{}{"command": "secret arguments"}, Result: "secret result", ResultText: "secret result"},
		},
		Evaluation: &GenAIEvaluationInfo{Name: "helpfulness", Explanation: "secret explanation"},
		Agent:      &GenAIAgentInfo{ID: "a1", Name: "reviewer", Description: "secret agent description"},
		Request:    &GenAIRequestInfo{Model: "claude-sonnet-5-5", StopSequences: []string{"secret stop"}},
		Usage:      &GenAIUsageInfo{InputTokens: int64Ptr(5), OutputTokens: int64Ptr(7)},
	}
	return event
}

func TestStripToMetadataRemovesContentAndKeepsMetadata(t *testing.T) {
	original := metadataOnlyFixture()
	before, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	stripped := original
	StripToMetadata(&stripped)
	after, err := json.Marshal(stripped)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "secret") {
		t.Fatalf("content survived StripToMetadata:\n%s", after)
	}
	// The threat-rules copies of the semconv values are never written, but are content all the same.
	if stripped.GenAI.SystemInstructionsText != "" || stripped.GenAI.Tool.Call.ResultText != "" {
		t.Fatalf("rule-only text kept: %q %q", stripped.GenAI.SystemInstructionsText, stripped.GenAI.Tool.Call.ResultText)
	}
	g := stripped.GenAI
	switch {
	case stripped.Prompt == nil || stripped.Prompt.ID != "p1":
		t.Errorf("prompt = %+v, want its id kept", stripped.Prompt)
	case stripped.Command.Command != "go test ./..." || stripped.Command.ExitCode == nil || *stripped.Command.ExitCode != 1 || stripped.Command.DurationMS != 42:
		t.Errorf("command = %+v", stripped.Command)
	case stripped.File.Path != "main.go" || stripped.File.Operation != "modify" || stripped.File.DiffHash != "sha256:d" || stripped.File.DiffBytes != 11:
		t.Errorf("file = %+v", stripped.File)
	case stripped.Approval.Decision != "deny" || !stripped.Approval.Required || stripped.Policy.ID != "pol-1" || stripped.Policy.Enforcement != "enforce":
		t.Errorf("approval = %+v policy = %+v", stripped.Approval, stripped.Policy)
	case g.Tool.Name != "Bash" || g.Tool.Type != "function" || g.Tool.Call.ID != "toolu_1":
		t.Errorf("tool = %+v call = %+v", g.Tool, g.Tool.Call)
	case g.Evaluation.Name != "helpfulness" || g.Agent.Name != "reviewer" || g.Agent.ID != "a1" || g.Request.Model != "claude-sonnet-5-5":
		t.Errorf("gen_ai = %+v", g)
	case g.Usage == nil || *g.Usage.InputTokens != 5 || *g.Usage.OutputTokens != 7 || stripped.Model != "claude-sonnet-5-5":
		t.Errorf("usage = %+v model = %q", g.Usage, stripped.Model)
	case stripped.Session == nil || stripped.Session.ID != "sess-1" || stripped.Event.Action != "command.executed":
		t.Errorf("identity = %+v %+v", stripped.Session, stripped.Event)
	}
	if c := stripped.Content; c == nil || c.Retention != ContentRetentionMetadata || c.Included || !c.Redacted || c.Hash != "sha256:c" {
		t.Errorf("content marker = %+v", c)
	}
	// The caller's copy shares every pointer with the stripped event and keeps all of it.
	if again, _ := json.Marshal(original); string(again) != string(before) {
		t.Fatalf("StripToMetadata changed another event's blocks:\nbefore %s\nafter  %s", before, again)
	}
	if original.GenAI.SystemInstructionsText == "" || original.GenAI.Tool.Call.ResultText == "" {
		t.Fatal("StripToMetadata changed another event's rule-only text")
	}
}

// A block left with nothing in it goes, as the Vector transform deletes an object it emptied; the
// command and file blocks stay, as they do there.
func TestStripToMetadataDropsBlocksItEmptied(t *testing.T) {
	event := Event{
		Prompt:   &PromptInfo{Text: "x"},
		Approval: &ApprovalInfo{Reason: "x"},
		Policy:   &PolicyInfo{Reason: "x"},
		Command:  &CommandInfo{Output: "x"},
		File:     &FileInfo{Diff: "x"},
		GenAI: &GenAIInfo{
			Input:      &GenAIInputInfo{Messages: TextInputMessages("x")},
			Tool:       &GenAIToolInfo{Description: "x", Call: &GenAIToolCallInfo{Arguments: "x"}},
			Evaluation: &GenAIEvaluationInfo{Explanation: "x"},
			Agent:      &GenAIAgentInfo{Description: "x"},
			Request:    &GenAIRequestInfo{StopSequences: []string{"x"}},
		},
	}
	StripToMetadata(&event)
	if event.Prompt != nil || event.Approval != nil || event.Policy != nil || event.GenAI != nil {
		t.Fatalf("emptied blocks kept: prompt=%+v approval=%+v policy=%+v gen_ai=%+v", event.Prompt, event.Approval, event.Policy, event.GenAI)
	}
	if event.Command == nil || event.File == nil || event.Content != nil {
		t.Fatalf("command=%+v file=%+v content=%+v", event.Command, event.File, event.Content)
	}
	partial := Event{GenAI: &GenAIInfo{Tool: &GenAIToolInfo{Name: "Bash", Call: &GenAIToolCallInfo{Result: "x"}}}}
	StripToMetadata(&partial)
	if partial.GenAI == nil || partial.GenAI.Tool == nil || partial.GenAI.Tool.Name != "Bash" || partial.GenAI.Tool.Call != nil {
		t.Fatalf("gen_ai = %+v", partial.GenAI)
	}
	StripToMetadata(nil)
}

// A Metadata-only share carries no prompt, answer, output, diff, argument, result or reason, and
// names every event exactly as a full share of the same session does.
func TestShareBundleMetadataOnly(t *testing.T) {
	events := shareFieldEvents()
	for i := range events {
		events[i].Event.ID = "" // derived from the content, which the strip must not change
	}
	full := BuildShareBundle(events, shareTestOpts)
	opts := shareTestOpts
	opts.MetadataOnly = true
	meta := BuildShareBundle(events, opts)
	if len(meta.Events) != len(full.Events) || len(meta.Events) == 0 {
		t.Fatalf("events: metadata-only %d, full %d", len(meta.Events), len(full.Events))
	}
	for i := range meta.Events {
		if meta.Events[i].ID != full.Events[i].ID || meta.Events[i].Type != full.Events[i].Type {
			t.Errorf("event %d: metadata-only %s/%s, full %s/%s", i, meta.Events[i].ID, meta.Events[i].Type, full.Events[i].ID, full.Events[i].Type)
		}
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	// The MCP resource URI is metadata Metadata-only forwarding keeps; the call's arguments and
	// result (a path and the text read from it) are not.
	for _, content := range []string{"todo.md", "No such file", "+++", `"path":"~/secret.txt"`, "from ~/secret.txt", "I changed", "writes to", "is protected", "permission denied", `"cwd"`, `"nested"`} {
		if strings.Contains(string(data), content) {
			t.Errorf("metadata-only bundle carries %q:\n%s", content, data)
		}
	}
	for _, kept := range []string{"cat ~/.config/app.toml", "main.go", "toolu_c", "mcp__fs__read", `"decision":"deny"`} {
		if !strings.Contains(string(data), kept) {
			t.Errorf("metadata-only bundle lost %q:\n%s", kept, data)
		}
	}
	if title := NewShareTrace(meta, "").Title; title != "" {
		t.Errorf("metadata-only title = %q, want none", title)
	}
	if NewShareTrace(full, "").Title == "" {
		t.Error("the full share has no title from its first prompt")
	}
	// The caller's events still carry their content.
	if events[0].Prompt == nil || events[0].Prompt.Text == "" || events[1].Command.Output == "" {
		t.Fatal("BuildShareBundle stripped the caller's events")
	}
}
