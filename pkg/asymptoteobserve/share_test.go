package asymptoteobserve

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func shareEventAt(ts, harness, method, action, category string) Event {
	return Event{
		Timestamp:     ts,
		Vendor:        Vendor,
		Product:       Product,
		SchemaVersion: SchemaVersion,
		Event:         EventInfo{Kind: "agent_runtime", Action: action, Category: category},
		Severity:      SeverityInfo,
		Endpoint:      EndpointInfo{OS: "linux"},
		Harness:       HarnessInfo{Name: harness, CollectionMethod: method},
		Session:       &SessionInfo{ID: "sess-1", WorkingDirectory: "/home/dev/src/app"},
	}
}

func withCall(e Event, id, name string, args interface{}, result interface{}) Event {
	if e.GenAI == nil {
		e.GenAI = &GenAIInfo{}
	}
	e.GenAI.Tool = &GenAIToolInfo{Name: name, Call: &GenAIToolCallInfo{ID: id, Arguments: args, Result: result}}
	if e.Tool == nil {
		e.Tool = &ToolInfo{Name: name}
	}
	return e
}

var shareTestOpts = ShareOptions{HomeDirectory: "/home/dev", NoHomeLookup: true}

// shareMessages is the chat view of the shared bundle built from events: the path every message a
// shared trace shows goes through.
func shareMessages(events []Event, opts ShareOptions) []TraceMessageV1 {
	return TraceMessages(BuildShareBundle(events, opts))
}

func partsOf(t *testing.T, messages []TraceMessageV1) []string {
	t.Helper()
	var out []string
	for _, m := range messages {
		for _, p := range m.Parts {
			out = append(out, m.Role+"/"+p.Type)
		}
	}
	return out
}

func TestShareMessagesPairsClaudeHookPreAndPost(t *testing.T) {
	prompt := shareEventAt("2026-10-01T10:00:00.000000000Z", "claude_code", CollectionMethodHook, "prompt.submitted", "prompt")
	prompt.Event.ID = "e-prompt"
	prompt.Prompt = &PromptInfo{Text: "List the files in /home/dev/src/app please"}

	pre := shareEventAt("2026-10-01T10:00:01.000000000Z", "claude_code", CollectionMethodHook, "tool.invoked", "tool")
	pre.Event.ID = "e-pre"
	pre = withCall(pre, "toolu_1", "Bash", nil, nil)
	pre.Tool.Command = "ls -la /home/dev/src/app/cmd"
	pre.Command = &CommandInfo{Command: "ls -la /home/dev/src/app/cmd"}
	pre.Raw = map[string]interface{}{"tool_input": map[string]interface{}{"command": "ls -la /home/dev/src/app/cmd"}}

	post := shareEventAt("2026-10-01T10:00:02.000000000Z", "claude_code", CollectionMethodHook, "command.executed", "command")
	post.Event.ID = "e-post"
	post = withCall(post, "toolu_1", "Bash", nil, nil)
	post.Command = &CommandInfo{Command: "ls -la /home/dev/src/app/cmd", ExitCode: intPtr(0), Output: "main.go\n"}

	// The OTLP report of the same call: a second completion that only adds the duration.
	otlp := shareEventAt("2026-10-01T10:00:02.500000000Z", "claude_code", CollectionMethodOTLP, "command.executed", "command")
	otlp.Event.ID = "e-otlp"
	otlp = withCall(otlp, "toolu_1", "Bash", nil, nil)
	otlp.Command = &CommandInfo{Command: "ls -la /home/dev/src/app/cmd", DurationMS: 42}

	answer := shareEventAt("2026-10-01T10:00:03.000000000Z", "claude_code", CollectionMethodHook, "agent.message", "agent")
	answer.Event.ID = "e-answer"
	answer.Model = "claude-sonnet-5-5"
	answer.GenAI = &GenAIInfo{Output: &GenAIOutputInfo{Messages: TextOutputMessages("There is one file.")}}

	usage := shareEventAt("2026-10-01T10:00:03.000000000Z", "claude_code", CollectionMethodOTLP, "token.usage", "metric")
	usage.GenAI = &GenAIInfo{Usage: &GenAIUsageInfo{InputTokens: int64Ptr(10)}}

	// Given out of order, and with the session.shared record that must never become a message.
	shared := shareEventAt("2026-10-01T11:00:00.000000000Z", "claude_code", "", "session.shared", "session")
	messages := shareMessages([]Event{answer, otlp, shared, post, usage, pre, prompt}, shareTestOpts)

	got := partsOf(t, messages)
	want := []string{"user/text", "assistant/tool_call", "assistant/tool_result", "assistant/text"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parts = %v, want %v", got, want)
	}
	for i, m := range messages {
		if m.Order != i+1 {
			t.Fatalf("message %d order = %d", i, m.Order)
		}
	}
	if messages[0].ID != "e-prompt" || messages[1].ID != "e-pre" || messages[2].ID != "e-post" {
		t.Fatalf("ids = %s %s %s", messages[0].ID, messages[1].ID, messages[2].ID)
	}
	if text := messages[0].Text; text != "List the files in . please" {
		t.Fatalf("prompt text not scrubbed: %q", text)
	}
	call := messages[1].Parts[0].Content.(*TraceToolCallContentV1)
	if call.CallID != "toolu_1" || call.ToolKind != TraceToolKindTerminalCommand || call.Command != "ls -la ./cmd" {
		t.Fatalf("call = %+v", call)
	}
	if call.Args["command"] != "ls -la ./cmd" {
		t.Fatalf("args = %+v", call.Args)
	}
	result := messages[2].Parts[0].Content.(*TraceToolResultContentV1)
	if result.CallID != "toolu_1" || result.Output != "main.go\n" || result.Status != TraceToolStatusSuccess {
		t.Fatalf("result = %+v", result)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || result.DurationMS == nil || *result.DurationMS != 42 {
		t.Fatalf("result exit/duration = %+v", result)
	}
	if messages[3].Model != "claude-sonnet-5-5" || messages[3].Text != "There is one file." {
		t.Fatalf("answer = %+v", messages[3])
	}
}

func TestShareMessagesSynthesizesCallForPollCompletion(t *testing.T) {
	cmd := shareEventAt("2026-10-01T10:00:00Z", "codex", CollectionMethodPoll, "command.executed", "command")
	cmd.Event.ID = "e-cmd"
	cmd = withCall(cmd, "call_9", "exec_command", map[string]interface{}{"cmd": "go test ./..."}, nil)
	cmd.Command = &CommandInfo{Command: "go test ./...", ExitCode: intPtr(1), Output: "FAIL"}

	messages := shareMessages([]Event{cmd}, shareTestOpts)
	if got := partsOf(t, messages); strings.Join(got, ",") != "assistant/tool_call,assistant/tool_result" {
		t.Fatalf("parts = %v", got)
	}
	if messages[0].ID == "e-cmd" || messages[1].ID != "e-cmd" {
		t.Fatalf("ids = %s %s", messages[0].ID, messages[1].ID)
	}
	again := shareMessages([]Event{cmd}, shareTestOpts)
	if again[0].ID != messages[0].ID {
		t.Fatalf("synthesized call id is not stable: %s vs %s", again[0].ID, messages[0].ID)
	}
	call := messages[0].Parts[0].Content.(*TraceToolCallContentV1)
	if call.CallID != "call_9" || call.ToolKind != TraceToolKindTerminalCommand || call.Args["cmd"] != "go test ./..." {
		t.Fatalf("call = %+v", call)
	}
	result := messages[1].Parts[0].Content.(*TraceToolResultContentV1)
	if result.Status != TraceToolStatusError || result.ExitCode == nil || *result.ExitCode != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestShareMessagesPairsCallsWithoutRuntimeIDs(t *testing.T) {
	// Cursor-shaped: no tool call id on either hook. The edit's diff arrives on the completion and
	// belongs on the call.
	before := shareEventAt("2026-10-01T10:00:00Z", "cursor", CollectionMethodHook, "tool.invoked", "tool")
	before.Event.ID = "c-before"
	before.Tool = &ToolInfo{Name: "Shell", Command: "npm test"}
	before.Command = &CommandInfo{Command: "npm test"}
	after := shareEventAt("2026-10-01T10:00:05Z", "cursor", CollectionMethodHook, "command.executed", "command")
	after.Event.ID = "c-after"
	after.Tool = &ToolInfo{Name: "Shell", Command: "npm test"}
	after.Command = &CommandInfo{Command: "npm test", ExitCode: intPtr(0), Output: "ok"}
	edit := shareEventAt("2026-10-01T10:00:06Z", "cursor", CollectionMethodHook, "file.modified", "file")
	edit.Event.ID = "c-edit"
	edit.File = &FileInfo{Path: "/home/dev/src/app/main.go", Operation: "modify", Diff: "--- a/main.go\n+++ b/main.go\n"}
	read := shareEventAt("2026-10-01T10:00:07Z", "cursor", CollectionMethodHook, "file.read", "file")
	read.Event.ID = "c-read"
	read.File = &FileInfo{Path: "/home/dev/notes.txt", Operation: "read"}

	messages := shareMessages([]Event{before, after, edit, read}, shareTestOpts)
	want := "assistant/tool_call,assistant/tool_result,assistant/tool_call,assistant/tool_result,assistant/tool_call"
	if got := partsOf(t, messages); strings.Join(got, ",") != want {
		t.Fatalf("parts = %v", got)
	}
	call := messages[0].Parts[0].Content.(*TraceToolCallContentV1)
	result := messages[1].Parts[0].Content.(*TraceToolResultContentV1)
	if result.CallID != call.CallID || call.CallID != "c-before" {
		t.Fatalf("not paired: call %q result %q", call.CallID, result.CallID)
	}
	editCall := messages[2].Parts[0].Content.(*TraceToolCallContentV1)
	if editCall.FilePath != "main.go" || editCall.ToolKind != TraceToolKindEdit || editCall.Diff == "" {
		t.Fatalf("edit call = %+v", editCall)
	}
	readCall := messages[4].Parts[0].Content.(*TraceToolCallContentV1)
	if readCall.FilePath != "~/notes.txt" || readCall.ToolKind != TraceToolKindRead {
		t.Fatalf("read call = %+v", readCall)
	}
}

func TestShareMessagesTranscriptMCPAndApproval(t *testing.T) {
	invoke := shareEventAt("2026-10-01T10:00:00Z", "claude_code", CollectionMethodPoll, "mcp.tool_invoked", "mcp")
	invoke.Event.ID = "m-invoke"
	invoke = withCall(invoke, "toolu_m", "mcp__github__search", map[string]interface{}{"q": "beacon"}, nil)
	invoke.MCP = &MCPInfo{Server: "github", Tool: "search"}
	done := shareEventAt("2026-10-01T10:00:01Z", "claude_code", CollectionMethodPoll, "tool.completed", "tool")
	done.Event.ID = "m-done"
	done = withCall(done, "toolu_m", "mcp__github__search", nil, []interface{}{map[string]interface{}{"type": "text", "text": "3 results"}})
	approval := shareEventAt("2026-10-01T10:00:02Z", "opencode", CollectionMethodPlugin, "approval.denied", "approval")
	approval.Event.ID = "m-approval"
	approval.Approval = &ApprovalInfo{Required: true, Decision: "deny", Reason: "not now"}
	approval.Tool = &ToolInfo{Name: "bash"}
	start := shareEventAt("2026-09-30T10:00:00Z", "claude_code", CollectionMethodHook, "session.started", "session")
	start.Event.ID = "m-start"
	failure := shareEventAt("2026-10-01T10:00:03Z", "opencode", CollectionMethodPlugin, "session.error", "session")
	failure.Event.ID = "m-error"
	failure.Message = "provider returned 500"
	reasoning := shareEventAt("2026-10-01T10:00:04Z", "cursor", CollectionMethodHook, "agent.reasoning", "agent")
	reasoning.Event.ID = "m-think"
	reasoning.GenAI = &GenAIInfo{Output: &GenAIOutputInfo{Messages: ReasoningOutputMessages("Consider the cache.")}}

	messages := shareMessages([]Event{invoke, done, approval, start, failure, reasoning}, shareTestOpts)
	want := "system/system_event,assistant/tool_call,assistant/tool_result,system/system_event,system/error,assistant/thinking"
	if got := partsOf(t, messages); strings.Join(got, ",") != want {
		t.Fatalf("parts = %v", got)
	}
	call := messages[1].Parts[0].Content.(*TraceToolCallContentV1)
	if call.ToolKind != TraceToolKindMCP || call.Args["q"] != "beacon" {
		t.Fatalf("mcp call = %+v", call)
	}
	if result := messages[2].Parts[0].Content.(*TraceToolResultContentV1); result.Output != "3 results" || result.CallID != "toolu_m" {
		t.Fatalf("mcp result = %+v", result)
	}
	ev := messages[3].Parts[0].Content.(map[string]interface{})
	if ev["subtype"] != "approval" || ev["decision"] != "deny" || ev["toolName"] != "bash" {
		t.Fatalf("approval = %+v", ev)
	}
	if messages[4].Parts[0].Content.(TraceErrorContentV1).Message != "provider returned 500" {
		t.Fatalf("error = %+v", messages[4])
	}
}

func TestShareMessagesSanitizesContent(t *testing.T) {
	prompt := shareEventAt("2026-10-01T10:00:00Z", "codex", CollectionMethodHook, "prompt.submitted", "prompt")
	prompt.Prompt = &PromptInfo{Text: "use api_key=supersecretvalue123 to call it"}
	cmd := shareEventAt("2026-10-01T10:00:01Z", "codex", CollectionMethodHook, "command.executed", "command")
	cmd.Command = &CommandInfo{Command: "curl -H 'Authorization: Bearer abc.def.ghi' x", Output: "token=zzzzzzzzzzzz"}

	messages := shareMessages([]Event{prompt, cmd}, shareTestOpts)
	data, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"supersecretvalue123", "abc.def.ghi", "zzzzzzzzzzzz"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("secret %q reached the payload: %s", secret, data)
		}
	}
	if messages[0].ID == "" || messages[0].ID != ShareEventID(prompt) {
		t.Fatalf("derived id %q does not match ShareMessageID", messages[0].ID)
	}
}

