// Package events is the analyzer that turns each call into Beacon events, in runtime.jsonl's
// schema, and records them. It runs first: the events it builds, sanitized once, are left on the
// Turn for every analyzer after it.
//
// A gateway sees the conversation as the model receives it, so events describe what the model was
// shown and what it asked for:
//
//	new system text              → session.context      gen_ai.system_instructions
//	new MCP tool definition      → mcp.tool_listed      gen_ai.tool.description
//	new user turn                → prompt.submitted     prompt.text
//	the model's text             → agent.message        gen_ai.output.messages, gen_ai.response
//	the reply's token counts     → token.usage          gen_ai.usage, gen_ai.response
//	the model's tool call        → command.executed (shell), mcp.tool_invoked (MCP),
//	                               tool.invoked (reads, fetches, anything else)
//	the tool's result            → file.read (reads), tool.completed (anything else),
//	                               tool.failed (an error result)
//
// A call and its result never share an action, so they never share an event id. Actions with an
// effect outside the agent (running a command, calling an MCP server) are recorded when the model
// asks for them, before they happen; a read is recorded where its content arrives, as the
// session-store readers record it, so one read is one file.read carrying both its path and what
// it returned. Both a read's and an MCP tool's result are where a rule sees content from outside
// the agent (gen_ai.tool.call.result_text).
//
// System text and tool definitions are resent on every call. They are recorded once per session;
// a request that resends them lists them on the Turn (Turn.Resent) for analyzers that judge them
// again, and builds their events only when one asks.
package events

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"

	obs "github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/harness"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
	"github.com/asymptote-labs/agent-beacon/gateway/pipeline"
)

// maxEventBytes is the size Beacon's writers cap an event at.
const maxEventBytes = 65536

// Retention values: what recorded events keep of the content they describe. They are
// asymptoteobserve's content retention values.
const (
	RetentionFull     = obs.ContentRetentionFull
	RetentionMetadata = obs.ContentRetentionMetadata
)

// Config configures the analyzer.
type Config struct {
	// Retention is what recorded events keep of the content they describe: "full" (the
	// default; sanitized and redacted as Beacon's writers do), or "metadata", which writes them
	// as Beacon's Metadata-only forwarding does (asymptoteobserve.StripToMetadata): identifiers,
	// names, paths, command lines, model and token usage, no prompt, reply, instructions,
	// tool arguments or results. Analyzers still read the whole event.
	Retention string `yaml:"retention"`
	// Version stamps endpoint.agent_version.
	Version string `yaml:"-"`
}

// Analyzer implements pipeline.Analyzer.
type Analyzer struct {
	cfg          Config
	metadataOnly bool
	seq          atomic.Uint64
}

// New validates cfg.
func New(cfg Config) (*Analyzer, error) {
	switch cfg.Retention {
	case "", RetentionFull:
		cfg.Retention = RetentionFull
	case RetentionMetadata:
	default:
		return nil, fmt.Errorf("content.retention %q: want %s or %s", cfg.Retention, RetentionFull, RetentionMetadata)
	}
	return &Analyzer{cfg: cfg, metadataOnly: cfg.Retention == RetentionMetadata}, nil
}

// Name implements pipeline.Analyzer.
func (a *Analyzer) Name() string { return "events" }

// Analyze implements pipeline.Analyzer. It builds the call's events, leaves them on the Turn,
// and records the ones that are new.
func (a *Analyzer) Analyze(_ context.Context, t *pipeline.Turn) (pipeline.Report, error) {
	b := builder{a: a, t: t}
	if t.Call.Phase == gateway.PhaseResponse {
		b.response()
	} else {
		b.request()
	}
	t.Events = b.events
	var rep pipeline.Report
	if len(b.events) > 0 {
		rep.Records = make([]pipeline.Record, 0, len(b.events))
		for _, e := range b.events {
			if e.Recorded {
				rep.Records = append(rep.Records, pipeline.Record{Kind: pipeline.KindEvent, Value: e})
			}
		}
	}
	return rep, nil
}

// builder builds one call's events.
type builder struct {
	a      *Analyzer
	t      *pipeline.Turn
	events []*pipeline.Event
}

