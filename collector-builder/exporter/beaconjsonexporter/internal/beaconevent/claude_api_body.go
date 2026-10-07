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
	// gone. ClaudeWebFetchInputTruncatedAttr says Claude Code cut the body short and the text is
	// the part that survived.
	ClaudeWebFetchInputAttr          = "beacon.web_fetch.input"
	ClaudeWebFetchInputTruncatedAttr = "beacon.web_fetch.input_truncated"

	// Claude Code 2.1.291 sends the fetched page to a separate summarizer model call and tags that
	// call's body events with this query_source; the main model only ever gets the summary.
	claudeWebFetchApplyQuerySource = "web_fetch_apply"
	claudeWebFetchToolName         = "WebFetch"
)

// SanitizeClaudeAPIBody applies Beacon's policy for Claude Code's API body events
// (OTEL_LOG_RAW_API_BODIES) to attrs in place, and reports whether the record is dropped. It is
// keyed on the event name, never the harness name, so the Agent SDK or anything else that emits
// these events under another service name is held to the same policy.
//
// A body can carry the system prompt, the whole conversation, tool schemas and tool results. The
// one Beacon keeps is the WebFetch summarizer request, the only model call that carries the
// fetched page: its user-role text moves to ClaudeWebFetchInputAttr, and body and body_ref are
// removed. Every other body event, request or response, is dropped -- claude_code.api_request
// already records the call -- and so is a summarizer request with no text left to keep.
//
// It is idempotent: a record the claude_api_body processor already rewrote passes through
// unchanged.
func SanitizeClaudeAPIBody(attrs map[string]interface{}, recordBody string) (drop bool) {
	name := ClaudeLogEventName(attrs, recordBody)
	if name != ClaudeAPIRequestBody && name != ClaudeAPIResponseBody {
		return false
	}
	body, _ := attrs["body"].(string)
	delete(attrs, "body")
	delete(attrs, "body_ref")
	if name != ClaudeAPIRequestBody || FirstString(attrs, "query_source") != claudeWebFetchApplyQuerySource {
		return true
	}
	if text, _ := attrs[ClaudeWebFetchInputAttr].(string); text != "" {
		return false
	}
	truncated, _ := BoolAttr(attrs, "body_truncated")
	text, partial, ok := claudeUserText(body, truncated)
	if !ok || text == "" {
		return true
	}
	attrs[ClaudeWebFetchInputAttr] = text
	if partial {
		attrs[ClaudeWebFetchInputTruncatedAttr] = true
	}
	return false
}

// SanitizeClaudeAPIBodyRecord is SanitizeClaudeAPIBody on a log record. The claude_api_body
// processor calls it so the policy holds before any exporter sees the record, including ones such
// as splunk_hec that forward OTLP attributes as they arrive.
func SanitizeClaudeAPIBodyRecord(record plog.LogRecord) (drop bool) {
	attrs := record.Attributes()
	name := map[string]interface{}{}
	if value, ok := attrs.Get("event.name"); ok {
		name["event.name"] = value.AsString()
	}
	switch ClaudeLogEventName(name, record.Body().AsString()) {
	case ClaudeAPIRequestBody, ClaudeAPIResponseBody:
	default:
		return false
	}
	sanitized := AttrsToMap(attrs)
	if SanitizeClaudeAPIBody(sanitized, record.Body().AsString()) {
		return true
	}
	attrs.Remove("body")
	attrs.Remove("body_ref")
	if text, ok := sanitized[ClaudeWebFetchInputAttr].(string); ok {
		attrs.PutStr(ClaudeWebFetchInputAttr, text)
	}
	if truncated, _ := BoolAttr(sanitized, ClaudeWebFetchInputTruncatedAttr); truncated {
		attrs.PutBool(ClaudeWebFetchInputTruncatedAttr, true)
	}
	return false
}

type claudeWebFetchInput struct {
	text      string
	truncated bool
}

// takeClaudeWebFetchInput removes the summarizer input from attrs so that raw.attributes does not
// hold a second copy of the page beside gen_ai.tool.call.result.
func takeClaudeWebFetchInput(attrs map[string]interface{}, recordBody string) claudeWebFetchInput {
	if ClaudeLogEventName(attrs, recordBody) != ClaudeAPIRequestBody {
		return claudeWebFetchInput{}
	}
	text, _ := attrs[ClaudeWebFetchInputAttr].(string)
	truncated, _ := BoolAttr(attrs, ClaudeWebFetchInputTruncatedAttr)
	delete(attrs, ClaudeWebFetchInputAttr)
	delete(attrs, ClaudeWebFetchInputTruncatedAttr)
	return claudeWebFetchInput{text: text, truncated: truncated}
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
	// gen_ai is stored at the raw-attribute limit, so that is the limit the marker describes.
	event.Content = asymptoteobserve.RetainedContent(input.text, asymptoteobserve.DefaultRawStringLimit)
	if input.truncated {
		event.Content.Truncated = true
	}
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