func TestShareMessagesIsDeterministic(t *testing.T) {
	a := shareEventAt("2026-10-01T10:00:00Z", "codex", CollectionMethodHook, "prompt.submitted", "prompt")
	a.Event.ID = "b"
	a.Prompt = &PromptInfo{Text: "one"}
	b := shareEventAt("2026-10-01T10:00:00Z", "codex", CollectionMethodHook, "prompt.submitted", "prompt")
	b.Event.ID = "a"
	b.Prompt = &PromptInfo{Text: "two"}
	first, _ := json.Marshal(shareMessages([]Event{a, b}, shareTestOpts))
	second, _ := json.Marshal(shareMessages([]Event{b, a}, shareTestOpts))
	if string(first) != string(second) {
		t.Fatalf("order depends on input:\n%s\n%s", first, second)
	}
	if messages := shareMessages([]Event{a, b}, shareTestOpts); messages[0].ID != "a" || messages[0].Order != 1 {
		t.Fatalf("ties are not broken by id: %s", first)
	}
}

func TestTraceToolKind(t *testing.T) {
	cases := []struct {
		name, action, category string
		mcp                    bool
		want                   string
	}{
		{"Bash", "tool.invoked", "tool", false, TraceToolKindTerminalCommand},
		{"exec_command", "", "", false, TraceToolKindTerminalCommand},
		{"run_terminal_cmd", "", "", false, TraceToolKindTerminalCommand},
		{"shell", "", "", false, TraceToolKindTerminalCommand},
		{"Read", "", "", false, TraceToolKindRead},
		{"view", "", "", false, TraceToolKindRead},
		{"read_file", "", "", false, TraceToolKindRead},
		{"Edit", "", "", false, TraceToolKindEdit},
		{"MultiEdit", "", "", false, TraceToolKindEdit},
		{"apply_patch", "", "", false, TraceToolKindEdit},
		{"str_replace_editor", "", "", false, TraceToolKindEdit},
		{"Write", "", "", false, TraceToolKindWrite},
		{"create_file", "", "", false, TraceToolKindWrite},
		{"Grep", "", "", false, TraceToolKindGrep},
		{"Glob", "", "", false, TraceToolKindGlob},
		{"WebSearch", "", "", false, TraceToolKindWebSearch},
		{"WebFetch", "", "", false, TraceToolKindWebFetch},
		{"Task", "", "", false, TraceToolKindTask},
		{"TodoWrite", "", "", false, TraceToolKindTodo},
		{"update_plan", "", "", false, TraceToolKindTodo},
		{"mcp__github__search", "", "", false, TraceToolKindMCP},
		{"search_issues", "mcp.tool_invoked", "mcp", true, TraceToolKindMCP},
		{"", "file.read", "file", false, TraceToolKindRead},
		{"", "file.created", "file", false, TraceToolKindWrite},
		{"", "command.executed", "command", false, TraceToolKindTerminalCommand},
		{"frobnicate", "tool.invoked", "tool", false, TraceToolKindOther},
	}
	for _, tc := range cases {
		if got := TraceToolKind(tc.name, tc.action, tc.category, tc.mcp); got != tc.want {
			t.Errorf("TraceToolKind(%q, %q) = %q, want %q", tc.name, tc.action, got, tc.want)
		}
	}
}