func (b *builder) request() {
	r := b.t.Call.Request
	newSystem := make(map[string]bool, len(b.t.New.System))
	for _, m := range b.t.New.System {
		newSystem[m.Text()] = true
	}
	type toolKey struct{ name, description string }
	newTools := make(map[toolKey]bool, len(b.t.New.Tools))
	for _, tool := range b.t.New.Tools {
		newTools[toolKey{tool.Name, tool.Description}] = true
	}
	resent := b.t.WantsResent()
	for i := range r.Messages {
		m := &r.Messages[i]
		if m.Role != llm.RoleSystem {
			continue
		}
		text := m.Text()
		switch {
		case newSystem[text]:
			b.add(b.systemContext(text), true)
		case resent:
			b.t.Resent = append(b.t.Resent, pipeline.NewResent("session.context", m, nil, func() *pipeline.Event {
				return b.finish(b.systemContext(text), false)
			}))
		}
	}
	for i := range r.Tools {
		tool := &r.Tools[i]
		bound := harness.Bind(tool.Name, "")
		if bound.Kind != harness.MCP {
			continue
		}
		switch {
		case newTools[toolKey{tool.Name, tool.Description}]:
			b.add(b.toolListed(tool, bound), true)
		case resent:
			b.t.Resent = append(b.t.Resent, pipeline.NewResent("mcp.tool_listed", nil, tool, func() *pipeline.Event {
				return b.finish(b.toolListed(tool, bound), false)
			}))
		}
	}
	var prompt []string
	for _, m := range b.t.New.Tail {
		for _, part := range m.Parts {
			switch {
			case part.Kind == llm.PartToolResult:
				b.result(part.Result)
			case m.Role == llm.RoleUser && part.Kind == llm.PartText && part.Text != "":
				prompt = append(prompt, part.Text)
			}
		}
	}
	if len(prompt) > 0 {
		text := strings.Join(prompt, "\n")
		e := b.event("prompt.submitted", "prompt", obs.FidelityObserved)
		e.Prompt = &obs.PromptInfo{Text: text}
		e.Content = obs.RetainedContent(text, obs.DefaultRawStringLimit)
		e.GenAI.Input = &obs.GenAIInputInfo{Messages: obs.TextInputMessages(text)}
		b.add(e, true)
	}
}

func (b *builder) systemContext(text string) obs.Event {
	e := b.event("session.context", "session", obs.FidelityObserved)
	e.GenAI.SystemInstructions = obs.SystemInstructionParts(text)
	return e
}

func (b *builder) toolListed(tool *llm.Tool, bound harness.Call) obs.Event {
	e := b.event("mcp.tool_listed", "mcp", obs.FidelityObserved)
	e.MCP = &obs.MCPInfo{Server: bound.Server, Tool: bound.Tool}
	e.GenAI.Tool = &obs.GenAIToolInfo{Name: tool.Name, Description: tool.Description}
	return e
}

// result records one tool result, as the action its call names it: the content of a read lands
// on the file.read, and every other result completes its call as tool.completed, keeping the
// call's target so the result says what it answers.
func (b *builder) result(r *llm.ToolResult) {
	tc, _ := b.t.ToolCall(r.CallID)
	call := harness.Bind(tc.Name, tc.Arguments)
	text := llm.JoinText(r.Parts)
	var e obs.Event
	switch {
	case r.IsError:
		e = b.event("tool.failed", "tool", obs.FidelityObserved)
		e.Tool = target(call)
		e.MCP = mcpTarget(call)
	case call.Kind == harness.Read:
		e = b.event("file.read", "file", obs.FidelityInferred)
		e.File = &obs.FileInfo{Path: call.Path, Operation: "read"}
		e.Tool = &obs.ToolInfo{Name: call.Name, Path: call.Path}
	case call.Kind == harness.Shell:
		e = b.event("tool.completed", "command", obs.FidelityObserved)
		e.Command = &obs.CommandInfo{Command: call.Command, Output: text}
		e.Tool = target(call)
	default:
		e = b.event("tool.completed", "tool", obs.FidelityObserved)
		e.Tool = target(call)
		e.MCP = mcpTarget(call)
	}
	e.GenAI.Tool = &obs.GenAIToolInfo{Name: call.Name, Call: &obs.GenAIToolCallInfo{ID: r.CallID, Result: text}}
	b.add(e, true)
}

// response records the model's reply: its text, its token counts, and the tool calls it made. A
// gateway sees the calls before the harness acts on them, so a command or MCP call is recorded
// before it runs.
func (b *builder) response() {
	resp := b.t.Call.Response
	var info *obs.GenAIResponseInfo
	if resp.ID != "" || resp.Model != "" || resp.StopReason != "" {
		info = &obs.GenAIResponseInfo{ID: resp.ID, Model: resp.Model}
		if resp.StopReason != "" {
			info.FinishReasons = []string{resp.StopReason}
		}
	}
	if text := llm.JoinText(resp.Parts); text != "" {
		e := b.event("agent.message", "agent", obs.FidelityObserved)
		e.Content = obs.RetainedContent(text, obs.DefaultRawStringLimit)
		e.GenAI.Output = &obs.GenAIOutputInfo{Messages: obs.TextOutputMessages(text)}
		e.GenAI.Response = info
		b.add(e, true)
	}
	if usage := b.usage(resp.Usage); usage != nil {
		e := b.event("token.usage", "metric", obs.FidelityObserved)
		e.GenAI.Usage = usage
		e.GenAI.Response = info
		b.add(e, true)
	}
	for _, tc := range llm.ToolCalls(resp.Parts) {
		call := harness.Bind(tc.Name, tc.Arguments)
		var e obs.Event
		switch call.Kind {
		case harness.Shell:
			e = b.event("command.executed", "command", obs.FidelityInferred)
			e.Command = &obs.CommandInfo{Command: call.Command}
		case harness.MCP:
			e = b.event("mcp.tool_invoked", "mcp", obs.FidelityInferred)
			e.MCP = mcpTarget(call)
		default:
			e = b.event("tool.invoked", "tool", obs.FidelityObserved)
		}
		e.Tool = target(call)
		e.GenAI.Tool = &obs.GenAIToolInfo{Name: call.Name, Call: &obs.GenAIToolCallInfo{ID: tc.ID, Arguments: tc.Arguments}}
		b.add(e, true)
	}
}

