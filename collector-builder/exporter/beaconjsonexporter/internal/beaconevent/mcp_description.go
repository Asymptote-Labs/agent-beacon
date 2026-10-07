package beaconevent

import (
	"encoding/json"
	"errors"
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const maxClaudeMCPDescriptionsPerRequest = 256

var errInvalidClaudeToolKey = errors.New("invalid Claude MCP tool JSON key")

type claudeMCPToolDescription struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Truncated and Redacted say what claudeMCPTools did to Description, which is the stored copy
	// rather than what the server sent, so a listing's content marker can say so.
	Truncated bool `json:"-"`
	Redacted  bool `json:"-"`
}

// extractClaudeMCPToolDescriptions reads only MCP tool identity and description from a Claude Code
// API request body. The body also contains the complete conversation, tool inputs, and results;
// those values are skipped and never retained. A body Claude Code cut short still yields every tool
// entry decoded whole before the cut.
func extractClaudeMCPToolDescriptions(body string) []claudeMCPToolDescription {
	if body == "" {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return nil
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil
		}
		if key == "tools" {
			return decodeClaudeToolArray(decoder)
		}
		if err := skipJSONValue(decoder); err != nil {
			return nil
		}
	}
	return nil
}

func decodeClaudeToolArray(decoder *json.Decoder) []claudeMCPToolDescription {
	open, err := decoder.Token()
	if err != nil || open != json.Delim('[') {
		return nil
	}
	out := make([]claudeMCPToolDescription, 0)
	for decoder.More() {
		tool, complete, err := decodeClaudeTool(decoder)
		if err != nil {
			return out
		}
		if complete && strings.HasPrefix(tool.Name, "mcp__") && tool.Description != "" {
			if _, leaf := deriveMCPServerToolFromName(tool.Name); leaf != "" {
				out = append(out, tool)
				if len(out) == maxClaudeMCPDescriptionsPerRequest {
					break
				}
			}
		}
	}
	return out
}

func decodeClaudeTool(decoder *json.Decoder) (claudeMCPToolDescription, bool, error) {
	var tool claudeMCPToolDescription
	open, err := decoder.Token()
	if err != nil {
		return tool, false, err
	}
	if open != json.Delim('{') {
		return tool, false, errors.New("Claude MCP tool entry is not an object")
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return tool, false, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return tool, false, errInvalidClaudeToolKey
		}
		switch key {
		case "name":
			err = decoder.Decode(&tool.Name)
		case "description":
			err = decoder.Decode(&tool.Description)
		default:
			err = skipJSONValue(decoder)
		}
		if err != nil {
			return tool, false, err
		}
	}
	_, err = decoder.Token()
	return tool, err == nil, err
}

func skipJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		for decoder.More() {
			if _, err := decoder.Token(); err != nil {
				return err
			}
			if err := skipJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := skipJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return nil
	}
}

// ClaudeMCPToolsAttr holds the MCP tools a Claude Code request body advertised to the model once the
// body itself is gone: a list of {name, description}, each description redacted and capped at
// DefaultStringLimit, the limit gen_ai.tool.description is stored at, with truncated and redacted
// set on an entry whose description that changed. The claude_api_body processor leaves only the
// tools new to their session in it, so each destination is sent a tool once.
const ClaudeMCPToolsAttr = "beacon.mcp.tools"

// claudeMCPTools reads the MCP tools a request body advertised, redacted and capped the way every
// destination keeps them. A body with no MCP tool name in it is not walked: every model request
// carries one, and most sessions connect no MCP server.
//
// Secrets are replaced before the cap, as for the WebFetch page, so a secret that straddles the cut
// cannot survive as a prefix.
func claudeMCPTools(body string) []claudeMCPToolDescription {
	if !strings.Contains(body, `"mcp__`) {
		return nil
	}
	tools := extractClaudeMCPToolDescriptions(body)
	for i := range tools {
		redacted := asymptoteobserve.RedactString(tools[i].Description)
		kept := asymptoteobserve.TruncateString(redacted, asymptoteobserve.DefaultStringLimit)
		tools[i].Redacted = redacted != tools[i].Description
		tools[i].Truncated = kept != redacted
		tools[i].Description = kept
	}
	return tools
}

// claudeMCPToolsFromAttr reads ClaudeMCPToolsAttr back from a record the processor rewrote.
func claudeMCPToolsFromAttr(value interface{}) []claudeMCPToolDescription {
	entries, _ := value.([]interface{})
	var tools []claudeMCPToolDescription
	for _, entry := range entries {
		fields, _ := entry.(map[string]interface{})
		tool := claudeMCPToolDescription{Name: FirstString(fields, "name"), Description: FirstString(fields, "description")}
		tool.Truncated, _ = BoolAttr(fields, "truncated")
		tool.Redacted, _ = BoolAttr(fields, "redacted")
		if tool.Name != "" && tool.Description != "" {
			tools = append(tools, tool)
		}
	}
	return tools
}

func putClaudeMCPTools(attrs pcommon.Map, tools []claudeMCPToolDescription) {
	list := attrs.PutEmptySlice(ClaudeMCPToolsAttr)
	for _, tool := range tools {
		entry := list.AppendEmpty().SetEmptyMap()
		entry.PutStr("name", tool.Name)
		entry.PutStr("description", tool.Description)
		if tool.Truncated {
			entry.PutBool("truncated", true)
		}
		if tool.Redacted {
			entry.PutBool("redacted", true)
		}
	}
}

// claudeMCPToolListings records each MCP tool a request advertised to the model as one
// mcp.tool_listed. attrs is the record's with its body already removed. The request's own event is
// not kept beside them: claude_code.api_request already records the call.
func (c Converter) claudeMCPToolListings(attrs map[string]interface{}, record plog.LogRecord, tools []claudeMCPToolDescription) []Event {
	ts := Timestamp(record.Timestamp().AsTime())
	harness := HarnessName(attrs, ClaudeAPIRequestBody)
	var events []Event
	for _, tool := range tools {
		server, name := deriveMCPServerToolFromName(tool.Name)
		if server == "" || name == "" {
			continue
		}
		event := NewEvent("mcp.tool_listed", "mcp", "info", harness, ts)
		event.Message = "Claude Code MCP tool advertised to the model"
		c.PopulateCommon(&event, attrs)
		event.MCP = &MCPInfo{Server: server, Tool: name}
		if event.GenAI == nil {
			event.GenAI = &GenAIInfo{}
		}
		event.GenAI.Tool = &GenAIToolInfo{Name: tool.Name, Description: tool.Description}
		// The description is already redacted and capped, so the marker describes the stored copy and
		// the flags carry what was done to it.
		event.Content = asymptoteobserve.RetainedContent(tool.Description, asymptoteobserve.DefaultStringLimit)
		event.Content.Truncated = event.Content.Truncated || tool.Truncated
		event.Content.Redacted = event.Content.Redacted || tool.Redacted
		event.Raw = map[string]interface{}{
			"otel_signal":     "logs",
			"source":          ClaudeAPIRequestBody,
			"query_source":    FirstString(attrs, "query_source"),
			"request_body_id": FirstString(attrs, "request_body_id"),
		}
		if !record.TraceID().IsEmpty() {
			event.Trace = &TraceInfo{ID: record.TraceID().String()}
			if !record.SpanID().IsEmpty() {
				event.Trace.SpanID = record.SpanID().String()
			}
		}
		events = append(events, event)
	}
	return events
}
