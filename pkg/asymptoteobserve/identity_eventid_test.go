package asymptoteobserve_test

import (
	"encoding/json"
	"math/rand/v2"
	"path/filepath"
	"testing"

	obs "github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

// lineID is the identity a writer stamps: EventIDForLine over the event serialized without one.
func lineID(t testing.TB, e obs.Event) string {
	t.Helper()
	e.Event.ID = ""
	line, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return obs.EventIDForLine(line)
}

// spellings are values the serialized form normalizes or the target canonicalizes: surrounding
// space, case, MCP prefixes and separators, invalid UTF-8, characters JSON escapes.
var spellings = []string{
	"", " ", "Read", "Bash", "mcp__notes__get", " MCP__Srv__Tool ", "mcp:srv", "a__b",
	"/tmp/x__y", "/Home/Dev/.aws/credentials", "rm -rf /", "claude\xff\xfe-x", "s\xc3", "\xe2\x80",
	"\u2028line", "<&>", "é", "\t tab", "<nil>",
}

var actions = []string{
	"mcp.tool_invoked", "command.executed", "file.read", "file.modified", "tool.completed",
	"tool.invoked", "tool.failed", "prompt.submitted", "session.context", " File.Read ", "TOOL.INVOKED",
	"mcp.tool_listed",
}

// variants adds spellings of e that exercise both identities: the action padded, and a tool call
// id with values the round trip changes in every field a target reads.
func variants(e obs.Event) []obs.Event {
	out := []obs.Event{e}
	padded := e
	padded.Event.Action = "  " + e.Event.Action + " "
	out = append(out, padded)
	withCall := e
	genAI := obs.GenAIInfo{}
	if e.GenAI != nil {
		genAI = *e.GenAI
	}
	tool := obs.GenAIToolInfo{Name: "Read"}
	if genAI.Tool != nil {
		tool = *genAI.Tool
	}
	tool.Call = &obs.GenAIToolCallInfo{ID: " toolu_01 "}
	genAI.Tool = &tool
	withCall.GenAI = &genAI
	withCall.Model = "claude\xff\xfe-x"
	withCall.Message = " MCP__srv__Tool "
	withCall.Session = &obs.SessionInfo{ID: "s\xc3"}
	out = append(out, withCall)
	return out
}

// EventID must name every event as the writers do, or one action is named twice: every rule
// fixture event, and spellings of each, through both.
func TestEventIDMatchesEventIDForLine(t *testing.T) {
	rules, err := threatrules.LoadDir(filepath.Join("..", "..", "rules"))
	if err != nil {
		t.Fatal(err)
	}
	n, calls := 0, 0
	for _, r := range rules {
		for _, fx := range r.Tests {
			for _, e := range fx.EventList() {
				for _, v := range variants(e) {
					v.Event.ID = "preset-id-is-ignored"
					if got, want := obs.EventID(v), lineID(t, v); got != want {
						t.Errorf("%s %q: EventID %s, EventIDForLine %s", r.ID, v.Event.Action, got, want)
					}
					if obs.ToolCallID(&v) != "" {
						calls++
					}
					n++
				}
			}
		}
	}
	if n < 1000 || calls < 300 {
		t.Fatalf("only %d events (%d with a call id) checked", n, calls)
	}
}

func randomEvent(r *rand.Rand) obs.Event {
	pick := func(s []string) string { return s[r.IntN(len(s))] }
	e := obs.Event{
		Timestamp: "2026-11-09T10:00:00.000000000Z", Vendor: obs.Vendor, Product: obs.Product, SchemaVersion: obs.SchemaVersion,
		Event:    obs.EventInfo{Kind: "agent_runtime", Action: pick(actions)},
		Severity: obs.SeverityInfo, Harness: obs.HarnessInfo{Name: "claude_code"},
		Model: pick(spellings), Message: pick(spellings),
	}
	if r.IntN(4) > 0 {
		e.Session = &obs.SessionInfo{ID: pick(spellings)}
	}
	if r.IntN(2) == 0 {
		e.MCP = &obs.MCPInfo{Server: pick(spellings), Tool: pick(spellings)}
	}
	if r.IntN(2) == 0 {
		e.Tool = &obs.ToolInfo{Name: pick(spellings), Command: pick(spellings), Path: pick(spellings)}
	}
	if r.IntN(2) == 0 {
		e.Command = &obs.CommandInfo{Command: pick(spellings)}
	}
	if r.IntN(2) == 0 {
		e.File = &obs.FileInfo{Path: pick(spellings)}
	}
	if r.IntN(4) > 0 {
		e.GenAI = &obs.GenAIInfo{Tool: &obs.GenAIToolInfo{Name: pick(spellings), Call: &obs.GenAIToolCallInfo{ID: pick(spellings), Result: pick(spellings)}}}
	}
	return e
}

func TestEventIDMatchesEventIDForLineOnRandomEvents(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	for range 20000 {
		e := randomEvent(r)
		if got, want := obs.EventID(e), lineID(t, e); got != want {
			b, _ := json.Marshal(e)
			t.Fatalf("EventID %s, EventIDForLine %s\n%s", got, want, b)
		}
	}
}

func FuzzEventID(f *testing.F) {
	f.Add("file.read", "toolu_1", "s1", "", "", "Read", "", "", "/etc/passwd", "", "")
	f.Add("mcp.tool_invoked", " c ", "s\xc3", "srv", "", "mcp__srv__get", "", "", "", "m\xff", " MCP__x ")
	f.Add("command.executed", "c", "", "", "", "", "a__b", "rm -rf /", "", "", "")
	f.Fuzz(func(t *testing.T, action, callID, session, server, mcpTool, toolName, toolCommand, command, path, model, message string) {
		e := obs.Event{
			Timestamp: "2026-11-09T10:00:00.000000000Z", Vendor: obs.Vendor, Product: obs.Product, SchemaVersion: obs.SchemaVersion,
			Event:    obs.EventInfo{Kind: "agent_runtime", Action: action},
			Severity: obs.SeverityInfo, Harness: obs.HarnessInfo{Name: "x"},
			Session:  &obs.SessionInfo{ID: session},
			MCP:      &obs.MCPInfo{Server: server, Tool: mcpTool},
			Tool:     &obs.ToolInfo{Name: toolName, Command: toolCommand, Path: path},
			Command:  &obs.CommandInfo{Command: command},
			File:     &obs.FileInfo{Path: path},
			GenAI:    &obs.GenAIInfo{Tool: &obs.GenAIToolInfo{Call: &obs.GenAIToolCallInfo{ID: callID}}},
			Model:    model, Message: message,
		}
		if got, want := obs.EventID(e), lineID(t, e); got != want {
			t.Fatalf("EventID %s, EventIDForLine %s", got, want)
		}
	})
}

func eventIDBenchEvents() map[string]obs.Event {
	result := "[default]\naws_access_key_id = <redacted>\n" + string(make([]byte, 3000))
	base := obs.Event{
		Timestamp: "2026-11-09T10:00:00.000000000Z", Vendor: obs.Vendor, Product: obs.Product, SchemaVersion: obs.SchemaVersion,
		Severity: obs.SeverityInfo, Endpoint: obs.EndpointInfo{Hostname: "gw-1", OS: "linux"},
		Harness: obs.HarnessInfo{Name: "claude_code", Version: "2.1.306", CollectionMethod: obs.CollectionMethodGateway},
		Session: &obs.SessionInfo{ID: "0b7c3d7e-5f0a-4c8e-9d61-3f0d5e8c2a14"}, Model: "claude-opus-4-6",
	}
	read := base
	read.Event = obs.EventInfo{Kind: "agent_runtime", Action: "file.read", Category: "file", Fidelity: obs.FidelityInferred}
	read.File = &obs.FileInfo{Path: "/home/dev/.aws/credentials", Operation: "read"}
	read.GenAI = &obs.GenAIInfo{Tool: &obs.GenAIToolInfo{Name: "Read", Call: &obs.GenAIToolCallInfo{ID: "toolu_01", Result: result}}}
	prompt := base
	prompt.Event = obs.EventInfo{Kind: "agent_runtime", Action: "prompt.submitted", Category: "prompt", Fidelity: obs.FidelityObserved}
	prompt.Prompt = &obs.PromptInfo{Text: "summarize the failing tests and fix them"}
	return map[string]obs.Event{"call": read, "content": prompt}
}

func BenchmarkEventID(b *testing.B) {
	for name, e := range eventIDBenchEvents() {
		b.Run(name+"/EventID", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = obs.EventID(e)
			}
		})
		b.Run(name+"/EventIDForLine", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				line, _ := json.Marshal(e)
				_ = obs.EventIDForLine(line)
			}
		})
	}
}