// usage is the reply's token counts as gen_ai.usage counts them: input, cache reads and cache
// writes are disjoint. OpenAI's protocols count cached tokens inside the input; Anthropic's apart.
func (b *builder) usage(u llm.Usage) *obs.GenAIUsageInfo {
	if u == (llm.Usage{}) {
		return nil
	}
	input := u.InputTokens
	if b.t.Call.Protocol == llm.OpenAIChat || b.t.Call.Protocol == llm.OpenAIResponses {
		input = max(input-u.CacheRead, 0)
	}
	output, read, write := u.OutputTokens, u.CacheRead, u.CacheWrite
	return &obs.GenAIUsageInfo{
		InputTokens:   &input,
		OutputTokens:  &output,
		CacheRead:     &obs.GenAIUsageCacheReadInfo{InputTokens: &read},
		CacheCreation: obs.NewCacheCreationUsage(write, nil),
	}
}

// target is the tool block of a bound call: its name and what it acts on.
func target(call harness.Call) *obs.ToolInfo {
	t := &obs.ToolInfo{Name: call.Name}
	switch call.Kind {
	case harness.Shell:
		t.Command = call.Command
	case harness.Read:
		t.Path = call.Path
	case harness.Fetch:
		t.Path = call.URL
	}
	return t
}

func mcpTarget(call harness.Call) *obs.MCPInfo {
	if call.Kind != harness.MCP {
		return nil
	}
	return &obs.MCPInfo{Server: call.Server, Tool: call.Tool}
}

// hostname is the gateway host, looked up once: asymptoteobserve.NewEvent asks the OS on every
// event.
var hostname, _ = os.Hostname()

// event starts an event as asymptoteobserve.NewEvent does, except that the user is the gateway's
// resolved identity, never the process's own (on a gateway that is the service account, not the
// person behind the call).
func (b *builder) event(action, category, fidelity string) obs.Event {
	c := b.t.Call
	e := obs.Event{
		Timestamp:     obs.FormatTimestamp(c.Time),
		Vendor:        obs.Vendor,
		Product:       obs.Product,
		SchemaVersion: obs.SchemaVersion,
		Event:         obs.EventInfo{Kind: "agent_runtime", Action: action, Category: category, Fidelity: fidelity},
		Severity:      obs.SeverityInfo,
		Endpoint:      obs.EndpointInfo{Hostname: hostname, OS: runtime.GOOS, AgentVersion: b.a.cfg.Version},
		Harness:       obs.HarnessInfo{Name: b.t.Harness.Name, Version: b.t.Harness.Version, CollectionMethod: obs.CollectionMethodGateway},
		Origin:        obs.OriginLocal,
		Session:       &obs.SessionInfo{ID: b.t.SessionID},
		Model:         c.Model,
		GenAI:         &obs.GenAIInfo{Conversation: &obs.GenAIConversationInfo{ID: b.t.SessionID}},
		Raw:           map[string]interface{}{"gateway.name": c.Gateway, "gateway.call_id": c.ID},
	}
	if id := c.Client.Identity; id.User != "" || id.Email != "" {
		e.User = obs.UserInfo{Name: firstNonEmpty(id.Email, id.User), UID: id.User}
	}
	return e
}

// finish sanitizes the event as Beacon's writers do. A recorded event is stamped with its
// sequence; its id is derived when it is written or cited.
func (b *builder) finish(e obs.Event, record bool) *pipeline.Event {
	e = obs.SanitizeEvent(e, maxEventBytes)
	if record {
		e.Sequence = b.a.seq.Add(1)
	}
	return &pipeline.Event{Beacon: e, Recorded: record, MetadataOnly: b.a.metadataOnly}
}

func (b *builder) add(e obs.Event, record bool) {
	b.events = append(b.events, b.finish(e, record))
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