func TestShareExternalID(t *testing.T) {
	cases := []struct{ harness, session, want string }{
		{"claude", "3f1c2e4a-0000-4000-8000-000000000001", "claude_code:3f1c2e4a-0000-4000-8000-000000000001"},
		{"codex", "rollout:abc.1", "codex_cli:rollout:abc.1"},
		{"cursor", "has spaces/slash", "cursor:sha256-"},
		{"claude_code", strings.Repeat("a", 300), "claude_code:sha256-"},
	}
	for _, tc := range cases {
		got := ShareExternalID(tc.harness, tc.session)
		if !ValidShareExternalID(got) {
			t.Fatalf("ShareExternalID(%q, %q) = %q is not a valid external id", tc.harness, tc.session, got)
		}
		if tc.want != "" && !strings.HasPrefix(got, tc.want) {
			t.Fatalf("ShareExternalID(%q, %q) = %q, want prefix %q", tc.harness, tc.session, got, tc.want)
		}
	}
	hashed := ShareExternalID("cursor", "has spaces/slash")
	if !strings.HasPrefix(hashed, "cursor:sha256-") || len(hashed) != len("cursor:sha256-")+32 {
		t.Fatalf("hashed id = %q", hashed)
	}
	if ShareExternalID("cursor", "has spaces/slash") != hashed {
		t.Fatal("hashed id is not stable")
	}
}

func TestShareBundleSummary(t *testing.T) {
	start := shareEventAt("2026-10-01T10:00:00Z", "claude", CollectionMethodHook, "session.started", "session")
	start.Branch = "main"
	prompt := shareEventAt("2026-10-01T10:00:01Z", "claude", CollectionMethodHook, "prompt.submitted", "prompt")
	prompt.Prompt = &PromptInfo{Text: "Fix the flaky login test in /home/dev/src/app/login_test.go, it fails about one run in ten on CI and I want it gone\n[beacon-handoff from=codex session=abc123]"}
	m1 := shareEventAt("2026-10-01T10:00:02Z", "claude", CollectionMethodOTLP, "agent.message", "agent")
	m1.Model = "claude-sonnet-5-5"
	m1.GenAI = &GenAIInfo{Usage: &GenAIUsageInfo{InputTokens: int64Ptr(100), OutputTokens: int64Ptr(20), CostUSD: float64Ptr(0.25),
		CacheRead: &GenAIUsageCacheReadInfo{InputTokens: int64Ptr(5)}}}
	m2 := shareEventAt("2026-10-01T10:00:03Z", "claude", CollectionMethodOTLP, "agent.message", "agent")
	m2.Model = "claude-sonnet-5-5"
	m2.GenAI = &GenAIInfo{Usage: &GenAIUsageInfo{InputTokens: int64Ptr(50), Reasoning: &GenAIUsageReasoningInfo{OutputTokens: int64Ptr(7)}}}
	m3 := shareEventAt("2026-10-01T10:00:04Z", "claude", CollectionMethodOTLP, "agent.message", "agent")
	m3.Model = "claude-haiku-5"
	link := shareEventAt("2026-10-01T10:00:05Z", "claude", "", "session.commit_linked", "session")
	link.VCS = &VCSInfo{
		Ref:        &VCSRefInfo{Head: &VCSRefHeadInfo{Revision: "abc123def", Name: "main", Type: "branch"}},
		Repository: &VCSRepositoryInfo{URL: &VCSRepositoryURLInfo{Full: "https://x-access-token:ghp_secret@github.com/acme/repo.git?x=1"}},
	}
	handoff := shareEventAt("2026-10-01T10:00:01Z", "claude", CollectionMethodHook, "session.handoff", "session")
	handoff.Handoff = &HandoffInfo{SourceHarness: "codex", SourceSessionID: "abc123"}

	opts := shareTestOpts
	opts.BeaconVersion = "v0.0.99"
	opts.GeneratedAt = time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	bundle := BuildShareBundle([]Event{link, m3, m2, m1, prompt, handoff, start}, opts)
	summary := bundle.Summary
	if bundle.SchemaVersion != TraceSchemaVersion || bundle.ID != "session:claude_code:sess-1" || summary.ID != bundle.ID {
		t.Fatalf("identity = %q %q", bundle.SchemaVersion, bundle.ID)
	}
	if bundle.Provenance != (TraceProvenanceV1{Source: ShareSourceCLI, GeneratedAt: "2026-10-02T00:00:00.000000000Z", BeaconVersion: "v0.0.99"}) {
		t.Fatalf("provenance = %+v", bundle.Provenance)
	}
	// The commit link is bookkeeping: it is not an event of the shared trace, but it names the commit.
	if summary.EventCount != 6 || len(bundle.Events) != 6 {
		t.Fatalf("event count = %d, events = %d", summary.EventCount, len(bundle.Events))
	}
	if summary.Model == nil || len(summary.Model.Names) != 2 {
		t.Fatalf("model = %+v", summary.Model)
	}
	want := TraceUsageV1{InputTokens: 150, OutputTokens: 20, CacheReadInputTokens: 5, ReasoningOutputTokens: 7, CostUSD: 0.25}
	if summary.TokenUsage == nil || *summary.TokenUsage != want {
		t.Fatalf("usage = %+v", summary.TokenUsage)
	}
	wantRepo := TraceRepositoryV1{RemoteURL: "https://github.com/acme/repo.git", Branch: "main", Ref: "abc123def"}
	if summary.Repository == nil || *summary.Repository != wantRepo {
		t.Fatalf("repository = %+v", summary.Repository)
	}
	if summary.Project == nil || summary.Project.Name != "repo" {
		t.Fatalf("project = %+v", summary.Project)
	}
	if summary.Session == nil || summary.Session.ID != "sess-1" || summary.Session.WorkingDirectory != "" {
		t.Fatalf("session = %+v", summary.Session)
	}
	if summary.StartedAt != "2026-10-01T10:00:00Z" || summary.EndedAt != "2026-10-01T10:00:04Z" {
		t.Fatalf("range = %s .. %s", summary.StartedAt, summary.EndedAt)
	}
	if summary.Harness.Name != "claude" || strings.Join(summary.Harness.CollectionMethods, ",") != "hook,otlp" {
		t.Fatalf("harness = %+v", summary.Harness)
	}

	trace := NewShareTrace(bundle, ShareVisibilityDirect)
	if trace.SchemaVersion != TraceSchemaVersion || trace.Source != ShareSourceCLI || trace.Visibility != ShareVisibilityDirect {
		t.Fatalf("trace = %+v", trace)
	}
	if trace.Title != "Fix the flaky login test in ./login_test.go, it fails about one run in ten on C…" {
		t.Fatalf("title = %q", trace.Title)
	}
	if n := len([]rune(trace.Title)); n > ShareTitleMaxRunes {
		t.Fatalf("title has %d runes", n)
	}
	if trace.Summary.Sharing.Visibility != ShareVisibilityDirect || trace.Provenance != bundle.Provenance {
		t.Fatalf("sharing = %+v, provenance = %+v", trace.Summary.Sharing, trace.Provenance)
	}
}

