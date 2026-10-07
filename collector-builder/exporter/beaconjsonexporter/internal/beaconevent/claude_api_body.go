package beaconevent

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const (
	ClaudeAPIRequestBody  = "claude_code.api_request_body"
	ClaudeAPIResponseBody = "claude_code.api_response_body"

	// ClaudeWebFetchInputAttr holds the user-role text of Claude Code's WebFetch summarizer
	// request -- the fetched page and the summarizer prompt -- once the request body itself is
	// gone, with secrets redacted and capped at DefaultRawStringLimit. The Truncated attribute
	// says the text is a prefix, because Claude Code cut the body or the cap did; the Redacted
	// one says secrets were replaced.
	ClaudeWebFetchInputAttr          = "beacon.web_fetch.input"
	ClaudeWebFetchInputTruncatedAttr = "beacon.web_fetch.input_truncated"
	ClaudeWebFetchInputRedactedAttr  = "beacon.web_fetch.input_redacted"

	// Claude Code 2.1.291 sends the fetched page to a separate summarizer model call and tags that
	// call's body events with this query_source; the main model only ever gets the summary.
	claudeWebFetchApplyQuerySource = "web_fetch_apply"
	claudeWebFetchToolName         = "WebFetch"
)

// Beacon's policy for Claude Code's API body events (OTEL_LOG_RAW_API_BODIES) is keyed on the
// event name, never the harness name, so the Agent SDK or anything else that emits these events
// under another service name is held to it too.
//
// A body can carry the system prompt, the whole conversation, tool schemas and tool results. Beacon
// keeps two things from request bodies, the model context no other Claude Code event reports: the
// user-role text of the WebFetch summarizer request, the only model call that carries the fetched
// page, and the name and description of each MCP tool a request advertised. Every other body event,
// request or response, is dropped -- claude_code.api_request already records the call -- and so is
// a request with nothing left to keep.
//
// Even those are kept only when the endpoint was installed with --claude-capture-model-context,
// which the collector config carries as capture_model_context. Claude Code sends bodies whenever
// OTEL_LOG_RAW_API_BODIES is on, and a user can turn that on without asking Beacon to keep
// anything; without the opt-in, every body event is dropped.

func isClaudeAPIBody(eventName string) bool {
	return eventName == ClaudeAPIRequestBody || eventName == ClaudeAPIResponseBody
}

func isClaudeWebFetchSummarizerRequest(eventName, querySource string) bool {
	return eventName == ClaudeAPIRequestBody && querySource == claudeWebFetchApplyQuerySource
}

type claudeWebFetchInput struct {
	text      string
	truncated bool
	redacted  bool
}

// retainedClaudeWebFetchInput is the page text as Beacon keeps it at every destination, Splunk
// included: secrets redacted, then capped at the limit gen_ai is stored at.
func retainedClaudeWebFetchInput(text string, partial bool) claudeWebFetchInput {
	redacted := asymptoteobserve.RedactString(text)
	kept := asymptoteobserve.TruncateString(redacted, asymptoteobserve.DefaultRawStringLimit)
	return claudeWebFetchInput{text: kept, truncated: partial || kept != redacted, redacted: redacted != text}
}

// claudeAPIBodyKept is what Beacon keeps from one API body event.
type claudeAPIBodyKept struct {
	webFetch claudeWebFetchInput
	mcpTools []claudeMCPToolDescription
}

// takeClaudeAPIBody removes the body, body_ref and kept context of a Claude Code API body event from
// attrs, and returns what Beacon keeps when capture is on: the summarizer request's user text, the
// MCP tools a request advertised, or nothing. isBody reports whether eventName is a body event at
// all. A record the claude_api_body processor already rewrote carries what it kept in
// ClaudeWebFetchInputAttr and ClaudeMCPToolsAttr, so its body is not parsed again.
func takeClaudeAPIBody(attrs map[string]interface{}, eventName string, capture bool) (kept claudeAPIBodyKept, isBody bool) {
	if !isClaudeAPIBody(eventName) {
		return claudeAPIBodyKept{}, false
	}
	body, _ := attrs["body"].(string)
	input := claudeWebFetchInput{}
	input.text, _ = attrs[ClaudeWebFetchInputAttr].(string)
	input.truncated, _ = BoolAttr(attrs, ClaudeWebFetchInputTruncatedAttr)
	input.redacted, _ = BoolAttr(attrs, ClaudeWebFetchInputRedactedAttr)
	kept.mcpTools = claudeMCPToolsFromAttr(attrs[ClaudeMCPToolsAttr])
	for _, key := range claudeAPIBodyAttrs {
		delete(attrs, key)
	}
	if !capture || eventName != ClaudeAPIRequestBody {
		return claudeAPIBodyKept{}, true
	}
	if kept.mcpTools == nil {
		kept.mcpTools = claudeMCPTools(body)
	}
	if !isClaudeWebFetchSummarizerRequest(eventName, FirstString(attrs, "query_source")) {
		return kept, true
	}
	if input.text != "" {
		kept.webFetch = input
		return kept, true
	}
	bodyTruncated, _ := BoolAttr(attrs, "body_truncated")
	if text, partial, ok := claudeUserText(body, bodyTruncated); ok {
		kept.webFetch = retainedClaudeWebFetchInput(text, partial)
	}
	return kept, true
}

