package asymptoteobserve

import (
	"reflect"
	"strings"
	"testing"
)

func TestShareMessagesToolTextFollowsEnrichment(t *testing.T) {
	tool := func(id, action, timestamp, name string) Event {
		e := shareEventAt(timestamp, "claude_code", CollectionMethodHook, action, "tool")
		e.Event.ID = id
		return withCall(e, "enriched-call", name, nil, nil)
	}
	const start = "2026-10-22T10:00:00Z"
	const later = "2026-10-22T10:00:01Z"
	const last = "2026-10-22T10:00:02Z"
	longCommand := "printf " + strings.Repeat("x", 1100)
	longOutput := strings.Repeat("y", 1100)
	cases := []struct {
		name   string
		events []Event
		seed   int
		call   string
		result string
		check  func(*testing.T, *TraceToolCallContentV1, *TraceToolResultContentV1)
	}{
		{
			name:   "completion enriches sparse invocation",
			events: []Event{tool("invoke", "tool.invoked", start, "Bash"), tool("complete", "command.executed", later, "Bash")},
			seed:   1, call: "Bash: printf done", result: "done",
		},
		{
			name:   "duplicate completion enriches sparse result",
			events: []Event{tool("sparse", "tool.completed", start, "Bash"), tool("rich", "command.executed", later, "Bash")},
			seed:   1, call: "Bash: printf done", result: "done",
		},
		{
			name:   "duplicate invocation enriches call",
			events: []Event{tool("invoke", "tool.invoked", start, "Bash"), tool("rich-invoke", "tool.invoked", later, "Bash"), tool("complete", "tool.completed", last, "Bash")},
			seed:   1, call: "Bash: printf done", result: "done",
		},
		{
			name:   "completion enriches file path",
			events: []Event{tool("invoke", "tool.invoked", start, "Write"), tool("complete", "file.modified", later, "Write")},
			seed:   1, call: "Write: main.go", result: "Write success",
			check: func(t *testing.T, call *TraceToolCallContentV1, _ *TraceToolResultContentV1) {
				if call.FilePath != "main.go" || call.Diff != "--- a/main.go\n+++ b/main.go\n" {
					t.Fatalf("file enrichment = %+v", call)
				}
			},
		},
		{
			name:   "complete first retains original fields",
			events: []Event{tool("invoke", "tool.invoked", start, "Bash"), tool("complete", "command.executed", later, "Bash"), tool("conflicting", "command.executed", last, "Bash")},
			seed:   2, call: "Bash: printf first", result: "first output",
			check: func(t *testing.T, call *TraceToolCallContentV1, result *TraceToolResultContentV1) {
				if call.Command != "printf first" || call.Args["command"] != "printf first" || result.Output != "first output" || result.ExitCode == nil || *result.ExitCode != 0 || result.DurationMS == nil || *result.DurationMS != 5 || result.Status != TraceToolStatusError {
					t.Fatalf("fill-only precedence changed: call=%+v result=%+v", call, result)
				}
			},
		},
		{
			name:   "empty output reflects promoted error status",
			events: []Event{tool("sparse", "tool.completed", start, "Bash"), tool("failed", "tool.failed", later, "Bash")},
			seed:   1, call: "Bash", result: "Bash error",
		},
		{
			name:   "retained output outranks promoted error status",
			events: []Event{tool("complete", "tool.completed", start, "Bash"), tool("failed", "tool.failed", later, "Bash")},
			seed:   1, call: "Bash", result: "retained output",
			check: func(t *testing.T, _ *TraceToolCallContentV1, result *TraceToolResultContentV1) {
				if result.Status != TraceToolStatusError {
					t.Fatalf("status = %q, want error", result.Status)
				}
			},
		},
		{
			name:   "complete first keeps formatter bounds",
			events: []Event{tool("complete", "command.executed", start, "Bash")},
			seed:   1, call: TruncateString("Bash: "+longCommand, 1024), result: TruncateString(longOutput, 1024),
		},
		{
			name:   "enriched text keeps formatter bounds",
			events: []Event{tool("sparse", "tool.completed", start, "Bash"), tool("rich", "command.executed", later, "Bash")},
			seed:   1, call: TruncateString("Bash: "+longCommand, 1024), result: TruncateString(longOutput, 1024),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			switch tc.name {
			case "completion enriches sparse invocation", "duplicate completion enriches sparse result":
				tc.events[1].Command = &CommandInfo{Command: "printf done", Output: "done", ExitCode: intPtr(0)}
			case "duplicate invocation enriches call":
				tc.events[1].Command = &CommandInfo{Command: "printf done"}
				tc.events[1].GenAI.Tool.Call.Arguments = map[string]interface{}{"command": "printf done"}
				tc.events[2].Command = &CommandInfo{Output: "done"}
			case "completion enriches file path":
				tc.events[1].File = &FileInfo{Path: "/home/dev/src/app/main.go", Operation: "modify", Diff: "--- a/main.go\n+++ b/main.go\n"}
			case "complete first retains original fields":
				tc.events[0].Command = &CommandInfo{Command: "printf first"}
				tc.events[0].GenAI.Tool.Call.Arguments = map[string]interface{}{"command": "printf first"}
				tc.events[1].Command = &CommandInfo{Output: "first output", ExitCode: intPtr(0), DurationMS: 5}
				tc.events[2].Command = &CommandInfo{Command: "printf second", Output: "second output", ExitCode: intPtr(1), DurationMS: 99}
				tc.events[2].GenAI.Tool.Call.Arguments = map[string]interface{}{"command": "printf second"}
			case "retained output outranks promoted error status":
				tc.events[0].Command = &CommandInfo{Output: "retained output"}
			case "complete first keeps formatter bounds":
				tc.events[0].Command = &CommandInfo{Command: longCommand, Output: longOutput}
			case "enriched text keeps formatter bounds":
				tc.events[1].Command = &CommandInfo{Command: longCommand, Output: longOutput}
			}
			seed := shareMessages(tc.events[:tc.seed], shareTestOpts)
			// Reverse the caller's order to exercise the public bundle's timestamp ordering.
			input := append([]Event(nil), tc.events...)
			for i, j := 0, len(input)-1; i < j; i, j = i+1, j-1 {
				input[i], input[j] = input[j], input[i]
			}
			messages := shareMessages(input, shareTestOpts)
			if len(messages) != 2 {
				t.Fatalf("message count = %d, want one call/result pair: %+v", len(messages), messages)
			}
			for i, message := range messages {
				kind := []string{TraceMessageKindToolCall, TraceMessageKindToolResult}[i]
				if message.Role != TraceRoleAssistant || message.Kind != kind || message.Order != i+1 || len(message.Parts) != 1 || message.Parts[0].Order != 1 || message.Parts[0].Type != kind {
					t.Fatalf("message structure = %+v", message)
				}
				if i < len(seed) && (message.ID != seed[i].ID || message.Timestamp != seed[i].Timestamp || message.Model != seed[i].Model || !reflect.DeepEqual(message.TokenUsage, seed[i].TokenUsage)) {
					t.Fatalf("enrichment changed original message identity: before=%+v after=%+v", seed[i], message)
				}
			}
			call := messages[0].Parts[0].Content.(*TraceToolCallContentV1)
			result := messages[1].Parts[0].Content.(*TraceToolResultContentV1)
			if call.CallID != "enriched-call" || result.CallID != call.CallID {
				t.Fatalf("pairing changed: call=%+v result=%+v", call, result)
			}
			if tc.check != nil {
				tc.check(t, call, result)
			}
			if messages[0].Text != tc.call || messages[1].Text != tc.result {
				t.Fatalf("final Text = (%q, %q), want (%q, %q); final Parts: call=%+v result=%+v", messages[0].Text, messages[1].Text, tc.call, tc.result, call, result)
			}
			if !reflect.DeepEqual(messages, shareMessages(input, shareTestOpts)) {
				t.Fatal("repeated projection changed stable messages")
			}
		})
	}
}