func TestShareRemoteURL(t *testing.T) {
	cases := map[string]string{
		"https://user:pw@github.com/acme/repo.git":        "https://github.com/acme/repo.git",
		"git@github.com:acme/repo.git":                    "github.com:acme/repo.git",
		"ssh://git@host:22/acme/repo":                     "ssh://host:22/acme/repo",
		"https://github.com/acme/repo?token=abc":          "https://github.com/acme/repo",
		"https://user:pw@git.example.test?token=abc":      "https://git.example.test",
		"https://git.example.test#frag":                   "https://git.example.test",
		"https://git.example.test?redirect=/x@other.test": "https://git.example.test",
		"":                              "",
		"github.com:acme/repo.git":      "github.com:acme/repo.git",
		"/home/alice/src/foo":           "",
		"../foo.git":                    "",
		"~/src/foo":                     "",
		"file:///Users/alice/repos/foo": "",
		`C:\Users\alice\repo`:           "",
		"./a:b":                         "",
	}
	for in, want := range cases {
		if got := ShareRemoteURL(in); got != want {
			t.Errorf("ShareRemoteURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestShareWireShape pins the JSON field names and shapes of the shared-trace contract, so a rename
// here cannot drift from what beacon.sh accepts.
func TestShareWireShape(t *testing.T) {
	trace := ShareTraceV1{
		SchemaVersion: TraceSchemaVersion,
		Summary:       TraceSummaryV1{ID: "session:claude_code:s", EventCount: 1, Harness: TraceHarnessV1{Name: "claude_code"}, Content: TraceContentSummaryV1{Retention: "full"}, Sharing: TraceSharingV1{State: "shared", Visibility: "direct"}},
		Visibility:    ShareVisibilityDirect,
		Title:         "t",
		Source:        ShareSourceCLI,
		Provenance:    TraceProvenanceV1{Source: "cli", GeneratedAt: "g"},
	}
	got, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":"beacon.trace.v1","summary":{"id":"session:claude_code:s","event_count":1,"harness":{"name":"claude_code"},` +
		`"content":{"retention":"full","has_redactions":false,"has_truncations":false},"sharing":{"state":"shared","visibility":"direct"}},` +
		`"visibility":"direct","title":"t","source":"cli","provenance":{"source":"cli","generated_at":"g"}}`
	if string(got) != want {
		t.Fatalf("trace JSON drifted:\n got %s\nwant %s", got, want)
	}

	batch := ShareEventsBatchV1{Events: []TraceEventV1{{ID: "e1", Number: 1, Timestamp: "ts", Type: "command", Action: "command.executed", PromptID: "p", SourceEventID: "e1",
		Command: &TraceCommandV1{Command: "ls", ExitCode: intPtr(0)}}}}
	got, err = json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	want = `{"events":[{"id":"e1","number":1,"timestamp":"ts","type":"command","action":"command.executed","prompt_id":"p","command":{"command":"ls","exit_code":0},"source_event_id":"e1"}]}`
	if string(got) != want {
		t.Fatalf("batch JSON drifted:\n got %s\nwant %s", got, want)
	}

	var state ShareTraceStateV1
	if err := json.Unmarshal([]byte(`{"id":"k3x","external_id":"claude_code:abc","url":"https://beacon.sh/s/k3x","title":"t","visibility":"direct","event_count":4,"last_event_id":"e4","max_number":4,"created_at":"c","updated_at":"u"}`), &state); err != nil {
		t.Fatal(err)
	}
	if state != (ShareTraceStateV1{ID: "k3x", ExternalID: "claude_code:abc", URL: "https://beacon.sh/s/k3x", Title: "t", Visibility: "direct", EventCount: 4, LastEventID: "e4", MaxNumber: 4, CreatedAt: "c", UpdatedAt: "u"}) {
		t.Fatalf("state = %+v", state)
	}
	var put ShareTraceResultV1
	if err := json.Unmarshal([]byte(`{"id":"k3x","external_id":"claude_code:abc","url":"https://beacon.sh/s/k3x","visibility":"direct","event_count":0,"created":true}`), &put); err != nil {
		t.Fatal(err)
	}
	if put != (ShareTraceResultV1{ID: "k3x", ExternalID: "claude_code:abc", URL: "https://beacon.sh/s/k3x", Visibility: "direct", Created: true}) {
		t.Fatalf("put = %+v", put)
	}
	var result ShareBatchResultV1
	if err := json.Unmarshal([]byte(`{"upserted":2,"event_count":6}`), &result); err != nil || result != (ShareBatchResultV1{Upserted: 2, EventCount: 6}) {
		t.Fatalf("batch result = %+v, %v", result, err)
	}
}

// The wire types survive a JSON round trip unchanged, so what the CLI sends is what beacon.sh
// stores and serves back.
func TestShareWireTypesRoundTrip(t *testing.T) {
	events := shareFieldEvents()
	bundle := BuildShareBundle(events, shareTestOpts)
	for name, value := range map[string]interface{}{
		"trace":  NewShareTrace(bundle, ShareVisibilityPublic),
		"batch":  ShareEventsBatchV1{Events: bundle.Events},
		"bundle": bundle,
	} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		decoded := reflect.New(reflect.TypeOf(value))
		if err := json.Unmarshal(data, decoded.Interface()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		again, err := json.Marshal(decoded.Elem().Interface())
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(data) {
			t.Fatalf("%s changed in a round trip:\n%s\n%s", name, data, again)
		}
	}
}

func TestShareSummaryLeavesOutDetachedHeadAndTheHomeDirectoryName(t *testing.T) {
	event := shareEventAt("2026-10-01T10:00:00Z", "claude_code", CollectionMethodPoll, "prompt.submitted", "prompt")
	event.Session.WorkingDirectory = "/home/dev"
	event.Repository = "/home/dev"
	event.Branch = "HEAD"
	event.Prompt = &PromptInfo{Text: "hi"}
	if summary := BuildShareBundle([]Event{event}, shareTestOpts).Summary; summary.Repository != nil || summary.Project != nil {
		t.Fatalf("repository = %+v, project = %+v", summary.Repository, summary.Project)
	}
	event.Session.WorkingDirectory = "/home/dev/src/app"
	event.Repository = "/home/dev/src/app"
	summary := BuildShareBundle([]Event{event}, shareTestOpts).Summary
	if summary.Repository != nil {
		t.Fatalf("repository = %+v", summary.Repository)
	}
	if summary.Project == nil || summary.Project.Name != "app" {
		t.Fatalf("project = %+v", summary.Project)
	}
}

func TestPathScrubberReplacesWholePathsOnly(t *testing.T) {
	s := newPathScrubber("/src/proj", "/home/al")
	cases := map[string]string{
		"/src/proj":                     ".",
		"/src/proj/main.go":             "./main.go",
		"cd /src/proj && go test":       "cd . && go test",
		`"/src/proj/a.go"`:              `"./a.go"`,
		"/src/proj-old/main.go":         "/src/proj-old/main.go",
		"/src/project/main.go":          "/src/project/main.go",
		"/home/al/notes.txt":            "~/notes.txt",
		"/home/alex/notes.txt":          "/home/alex/notes.txt",
		"/srv/home/al/x":                "/srv/home/al/x",
		"open /home/al, then /home/al.": "open ~, then ~.",
		"/home/al/src/proj/x":           "~/src/proj/x",
		"file:///home/al/notes.txt":     "file://~/notes.txt",
		"file:///home/alex/notes.txt":   "file:///home/alex/notes.txt",
	}
	for in, want := range cases {
		if got := s.text(in); got != want {
			t.Errorf("text(%q) = %q, want %q", in, got, want)
		}
	}
	if got := s.path("/src/proj-old/main.go"); got != "/src/proj-old/main.go" {
		t.Errorf("path of a sibling directory = %q", got)
	}
	win := newPathScrubber(`C:\work\proj`, `C:\Users\al`)
	if got := win.text(`C:\work\proj\a.go and C:\Users\alex\b`); got != `.\a.go and C:\Users\alex\b` {
		t.Errorf("windows text = %q", got)
	}
}

// A directory name may end in a space. The work directory is that directory, not its trimmed
// sibling, so its files are shown relative to it and the project keeps its name.
func TestShareKeepsWhitespaceInTheWorkingDirectory(t *testing.T) {
	event := shareEventAt("2026-10-01T10:00:00Z", "claude_code", CollectionMethodHook, "file.modified", "file")
	event.Session.WorkingDirectory = "/src/repo "
	event.File = &FileInfo{Path: "/src/repo /x", Operation: "modify"}
	bundle := BuildShareBundle([]Event{event}, shareTestOpts)
	if len(bundle.Events) != 1 || bundle.Events[0].File == nil || bundle.Events[0].File.Path != "x" {
		t.Fatalf("events = %+v", bundle.Events)
	}
	if bundle.Summary.Project == nil || bundle.Summary.Project.Name != "repo " {
		t.Fatalf("project = %+v", bundle.Summary.Project)
	}
}

func TestShareMessagesKeepsRepeatedPhaselessCallsApart(t *testing.T) {
	// Cursor's beforeReadFile records file.read with a path and nothing else. Reading the same file
	// twice is two calls, not a call and its result.
	var events []Event
	for i, ts := range []string{"2026-10-01T10:00:00Z", "2026-10-01T10:00:01Z", "2026-10-01T10:00:02Z"} {
		read := shareEventAt(ts, "cursor", CollectionMethodHook, "file.read", "file")
		read.Event.ID = "r-" + string(rune('a'+i))
		read.File = &FileInfo{Path: "/home/dev/src/app/main.go", Operation: "read"}
		events = append(events, read)
	}
	messages := shareMessages(events, shareTestOpts)
	want := "assistant/tool_call,assistant/tool_call,assistant/tool_call"
	if got := partsOf(t, messages); strings.Join(got, ",") != want {
		t.Fatalf("parts = %v", got)
	}
}

// A session with no working directory (an OTLP-only or browser session) still has its home
// directory scrubbed from everything that leaves the machine: the PUT body's title and the event
// batch. The home path names the local account. Replaces TestShareTitleUsesExplicitHomeWithoutLookup
// (d816303f), whose only coverage of this case went with it.
func TestSharedTraceScrubsHomeWithoutAWorkingDirectory(t *testing.T) {
	prompt := shareEventAt("2026-10-01T10:00:00Z", "claude_code", CollectionMethodOTLP, "prompt.submitted", "prompt")
	prompt.Session.WorkingDirectory = ""
	prompt.Prompt = &PromptInfo{Text: "look at /home/al/secret"}
	command := shareEventAt("2026-10-01T10:00:01Z", "claude_code", CollectionMethodOTLP, "command.executed", "command")
	command.Session.WorkingDirectory = ""
	command.Command = &CommandInfo{Command: "cat /home/al/secret/notes.txt", Output: "see /home/al/.aws"}

	bundle := BuildShareBundle([]Event{prompt, command}, ShareOptions{HomeDirectory: "/home/al", NoHomeLookup: true})
	if len(bundle.Events) != 2 {
		t.Fatalf("bundle carries %d events, want 2", len(bundle.Events))
	}
	trace := NewShareTrace(bundle, ShareVisibilityDirect)
	if trace.Title != "look at ~/secret" {
		t.Fatalf("title = %q, want the home directory scrubbed", trace.Title)
	}
	for name, body := range map[string]interface{}{
		"PUT /v1/traces":               trace,
		"POST /v1/traces/events/batch": ShareEventsBatchV1{Events: bundle.Events},
	} {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		if strings.Contains(string(data), "/home/al") {
			t.Errorf("%s body leaks the home directory: %s", name, data)
		}
	}
}

func TestShareMessagesTreatsAPostToolInvokedEventAsTheResult(t *testing.T) {
	// The hook adapter labels a PostToolUse for tools without a more specific action (WebFetch,
	// Grep, Task, ...) tool.invoked, carrying the result. With a PreToolUse tool.invoked for the
	// same call (Kimi Code, Muse Code) it must still produce a result.
	pre := shareEventAt("2026-10-01T10:00:00Z", "kimi_code", CollectionMethodHook, "tool.invoked", "tool")
	pre.Event.ID = "k-pre"
	pre = withCall(pre, "call_1", "WebFetch", map[string]interface{}{"url": "https://example.com"}, nil)
	post := shareEventAt("2026-10-01T10:00:01Z", "kimi_code", CollectionMethodHook, "tool.invoked", "tool")
	post.Event.ID = "k-post"
	post = withCall(post, "call_1", "WebFetch", nil, "page body")
	// Alone (Claude: PreToolUse becomes an approval), the post event still yields a call and result.
	lone := shareEventAt("2026-10-01T10:00:02Z", "claude_code", CollectionMethodHook, "tool.invoked", "tool")
	lone.Event.ID = "c-post"
	lone = withCall(lone, "toolu_9", "Grep", map[string]interface{}{"pattern": "x"}, "3 matches")

	messages := shareMessages([]Event{pre, post, lone}, shareTestOpts)
	want := "assistant/tool_call,assistant/tool_result,assistant/tool_call,assistant/tool_result"
	if got := partsOf(t, messages); strings.Join(got, ",") != want {
		t.Fatalf("parts = %v", got)
	}
	result := messages[1].Parts[0].Content.(*TraceToolResultContentV1)
	if result.CallID != "call_1" || !strings.Contains(fmt.Sprint(result.Output), "page body") {
		t.Fatalf("result = %+v", result)
	}
}

func TestShareMessagesKeepsCursorsToolOutputWriteApartFromItsWebSearch(t *testing.T) {
	// Cursor records the file it saves a WebSearch's output to as a Write carrying the WebSearch's
	// tool_use_id, ahead of the WebSearch's own postToolUse. The two are different tools: the Write
	// must not swallow the WebSearch and its result. The Stop hook's payloadless tool.completed is
	// the end of the turn, not a tool.
	const id = "call_Ox7\nfc_0f44"
	write := shareEventAt("2026-10-11T19:21:20Z", "cursor", CollectionMethodHook, "file.modified", "file")
	write.Event.ID = "cursor-write"
	write = withCall(write, id, "Write", nil, nil)
	write.File = &FileInfo{Path: "/home/dev/.cursor/projects/app/agent-tools/82a0.txt", Operation: "create"}
	search := shareEventAt("2026-10-11T19:21:21Z", "cursor", CollectionMethodHook, "tool.invoked", "tool")
	search.Event.ID = "cursor-search"
	search = withCall(search, id, "WebSearch", map[string]interface{}{"search_term": "weather NYC"}, map[string]interface{}{"results": "Sunny, 68F"})
	stop := shareEventAt("2026-10-11T19:21:25Z", "cursor", CollectionMethodHook, "tool.completed", "tool")
	stop.Event.ID = "cursor-stop"

	messages := shareMessages([]Event{write, search, stop}, shareTestOpts)
	want := "assistant/tool_call,assistant/tool_call,assistant/tool_result"
	if got := partsOf(t, messages); strings.Join(got, ",") != want {
		t.Fatalf("parts = %v, want %s", got, want)
	}
	writeCall := messages[0].Parts[0].Content.(*TraceToolCallContentV1)
	searchCall := messages[1].Parts[0].Content.(*TraceToolCallContentV1)
	result := messages[2].Parts[0].Content.(*TraceToolResultContentV1)
	if writeCall.ToolName != "Write" || searchCall.ToolName != "WebSearch" {
		t.Fatalf("calls = %q, %q; want Write, WebSearch", writeCall.ToolName, searchCall.ToolName)
	}
	if searchCall.CallID == writeCall.CallID || result.CallID != searchCall.CallID {
		t.Fatalf("call ids: write %q, search %q, result %q", writeCall.CallID, searchCall.CallID, result.CallID)
	}
	if !strings.Contains(result.Output, "Sunny, 68F") {
		t.Fatalf("result output = %q", result.Output)
	}
}

func TestShareMessagesWritesAPromptCapturedTwiceOnce(t *testing.T) {
	// Claude Code logs each prompt from the UserPromptSubmit hook and from the OTLP export, with
	// different event ids and the same prompt.id. The export copy may carry no text.
	otlp := shareEventAt("2026-10-01T10:00:00Z", "claude_code", CollectionMethodOTLP, "prompt.submitted", "prompt")
	otlp.Event.ID = "p-otlp"
	otlp.Prompt = &PromptInfo{ID: "prompt-1"}
	hook := shareEventAt("2026-10-01T10:00:00.5Z", "claude_code", CollectionMethodHook, "prompt.submitted", "prompt")
	hook.Event.ID = "p-hook"
	hook.Prompt = &PromptInfo{Text: "fix the test"} // the UserPromptSubmit hook carries no prompt.id
	next := shareEventAt("2026-10-01T10:01:00Z", "claude_code", CollectionMethodHook, "prompt.submitted", "prompt")
	next.Event.ID = "p-next"
	next.Prompt = &PromptInfo{ID: "prompt-2", Text: "fix the test"}

	messages := shareMessages([]Event{otlp, hook, next}, shareTestOpts)
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2: %+v", len(messages), messages)
	}
	if messages[0].Text != "fix the test" || messages[0].ID != "p-otlp" {
		t.Fatalf("first prompt = %+v", messages[0])
	}
}

func TestShareMessagesPairsPromptCapturesOnlyAcrossPaths(t *testing.T) {
	prompt := func(id, ts, method, promptID, text string) Event {
		e := shareEventAt(ts, "claude_code", method, "prompt.submitted", "prompt")
		e.Event.ID = id
		e.Prompt = &PromptInfo{ID: promptID, Text: text}
		return e
	}
	// Hook first, export second: one message.
	got := shareMessages([]Event{
		prompt("h1", "2026-10-01T10:00:00Z", CollectionMethodHook, "", "continue"),
		prompt("o1", "2026-10-01T10:00:01Z", CollectionMethodOTLP, "p-1", ""),
	}, shareTestOpts)
	if len(got) != 1 || got[0].Text != "continue" {
		t.Fatalf("hook then export = %+v", got)
	}
	// Two hook prompts with the same text moments apart are two submissions.
	got = shareMessages([]Event{
		prompt("h1", "2026-10-01T10:00:00Z", CollectionMethodHook, "", "continue"),
		prompt("h2", "2026-10-01T10:00:03Z", CollectionMethodHook, "", "continue"),
	}, shareTestOpts)
	if len(got) != 2 {
		t.Fatalf("two hook prompts = %+v", got)
	}
	// A keyless copy far from the keyed one, or with different text, stays separate.
	got = shareMessages([]Event{
		prompt("o1", "2026-10-01T10:00:00Z", CollectionMethodOTLP, "p-1", "first"),
		prompt("h1", "2026-10-01T10:05:00Z", CollectionMethodHook, "", "first"),
		prompt("o2", "2026-10-01T10:06:00Z", CollectionMethodOTLP, "p-2", "second"),
		prompt("h2", "2026-10-01T10:06:01Z", CollectionMethodHook, "", "other"),
	}, shareTestOpts)
	if len(got) != 4 {
		t.Fatalf("unrelated prompts merged: %+v", got)
	}
}

// shareFieldEvents is one session whose events put the sharer's home directory in every kind of
// field the trace projection reads.
func shareFieldEvents() []Event {
	prompt := shareEventAt("2026-10-01T10:00:00Z", "claude_code", CollectionMethodHook, "prompt.submitted", "prompt")
	prompt.Event.ID = "f-prompt"
	prompt.Prompt = &PromptInfo{ID: "p1", Text: "read /home/dev/notes/todo.md"}
	prompt.Repository = "/home/dev/src/app"

	cmd := shareEventAt("2026-10-01T10:00:01Z", "claude_code", CollectionMethodHook, "command.executed", "command")
	cmd.Event.ID = "f-cmd"
	cmd = withCall(cmd, "toolu_c", "Bash", map[string]interface{}{"command": "cat /home/dev/.config/app.toml"}, nil)
	cmd.Tool.Command = "cat /home/dev/.config/app.toml"
	cmd.Command = &CommandInfo{Command: "cat /home/dev/.config/app.toml", ExitCode: intPtr(1), Output: "cat: /home/dev/.config/app.toml: No such file"}
	cmd.Message = "ran cat /home/dev/.config/app.toml"

	edit := shareEventAt("2026-10-01T10:00:02Z", "claude_code", CollectionMethodHook, "file.modified", "file")
	edit.Event.ID = "f-edit"
	edit = withCall(edit, "toolu_e", "Edit", map[string]interface{}{"file_path": "/home/dev/src/app/main.go", "nested": []interface{}{map[string]interface{}{"p": "/home/dev/x"}}}, map[string]interface{}{"path": "/home/dev/src/app/main.go"})
	edit.Tool.Path = "/home/dev/src/app/main.go"
	edit.File = &FileInfo{Path: "/home/dev/src/app/main.go", Operation: "modify", Diff: "--- /home/dev/src/app/main.go\n+++ /home/dev/src/app/main.go\n"}

	mcp := shareEventAt("2026-10-01T10:00:03Z", "claude_code", CollectionMethodHook, "mcp.tool_invoked", "mcp")
	mcp.Event.ID = "f-mcp"
	mcp = withCall(mcp, "toolu_m", "mcp__fs__read", map[string]interface{}{"path": "/home/dev/secret.txt"}, []interface{}{map[string]interface{}{"type": "text", "text": "from /home/dev/secret.txt"}})
	mcp.MCP = &MCPInfo{Server: "fs", Tool: "read", Resource: &MCPResourceInfo{URI: "file:///home/dev/secret.txt"}}

	approval := shareEventAt("2026-10-01T10:00:04Z", "claude_code", CollectionMethodHook, "approval.denied", "approval")
	approval.Event.ID = "f-approval"
	approval.Approval = &ApprovalInfo{Required: true, Decision: "deny", Reason: "writes to /home/dev/.ssh"}
	approval.Policy = &PolicyInfo{Name: "home /home/dev guard", Decision: "deny", Reason: "path /home/dev/.ssh is protected"}

	failure := shareEventAt("2026-10-01T10:00:05Z", "claude_code", CollectionMethodHook, "session.error", "session")
	failure.Event.ID = "f-error"
	failure.Message = "open /home/dev/src/app/go.mod: permission denied"
	failure.Error = &ErrorInfo{Type: "/home/dev/bin/tool exited"}

	answer := shareEventAt("2026-10-01T10:00:06Z", "claude_code", CollectionMethodHook, "agent.message", "agent")
	answer.Event.ID = "f-answer"
	answer.GenAI = &GenAIInfo{Output: &GenAIOutputInfo{Messages: TextOutputMessages("I changed /home/dev/src/app/main.go")}}
	answer.Raw = map[string]interface{}{"cwd": "/home/dev/src/app"}

	return []Event{prompt, cmd, edit, mcp, approval, failure, answer}
}

// Every field the shared bundle carries is scrubbed of the sharer's home and session directory:
// command lines and output, file paths and diffs, tool arguments and results, MCP fields, approval
// and policy text, errors, retained text, the working directory and the repository.
func TestShareBundleScrubsEveryFieldType(t *testing.T) {
	events := shareFieldEvents()
	bundle := BuildShareBundle(events, shareTestOpts)
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/home/dev") {
		t.Fatalf("a local path reached the bundle: %s", data)
	}
	messages, err := json.Marshal(TraceMessages(bundle))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(messages), "/home/dev") {
		t.Fatalf("a local path reached the messages: %s", messages)
	}
	byID := map[string]TraceEventV1{}
	for _, event := range bundle.Events {
		byID[event.ID] = event
	}
	checks := map[string]string{
		"prompt":          byID["f-prompt"].Content.Text,
		"command":         byID["f-cmd"].Command.Command,
		"command output":  byID["f-cmd"].Command.Output.Text,
		"tool command":    byID["f-cmd"].Tool.Command,
		"tool argument":   fmt.Sprint(byID["f-cmd"].Tool.Arguments),
		"message":         byID["f-cmd"].Summary,
		"file path":       byID["f-edit"].File.Path,
		"file title":      byID["f-edit"].Title,
		"tool path":       byID["f-edit"].Tool.Path,
		"diff":            byID["f-edit"].File.Diff.Text,
		"nested argument": fmt.Sprint(byID["f-edit"].Tool.Arguments),
		"tool result":     fmt.Sprint(byID["f-edit"].Tool.Result),
		"mcp argument":    fmt.Sprint(byID["f-mcp"].Tool.Arguments),
		"mcp result":      fmt.Sprint(byID["f-mcp"].Tool.Result),
		"mcp resource":    byID["f-mcp"].MCP.ResourceURI,
		"approval reason": byID["f-approval"].Approval.Reason,
		"policy name":     byID["f-approval"].Policy.Name,
		"policy reason":   byID["f-approval"].Policy.Reason,
		"error message":   byID["f-error"].Summary,
		"error type":      byID["f-error"].Error.Type,
		"assistant text":  byID["f-answer"].Content.Text,
	}
	want := map[string]string{
		"prompt":          "read ~/notes/todo.md",
		"command":         "cat ~/.config/app.toml",
		"command output":  "cat: ~/.config/app.toml: No such file",
		"file path":       "main.go",
		"tool path":       "main.go",
		"mcp resource":    "file://~/secret.txt",
		"error message":   "open ./go.mod: permission denied",
		"approval reason": "writes to ~/.ssh",
		"assistant text":  "I changed ./main.go",
	}
	for name, got := range checks {
		if strings.Contains(got, "/home/dev") || got == "" {
			t.Errorf("%s = %q", name, got)
		}
		if w, ok := want[name]; ok && got != w {
			t.Errorf("%s = %q, want %q", name, got, w)
		}
	}
	if repo := bundle.Summary.Repository; repo != nil && repo.Path != "" {
		t.Errorf("repository = %+v", repo)
	}
	if project := bundle.Summary.Project; project == nil || project.Name != "app" {
		t.Errorf("project = %+v", project)
	}
	// The caller's events are not rewritten.
	if events[1].Command.Command != "cat /home/dev/.config/app.toml" || events[2].File.Path != "/home/dev/src/app/main.go" ||
		events[3].GenAI.Tool.Call.Arguments.(map[string]interface{})["path"] != "/home/dev/secret.txt" {
		t.Fatal("BuildShareBundle changed the caller's events")
	}
}

func TestShareProjects(t *testing.T) {
	event := func(action, category string) Event {
		return shareEventAt("2026-10-01T10:00:00Z", "claude_code", CollectionMethodHook, action, category)
	}
	usage := event("token.usage", "metric")
	usage.GenAI = &GenAIInfo{Usage: &GenAIUsageInfo{InputTokens: int64Ptr(3)}}
	metricWithoutUsage := event("", "metric")
	metricWithoutUsage.Raw = map[string]interface{}{"metric_name": "claude_code.lines_of_code.count"}
	cases := []struct {
		name  string
		event Event
		want  bool
	}{
		{"prompt", event("prompt.submitted", "prompt"), true},
		{"tool", event("tool.invoked", "tool"), true},
		{"command", event("command.executed", ""), true},
		{"session start", event("session.started", "session"), true},
		{"handoff", event("session.handoff", "session"), true},
		{"reasoning", event("agent.reasoning", "agent"), true},
		{"token usage", usage, true},
		{"metric without usage", metricWithoutUsage, false},
		{"shared record", event("session.shared", "session"), false},
		{"share request", event("session.share_requested", "session"), false},
		{"commit link", event("session.commit_linked", "session"), false},
		{"heartbeat", event("endpoint.heartbeat", "health"), false},
		{"inventory", event("inventory.snapshot", "inventory"), false},
		{"idle", event("session.idle", "session"), false},
		{"status", event("session.status", "session"), false},
		{"token action without usage", event("token.usage", "model"), false},
		{"tool listing", event("mcp.tool_listed", "mcp"), false},
		{"no action", event("", ""), false},
	}
	for _, tc := range cases {
		if got := ShareProjects(tc.event); got != tc.want {
			t.Errorf("ShareProjects(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
	// The bundle carries exactly what ShareProjects accepts, even when a rejected event holds content
	// that would otherwise be shared: local views rely on this to tell a shared trace is out of date.
	var events []Event
	accepted := 0
	for i, tc := range cases {
		e := tc.event
		e.Event.ID = fmt.Sprintf("e-%02d", i)
		if tc.want {
			accepted++
		} else {
			e.Message = "something happened"
			e.Prompt = &PromptInfo{Text: "a prompt-shaped field"}
			e.GenAI = &GenAIInfo{Output: &GenAIOutputInfo{Messages: TextOutputMessages("assistant-shaped text")}}
		}
		events = append(events, e)
	}
	if bundle := BuildShareBundle(events, shareTestOpts); len(bundle.Events) != accepted || bundle.Summary.EventCount != accepted {
		t.Fatalf("bundle carries %d events (summary %d), ShareProjects accepts %d", len(bundle.Events), bundle.Summary.EventCount, accepted)
	}
}

func TestShareBundleOrdersNumbersAndDeduplicates(t *testing.T) {
	pre := shareEventAt("2026-10-01T10:00:01Z", "claude_code", CollectionMethodHook, "tool.invoked", "tool")
	pre.Event.ID = "z-pre"
	pre = withCall(pre, "toolu_1", "Bash", map[string]interface{}{"command": "ls"}, nil)
	post := shareEventAt("2026-10-01T10:00:01Z", "claude_code", CollectionMethodHook, "command.executed", "command")
	post.Event.ID = "a-post"
	post = withCall(post, "toolu_1", "Bash", nil, nil)
	post.Command = &CommandInfo{Command: "ls", ExitCode: intPtr(0)}
	prompt := shareEventAt("2026-10-01T10:00:00Z", "claude_code", CollectionMethodHook, "prompt.submitted", "prompt")
	prompt.Prompt = &PromptInfo{Text: "list"}
	untimed := shareEventAt("not a time", "claude_code", CollectionMethodHook, "session.ended", "session")
	untimed.Event.ID = "u"

	bundle := BuildShareBundle([]Event{untimed, post, pre, prompt, post}, shareTestOpts)
	var got []string
	for i, event := range bundle.Events {
		if event.Number != i+1 {
			t.Fatalf("event %d is numbered %d", i, event.Number)
		}
		got = append(got, event.ID)
	}
	// The invocation sorts ahead of its completion at one instant, the duplicate is carried once,
	// and the untimed event comes last.
	if want := ShareEventID(prompt) + ",z-pre,a-post,u"; strings.Join(got, ",") != want {
		t.Fatalf("events = %v, want %s", got, want)
	}
	if bundle.Spans != nil && len(bundle.Spans) != 0 {
		t.Fatalf("spans = %+v", bundle.Spans)
	}
	if again := BuildShareBundle([]Event{untimed, post, pre, prompt, post}, ShareOptions{HomeDirectory: "/home/dev", NoHomeLookup: true, GeneratedAt: time.Unix(0, 0)}); again.Events[0].ID != got[0] {
		t.Fatal("derived event id is not stable")
	}
	if empty := BuildShareBundle(nil, shareTestOpts); empty.Events == nil || len(empty.Events) != 0 || empty.Summary.EventCount != 0 {
		t.Fatalf("empty bundle = %+v", empty)
	}
}

// TraceMessages reads any bundle, including the local dashboard's, which still holds bookkeeping
// and status events: they are never messages.
func TestTraceMessagesSkipsBookkeepingInALocalBundle(t *testing.T) {
	builder := NewTraceBuilder("session:claude_code:sess-1", Event{})
	for i, e := range []Event{
		shareEventAt("2026-10-01T10:00:00Z", "claude_code", "", "session.shared", "session"),
		shareEventAt("2026-10-01T10:00:01Z", "claude_code", "", "session.share_requested", "session"),
		shareEventAt("2026-10-01T10:00:02Z", "claude_code", "", "session.commit_linked", "session"),
		shareEventAt("2026-10-01T10:00:03Z", "claude_code", CollectionMethodOTLP, "token.usage", "metric"),
		shareEventAt("2026-10-01T10:00:04Z", "claude_code", CollectionMethodHook, "session.started", "session"),
	} {
		builder.Add(e, TraceEventFromEvent(e, fmt.Sprintf("line-%d", i), 0))
	}
	messages := TraceMessages(builder.Bundle(TraceProvenanceV1{}))
	if got := partsOf(t, messages); strings.Join(got, ",") != "system/system_event" || messages[0].Kind != TraceMessageKindSystem {
		t.Fatalf("parts = %v", got)
	}
}

func TestTraceMessageRoundTrip(t *testing.T) {
	messages := TraceMessages(BuildShareBundle(shareFieldEvents(), shareTestOpts))
	kinds := map[string]bool{}
	for _, m := range messages {
		kinds[m.Kind] = true
	}
	for _, kind := range []string{TraceMessageKindPrompt, TraceMessageKindToolCall, TraceMessageKindToolResult, TraceMessageKindApproval, TraceMessageKindError, TraceMessageKindAssistant} {
		if !kinds[kind] {
			t.Fatalf("no %s message in %+v", kind, messages)
		}
	}
	data, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []TraceMessageV1
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Fatalf("messages changed in a round trip:\n%s\n%s", data, again)
	}
	if _, ok := decoded[1].Parts[0].Content.(*TraceToolCallContentV1); !ok {
		t.Fatalf("tool call decoded as %T", decoded[1].Parts[0].Content)
	}
	want := `{"id":"m1","role":"assistant","kind":"tool_call","order":1,"timestamp":"t","model":"m","text":"x","parts":[` +
		`{"type":"tool_call","order":1,"content":{"callId":"c","toolName":"Bash","toolKind":"terminal_command","args":{},"command":"ls","filePath":"a","diff":"d"}},` +
		`{"type":"tool_result","order":2,"content":{"callId":"c","toolName":"Bash","output":"o","status":"success","exitCode":0,"durationMs":3}},` +
		`{"type":"text","order":3,"content":{"text":"x"}},` +
		`{"type":"error","order":4,"content":{"message":"e"}},` +
		`{"type":"system_event","order":5,"content":{"subtype":"session.started"}}],` +
		`"token_usage":{"input_tokens":1}}`
	pinned := TraceMessageV1{ID: "m1", Role: TraceRoleAssistant, Kind: TraceMessageKindToolCall, Order: 1, Timestamp: "t", Model: "m", Text: "x",
		Parts: []TraceMessagePartV1{
			{Type: TracePartToolCall, Order: 1, Content: &TraceToolCallContentV1{CallID: "c", ToolName: "Bash", ToolKind: TraceToolKindTerminalCommand, Args: map[string]interface{}{}, Command: "ls", FilePath: "a", Diff: "d"}},
			{Type: TracePartToolResult, Order: 2, Content: &TraceToolResultContentV1{CallID: "c", ToolName: "Bash", Output: "o", Status: TraceToolStatusSuccess, ExitCode: intPtr(0), DurationMS: int64Ptr(3)}},
			{Type: TracePartText, Order: 3, Content: TraceTextContentV1{Text: "x"}},
			{Type: TracePartError, Order: 4, Content: TraceErrorContentV1{Message: "e"}},
			{Type: TracePartSystemEvent, Order: 5, Content: map[string]interface{}{"subtype": "session.started"}},
		},
		TokenUsage: &TraceUsageV1{InputTokens: 1},
	}
	if got, _ := json.Marshal(pinned); string(got) != want {
		t.Fatalf("message JSON drifted:\n got %s\nwant %s", got, want)
	}
}

func TestShareEventIDAndBundleLeaveTheCallersTraceAlone(t *testing.T) {
	event := shareEventAt("2026-10-01T10:00:00Z", "claude_code", CollectionMethodHook, "tool.invoked", "tool")
	event.Event.ID = ""
	event.Trace = &TraceInfo{}
	event.Tool = &ToolInfo{Name: "Bash"}
	event.GenAI = &GenAIInfo{Tool: &GenAIToolInfo{Name: "Bash", Call: &GenAIToolCallInfo{ID: "toolu_1"}}}
	events := []Event{event}

	id := ShareEventID(events[0])
	if id == "" {
		t.Fatal("no id")
	}
	bundle := BuildShareBundle(events, shareTestOpts)
	if got := events[0].Trace; got == nil || got.ID != "" || got.SpanID != "" {
		t.Fatalf("caller's trace = %+v", got)
	}
	if len(bundle.Events) != 1 || bundle.Events[0].ID != id {
		t.Fatalf("events = %+v", bundle.Events)
	}
	if tr := bundle.Events[0].Trace; tr != nil && (tr.ID != "" || tr.SpanID != "") {
		t.Fatalf("bundle event trace = %+v", tr)
	}
	if len(bundle.Spans) != 0 {
		t.Fatalf("spans = %+v", bundle.Spans)
	}
}

func TestShareRelationComesFromTheHandoff(t *testing.T) {
	plain := shareEventAt("2026-10-01T10:00:00Z", "claude_code", CollectionMethodHook, "prompt.submitted", "prompt")
	if related, kind := ShareRelation([]Event{plain}); related != "" || kind != "" {
		t.Fatalf("relation = %q %q", related, kind)
	}
	handoff := shareEventAt("2026-10-01T10:00:01Z", "claude_code", CollectionMethodHook, "session.handoff", "session")
	handoff.Handoff = &HandoffInfo{SourceHarness: "codex", SourceSessionID: "sess-9"}
	related, kind := ShareRelation([]Event{plain, handoff})
	if related != ShareExternalID("codex", "sess-9") || kind != ShareRelationshipHandoff {
		t.Fatalf("relation = %q %q", related, kind)
	}
	trace := ShareTraceV1{RelatedTo: related, RelationshipType: kind}
	data, _ := json.Marshal(trace)
	if !strings.Contains(string(data), `"related_to":"codex_cli:sess-9","relationship_type":"handoff"`) {
		t.Fatalf("wire = %s", data)
	}
}

func TestTraceBuilderNamesTheProject(t *testing.T) {
	event := shareEventAt("2026-10-01T10:00:00Z", "claude_code", CollectionMethodHook, "prompt.submitted", "prompt")
	event.Session.WorkingDirectory = "/home/dev/src/app/sub"
	b := NewTraceBuilder("t", event)
	b.Add(event, TraceEventFromEvent(event, "e1", 0))
	if p := b.Summary().Project; p == nil || p.Name != "sub" {
		t.Fatalf("project = %+v", p)
	}
	event.Repository = "/home/dev/src/app"
	b = NewTraceBuilder("t", event)
	b.Add(event, TraceEventFromEvent(event, "e1", 0))
	if p := b.Summary().Project; p == nil || p.Name != "app" {
		t.Fatalf("project = %+v", p)
	}
}

func TestShareEventsAfterResendsTheLastEventAndOpenCalls(t *testing.T) {
	ev := func(id, action, callID string) TraceEventV1 {
		return TraceEventV1{ID: id, Action: action, ToolCallID: callID}
	}
	events := []TraceEventV1{
		ev("p1", "prompt.submitted", ""),
		ev("c1", "tool.invoked", "a"),
		ev("r1", "tool.completed", "a"),
		ev("c2", "tool.invoked", "b"),
		ev("m1", "agent.response", ""),
		ev("r2", "tool.completed", "b"),
		ev("m2", "agent.response", ""),
	}
	ids := func(in []TraceEventV1) string {
		var out []string
		for _, e := range in {
			out = append(out, e.ID)
		}
		return strings.Join(out, ",")
	}
	cases := []struct {
		last string
		max  int
		want string
	}{
		{"r1", 0, "r1,c2,m1,r2,m2"},
		{"m1", 0, "c2,m1,r2,m2"},
		{"r2", 0, "r2,m2"},
		{"m2", 0, "m2"},
		{"gone", 0, "p1,c1,r1,c2,m1,r2,m2"},
		{"", 3, "r1,c2,m1,r2,m2"},
		{"", 0, "p1,c1,r1,c2,m1,r2,m2"},
		{"", 99, "p1,c1,r1,c2,m1,r2,m2"},
	}
	for _, tc := range cases {
		if got := ids(ShareEventsAfter(events, tc.last, tc.max)); got != tc.want {
			t.Errorf("after %q/%d = %s, want %s", tc.last, tc.max, got, tc.want)
		}
	}
}

func TestShareRefreshCommand(t *testing.T) {
	if got := ShareRefreshCommand("claude", "abc-123"); got != "beacon share refresh claude_code:abc-123" {
		t.Fatalf("plain = %q", got)
	}
	if got := ShareRefreshCommand("claude_code", "has space"); got != "" {
		t.Fatalf("unsafe id = %q", got)
	}
	if got := ShareRefreshCommand("", "x"); got != "" {
		t.Fatalf("no harness = %q", got)
	}
}