// claudeAPIBodyAttrs are the attributes that carry body content, removed from every body event.
var claudeAPIBodyAttrs = []string{"body", "body_ref", ClaudeWebFetchInputAttr, ClaudeWebFetchInputTruncatedAttr, ClaudeWebFetchInputRedactedAttr, ClaudeMCPToolsAttr}

// SanitizeClaudeAPIBodyRecord applies the API body policy to a log record in place, and reports
// whether the record is dropped. With capture on, a kept summarizer request leaves with its text in
// ClaudeWebFetchInputAttr, and a request that advertised MCP tools with the ones listings has not
// seen for its session in ClaudeMCPToolsAttr, in place of body and body_ref; with it off, every
// body event is dropped. The claude_api_body processor calls it so the policy holds before any
// exporter sees the record, including ones such as splunk_hec that forward OTLP attributes as they
// arrive.
func SanitizeClaudeAPIBodyRecord(record plog.LogRecord, capture bool, listings *MCPListings) (drop bool) {
	attrs := record.Attributes()
	eventName := ""
	if value, ok := attrs.Get("event.name"); ok {
		eventName = value.AsString()
	}
	name := claudeLogEventName(eventName, record.Body().AsString())
	if !isClaudeAPIBody(name) {
		return false
	}
	fields := AttrsToMap(attrs)
	kept, _ := takeClaudeAPIBody(fields, name, capture)
	session := FirstString(fields, "session.id")
	var tools []claudeMCPToolDescription
	for _, tool := range kept.mcpTools {
		if listings.Fresh(session, tool.Name, tool.Description) {
			tools = append(tools, tool)
		}
	}
	input := kept.webFetch
	if input.text == "" && len(tools) == 0 {
		return true
	}
	for _, key := range claudeAPIBodyAttrs {
		attrs.Remove(key)
	}
	if input.text != "" {
		attrs.PutStr(ClaudeWebFetchInputAttr, input.text)
	}
	if input.truncated {
		attrs.PutBool(ClaudeWebFetchInputTruncatedAttr, true)
	}
	if input.redacted {
		attrs.PutBool(ClaudeWebFetchInputRedactedAttr, true)
	}
	if len(tools) > 0 {
		putClaudeMCPTools(attrs, tools)
	}
	return false
}

// normalizeClaudeWebFetchInput records the summarizer input as what it is: content the WebFetch
// tool brought into the agent's context. It is third-party text, not something the user typed, so
// it goes to gen_ai.tool.call.result under the WebFetch tool name -- where
// e.gen_ai.tool.call.result_text and indirect-injection-in-tool-result read fetched pages on every
// other capture path -- and never to prompt.text, where prompt rules and the dashboard would take
// it for the user's prompt.
func normalizeClaudeWebFetchInput(event *Event, input claudeWebFetchInput) {
	if event == nil || input.text == "" {
		return
	}
	event.Event.Action = "tool.invoked"
	event.Event.Category = "tool"
	event.Event.Fidelity = asymptoteobserve.FidelityObserved
	ensureClaudeTool(event, claudeWebFetchToolName)
	if event.GenAI == nil {
		event.GenAI = &GenAIInfo{}
	}
	if event.GenAI.Tool == nil {
		event.GenAI.Tool = &GenAIToolInfo{}
	}
	event.GenAI.Tool.Name = claudeWebFetchToolName
	if event.GenAI.Tool.Call == nil {
		event.GenAI.Tool.Call = &GenAIToolCallInfo{}
	}
	event.GenAI.Tool.Call.Result = input.text
	// The text is already redacted and capped, so the marker describes the stored copy and the
	// flags carry what was done to it.
	event.Content = asymptoteobserve.RetainedContent(input.text, asymptoteobserve.DefaultRawStringLimit)
	event.Content.Truncated = event.Content.Truncated || input.truncated
	event.Content.Redacted = event.Content.Redacted || input.redacted
}