func TestShareMessagesToolEnrichmentLeavesReasoningAndUsageAlone(t *testing.T) {
	pre := withCall(shareEventAt("2026-10-22T10:00:00Z", "claude_code", CollectionMethodHook, "tool.invoked", "tool"), "usage-call", "Bash", nil, nil)
	pre.Event.ID = "usage-invoke"
	pre.GenAI.Usage = &GenAIUsageInfo{InputTokens: int64Ptr(7)}
	post := withCall(shareEventAt("2026-10-22T10:00:01Z", "claude_code", CollectionMethodHook, "tool.completed", "tool"), "usage-call", "Bash", nil, nil)
	post.Event.ID = "usage-complete"
	post.GenAI.Usage = &GenAIUsageInfo{OutputTokens: int64Ptr(3)}
	think := shareEventAt("2026-10-22T10:00:02Z", "claude_code", CollectionMethodHook, "agent.reasoning", "agent")
	think.Event.ID = "reasoning"
	think.Model = "synthetic-model"
	think.GenAI = &GenAIInfo{Output: &GenAIOutputInfo{Messages: ReasoningOutputMessages("Consider the cache.")}, Usage: &GenAIUsageInfo{OutputTokens: int64Ptr(2)}}
	answer := shareEventAt("2026-10-22T10:00:03Z", "claude_code", CollectionMethodHook, "agent.message", "agent")
	answer.Event.ID = "answer"
	answer.GenAI = &GenAIInfo{Output: &GenAIOutputInfo{Messages: TextOutputMessages("Finished.")}, Usage: &GenAIUsageInfo{OutputTokens: int64Ptr(11)}}
	metric := shareEventAt("2026-10-22T10:00:04Z", "claude_code", CollectionMethodOTLP, "token.usage", "metric")
	metric.Event.ID = "metric"
	metric.GenAI = &GenAIInfo{Usage: &GenAIUsageInfo{InputTokens: int64Ptr(17)}}
	events := []Event{pre, post, think, answer, metric}
	before := shareMessages(events, shareTestOpts)
	rich := withCall(shareEventAt("2026-10-22T10:00:05Z", "claude_code", CollectionMethodOTLP, "command.executed", "command"), "usage-call", "Bash", nil, nil)
	rich.Event.ID = "usage-enrichment"
	rich.Command = &CommandInfo{Command: "printf done", Output: "done"}
	rich.GenAI.Usage = &GenAIUsageInfo{InputTokens: int64Ptr(1000)}
	after := shareMessages(append(events, rich), shareTestOpts)
	if len(before) != 4 || len(after) != 4 {
		t.Fatalf("message counts before=%d after=%d, want tool pair + reasoning + answer", len(before), len(after))
	}
	if after[0].Text != "Bash: printf done" || after[1].Text != "done" {
		t.Fatalf("tool enrichment text = %q / %q", after[0].Text, after[1].Text)
	}
	if !reflect.DeepEqual(before[2:], after[2:]) || after[2].Kind != TraceMessageKindThinking || after[2].Text != "Consider the cache." || after[3].Kind != TraceMessageKindAssistant || after[3].Text != "Finished." {
		t.Fatalf("reasoning/answer changed: before=%+v after=%+v", before[2:], after[2:])
	}
	if after[0].TokenUsage == nil || after[0].TokenUsage.InputTokens != 7 || after[1].TokenUsage == nil || after[1].TokenUsage.OutputTokens != 3 {
		t.Fatalf("original tool usage changed: call=%+v result=%+v", after[0].TokenUsage, after[1].TokenUsage)
	}
}