// claudeUserText returns the text parts of the user messages in an Anthropic Messages request
// body, joined by newlines. The system prompt, tool definitions, assistant turns and every
// non-text block (a tool_result's content included) are skipped.
//
// Claude Code cuts long inline bodies short and says so with body_truncated, which leaves JSON no
// decoder accepts -- and a long page is the one most worth keeping. For a truncated body the walk
// keeps every user text part that arrived whole plus the readable prefix of the part the cut fell
// in, and reports partial. A body that was not cut must be exactly one JSON object; anything else
// is not ok.
func claudeUserText(body string, truncated bool) (text string, partial, ok bool) {
	var walk userTextWalk
	dec := json.NewDecoder(strings.NewReader(body))
	for {
		token, err := dec.Token()
		if errors.Is(err, io.EOF) && walk.done {
			break
		}
		if err != nil {
			if !truncated || !walk.started {
				return "", false, false
			}
			walk.cut(body[dec.InputOffset():])
			return strings.Join(walk.parts, "\n"), true, true
		}
		if !walk.step(token) {
			return "", false, false
		}
		if walk.done && strings.TrimSpace(body[dec.InputOffset():]) != "" {
			return "", false, false
		}
	}
	return strings.Join(walk.parts, "\n"), truncated, true
}

// userTextWalk follows a request body token by token. Paths name the value being read: a key for
// each object, [] for each array, so a user text block is .messages[].content[].text.
type userTextWalk struct {
	stack   []jsonFrame
	parts   []string
	message *walkMessage
	block   *walkBlock
	started bool
	done    bool
}

type jsonFrame struct {
	object bool
	// key is the object key whose value is being read; inValue says a key was read and its
	// value comes next.
	key     string
	inValue bool
}

type walkMessage struct {
	role  string
	texts []string
}

type walkBlock struct {
	kind string
	text string
}

func (w *userTextWalk) path() string {
	var b strings.Builder
	for _, frame := range w.stack {
		if frame.object {
			b.WriteString(".")
			b.WriteString(frame.key)
		} else {
			b.WriteString("[]")
		}
	}
	return b.String()
}

func (w *userTextWalk) step(token json.Token) bool {
	if w.done {
		return false
	}
	if !w.started {
		if token != json.Delim('{') {
			return false
		}
		w.started = true
	}
	if n := len(w.stack); n > 0 && w.stack[n-1].object && !w.stack[n-1].inValue {
		if key, isKey := token.(string); isKey {
			w.stack[n-1].key, w.stack[n-1].inValue = key, true
			return true
		}
	}
	switch token {
	case json.Delim('{'), json.Delim('['):
		object := token == json.Delim('{')
		switch at := w.path(); {
		case object && at == ".messages[]":
			w.message = &walkMessage{}
		case object && at == ".messages[].content[]" && w.message != nil:
			w.block = &walkBlock{}
		}
		w.stack = append(w.stack, jsonFrame{object: object})
		return true
	case json.Delim('}'), json.Delim(']'):
		w.stack = w.stack[:len(w.stack)-1]
		if token == json.Delim('}') {
			switch w.path() {
			case ".messages[].content[]":
				w.closeBlock()
			case ".messages[]":
				w.closeMessage()
			}
		}
	default:
		if value, isString := token.(string); isString {
			w.value(w.path(), value)
		}
	}
	if n := len(w.stack); n == 0 {
		w.done = true
	} else if w.stack[n-1].object {
		w.stack[n-1].inValue = false
	}
	return true
}

func (w *userTextWalk) value(path, value string) {
	switch path {
	case ".messages[].role":
		if w.message != nil {
			w.message.role = value
		}
	case ".messages[].content":
		if w.message != nil && value != "" {
			w.message.texts = append(w.message.texts, value)
		}
	case ".messages[].content[].type":
		if w.block != nil {
			w.block.kind = value
		}
	case ".messages[].content[].text":
		if w.block != nil {
			w.block.text = value
		}
	}
}

func (w *userTextWalk) closeBlock() {
	if w.block != nil && w.message != nil && w.block.kind == "text" && w.block.text != "" {
		w.message.texts = append(w.message.texts, w.block.text)
	}
	w.block = nil
}

func (w *userTextWalk) closeMessage() {
	if w.message != nil && w.message.role == "user" {
		w.parts = append(w.parts, w.message.texts...)
	}
	w.message = nil
}

// cut ends the walk where a truncated body stops: the string value it stopped inside, if any,
// counts as far as it got, and the open block and message close there.
func (w *userTextWalk) cut(rest string) {
	if n := len(w.stack); n > 0 && w.stack[n-1].object && w.stack[n-1].inValue {
		if prefix := jsonStringPrefix(rest); prefix != "" {
			w.value(w.path(), prefix)
		}
	}
	w.closeBlock()
	w.closeMessage()
}

// jsonStringPrefix decodes the start of a JSON string value that the input ends inside.
func jsonStringPrefix(rest string) string {
	rest = strings.TrimLeft(rest, " \t\r\n:,")
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	raw := rest[1:]
	// The cut can split an escape sequence, the longest of which is \uXXXX.
	for back := range min(len(`\uXXXX`), len(raw)) + 1 {
		var value string
		if json.Unmarshal([]byte(`"`+raw[:len(raw)-back]+`"`), &value) == nil {
			return value
		}
	}
	return ""
}
