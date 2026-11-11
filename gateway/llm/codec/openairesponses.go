package codec

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// openAIResponses decodes the OpenAI Responses API.
type openAIResponses struct{}

func (openAIResponses) Protocol() llm.Protocol { return llm.OpenAIResponses }

func (openAIResponses) ErrorBody(status int, message string) []byte {
	return openAIErrorBody(status, message)
}

// responsesRequestFields are the top-level request fields the codec maps, plus the scalar knobs
// it ignores. Among those, previous_response_id and conversation make the call stateful: the
// provider holds the earlier turns, so the request carries only what is new and Messages is not
// the whole conversation.
var responsesRequestFields = set(
	"model", "stream", "instructions", "input", "tools", "text", "tool_choice", "response_format",
	"max_output_tokens", "temperature", "top_p", "top_logprobs", "metadata", "user", "store",
	"parallel_tool_calls", "service_tier", "reasoning", "previous_response_id", "conversation", "include",
	"truncation", "prompt_cache_key", "prompt_cache_retention", "safety_identifier", "stream_options",
	"background", "max_tool_calls",
)

func (o openAIResponses) DecodeRequest(body []byte) (*llm.Request, error) {
	return o.request(body, nil)
}

func (openAIResponses) request(body []byte, skim *skimInput) (*llm.Request, error) {
	req, err := decodeResponsesRequest(body, skim)
	if err != nil {
		return nil, fmt.Errorf("codec: openai responses request: %w", err)
	}
	return req, nil
}

func decodeResponsesRequest(body []byte, skim *skimInput) (*llm.Request, error) {
	req := &llm.Request{}
	var (
		instructions string
		instrParts   []llm.Part // the instructions, when cut from the body
		instrCut     bool
		inputText    *string // input sent as a string
		input        []llm.Message
		itemTypes    []string // input item types the codec does not know
		tools        []llm.Tool
	)
	formats := newFormats("text", "tool_choice", "response_format")
	unknown, err := decodeRequestObject(body, responsesRequestFields, func(dec *jsontext.Decoder, name []byte) (bool, error) {
		switch string(name) {
		case "model":
			return true, json.UnmarshalDecode(dec, &req.Model)
		case "stream":
			return true, json.UnmarshalDecode(dec, &req.Stream)
		case "instructions":
			if v, ok, err := skim.member(dec, "instructions"); ok {
				instrParts, _ = v.([]llm.Part)
				instrCut = true
				return true, err
			}
			return true, json.UnmarshalDecode(dec, &instructions)
		case "input":
			inputText, input, itemTypes, req.Elided = nil, nil, nil, nil
			switch dec.PeekKind() {
			case 'n':
				_, err := dec.ReadToken()
				return true, err
			case '"':
				s, err := readString(dec)
				inputText = &s
				return true, err
			case '[':
				if skim != nil {
					var err error
					input, req.Elided, itemTypes, err = skim.messages(dec, openAIResponses{})
					return true, err
				}
				var it responsesItem
				return true, readArray(dec, func() error {
					it = responsesItem{}
					if err := json.UnmarshalDecode(dec, &it); err != nil {
						return err
					}
					var ok bool
					if input, ok = it.appendMessages(input); !ok {
						itemTypes = append(itemTypes, it.Type)
					}
					return nil
				})
			}
			return true, fmt.Errorf("input is neither a string nor an array")
		case "previous_response_id":
			s, err := readString(dec)
			req.PreviousResponse = s
			return true, err
		case "conversation":
			id, err := readConversation(dec)
			req.Conversation = id
			return true, err
		case "tools":
			if v, ok, err := skim.member(dec, "tools"); ok {
				tools, _ = v.([]llm.Tool)
				return true, err
			}
			var ts []responsesTool
			err := json.UnmarshalDecode(dec, &ts)
			tools = appendResponsesTools(nil, ts, "")
			return true, err
		}
		return formats.read(dec, name)
	})
	if err != nil {
		return nil, err
	}
	req.Unknown, req.Format = unknown, formats.list()
	for _, typ := range itemTypes {
		if name := "input.item=" + typ; !slices.Contains(req.Unknown, name) {
			req.Unknown = append(req.Unknown, name)
		}
	}
	var lead []llm.Message
	if !instrCut {
		instrParts = appendText(nil, llm.PartText, instructions)
	}
	if instrParts != nil {
		lead = append(lead, llm.Message{Role: llm.RoleSystem, Parts: instrParts})
	}
	if inputText != nil {
		lead = append(lead, llm.Message{Role: llm.RoleUser, Parts: appendText(nil, llm.PartText, *inputText)})
	}
	req.Messages = slices.Insert(input, 0, lead...)
	req.Tools = tools
	return req, nil
}

// readConversation reads Responses' conversation parameter: an id, or an object naming one, or
// null.
func readConversation(dec *jsontext.Decoder) (string, error) {
	if dec.PeekKind() != '{' {
		return readString(dec)
	}
	var c struct {
		ID string `json:"id"`
	}
	err := json.UnmarshalDecode(dec, &c)
	return c.ID, err
}

// decodeResponsesTools decodes a tools value alone, as decodeResponsesRequest decodes it.
func decodeResponsesTools(raw []byte) (any, error) {
	var tools []responsesTool
	if err := unmarshal(raw, &tools); err != nil {
		return nil, err
	}
	return appendResponsesTools(nil, tools, ""), nil
}

// decodeResponsesInstructions decodes an instructions value alone, to the system message's parts
// decodeResponsesRequest makes of it.
func decodeResponsesInstructions(raw []byte) (any, error) {
	var instructions string
	if err := unmarshal(raw, &instructions); err != nil {
		return nil, err
	}
	return appendText(nil, llm.PartText, instructions), nil
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  jsontext.Value  `json:"parameters"`
	Format      compactText     `json:"format"`
	ServerLabel string          `json:"server_label"`
	Tools       []responsesTool `json:"tools"`
}

// appendResponsesTools flattens tool definitions. Codex groups tools in namespaces; the model
// calls an inner tool by its qualified name, so that is the name here.
func appendResponsesTools(out []llm.Tool, tools []responsesTool, prefix string) []llm.Tool {
	for _, t := range tools {
		switch t.Type {
		case "function":
			out = append(out, llm.Tool{Name: prefix + t.Name, Description: t.Description, Parameters: t.Parameters, Kind: "function"})
		case "custom":
			out = append(out, llm.Tool{Name: prefix + t.Name, Description: t.Description, Parameters: t.Format.params(), Kind: "custom"})
		case "namespace":
			out = appendResponsesTools(out, t.Tools, prefix+t.Name+"__")
		case "mcp":
			out = append(out, llm.Tool{Name: prefix + t.ServerLabel, Description: t.Description, Kind: t.Type})
		default:
			name := t.Name
			if name == "" {
				name = t.Type
			}
			out = append(out, llm.Tool{Name: prefix + name, Description: t.Description, Kind: t.Type})
		}
	}
	return out
}

// responsesItem is any input or output item; fields are shared across item types.
type responsesItem struct {
	Type        string           `json:"type"`
	Role        string           `json:"role"`
	Content     responsesContent `json:"content"`
	ID          string           `json:"id"`
	CallID      string           `json:"call_id"`
	Name        string           `json:"name"`
	ServerLabel string           `json:"server_label"`
	Arguments   jsonText         `json:"arguments"`
	Input       jsonText         `json:"input"`
	Action      jsonText         `json:"action"`
	Operation   jsonText         `json:"operation"`
	Queries     jsonText         `json:"queries"`
	Code        jsonText         `json:"code"`
	Output      responsesContent `json:"output"`
	Summary     []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

// appendMessages appends the conversation an item stands for, classified by item type: calls of
// every tool kind (function, custom, shell, hosted) are assistant tool calls, their outputs tool
// results. ok is false for an item type the codec does not know.
func (it *responsesItem) appendMessages(msgs []llm.Message) (_ []llm.Message, ok bool) {
	switch {
	case it.Type == "message" || it.Type == "" && it.Role != "":
		return append(msgs, llm.Message{Role: responsesRole(it.Role), Parts: it.Content.parts}), true
	case it.Type == "reasoning":
		var parts []llm.Part
		for _, s := range it.Summary {
			parts = appendText(parts, llm.PartReasoning, s.Text)
		}
		parts = append(parts, it.Content.parts...)
		return append(msgs, llm.Message{Role: llm.RoleAssistant, Parts: parts}), true
	case it.Type == "item_reference":
		// A pointer to a stored item: its content is not in the request.
		return msgs, true
	case it.Type == "mcp_call":
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
			callPart(it.ID, "mcp__"+it.ServerLabel+"__"+it.Name, it.Arguments.text),
		}})
		if it.Output.set {
			msgs = append(msgs, llm.Message{Role: llm.RoleTool, Parts: []llm.Part{resultPart(it.ID, it.Output.parts, false)}})
		}
		return msgs, true
	case strings.HasSuffix(it.Type, "_call"):
		name := it.Name
		if name == "" {
			name = strings.TrimSuffix(it.Type, "_call")
		}
		return append(msgs, llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{callPart(it.callID(), name, it.args())}}), true
	case strings.HasSuffix(it.Type, "_output"):
		return append(msgs, llm.Message{Role: llm.RoleTool, Parts: []llm.Part{resultPart(it.callID(), it.Output.parts, false)}}), true
	}
	return msgs, false
}

// callID is the ID a call's output refers back to; hosted tool calls have only an item ID.
func (it *responsesItem) callID() string {
	if it.CallID != "" {
		return it.CallID
	}
	return it.ID
}

// args is a call's input, from whichever field its tool kind carries it in.
func (it *responsesItem) args() string {
	for _, f := range []*jsonText{&it.Arguments, &it.Input, &it.Action, &it.Operation, &it.Queries, &it.Code} {
		if f.set {
			return f.text
		}
	}
	return ""
}

func (openAIResponses) decode(raw []byte, msgs []llm.Message) ([]llm.Message, bool, string, error) {
	var it responsesItem
	if err := unmarshal(raw, &it); err != nil {
		return nil, false, "", err
	}
	msgs, ok := it.appendMessages(msgs)
	return msgs, ok, it.Type, nil
}

// responsesArgFields are the members a call's input may arrive in, in args' order.
var responsesArgFields = [...]string{"arguments", "input", "action", "operation", "queries", "code"}

// measure mirrors responsesItem's decoding and appendMessages. It gives up on item types the
// codec does not know, so that decoding records them.
func (openAIResponses) measure(sk *skimmer, raw []byte) bool {
	s := rawScan{b: raw}
	var (
		typ, role, id, callID                           []byte
		nameLen, labelLen                               int
		content, output, summary                        partSum
		contentSeen, outputSeen, outputSet, summarySeen bool
		args                                            [len(responsesArgFields)]struct {
			n         int
			set, seen bool
		}
	)
	if !s.object(func(name []byte) bool {
		switch string(name) {
		case "type":
			return s.strVal(&typ)
		case "role":
			return s.strVal(&role)
		case "id":
			return s.strVal(&id)
		case "call_id":
			return s.strVal(&callID)
		case "name":
			return s.strLen(&nameLen)
		case "server_label":
			return s.strLen(&labelLen)
		case "content":
			if contentSeen {
				return false
			}
			contentSeen = true
			_, ok := sk.responsesContent(&s, &content)
			return ok
		case "output":
			if outputSeen {
				return false
			}
			outputSeen = true
			var ok bool
			outputSet, ok = sk.responsesContent(&s, &output)
			return ok
		case "summary":
			if summarySeen {
				return false
			}
			summarySeen = true
			return s.isNull() || s.array(func() bool {
				var n int
				ok := s.isNull() || s.object(func(name []byte) bool {
					if string(name) == "text" {
						return s.strLen(&n)
					}
					s.skip()
					return true
				})
				summary.addReasoning(n)
				return ok
			})
		}
		for i, f := range responsesArgFields {
			if f == string(name) {
				if args[i].seen {
					return false
				}
				args[i].seen = true
				args[i].n, args[i].set = s.jsonTextLen()
				return true
			}
		}
		s.skip()
		return true
	}) {
		return false
	}
	idLo := len(sk.ids)
	switch t := string(typ); {
	case t == "message" || t == "" && len(role) > 0:
		sk.push(responsesRole(string(role)), &content, idLo)
	case t == "reasoning":
		summary.addParts(&content)
		sk.push(llm.RoleAssistant, &summary, idLo)
	case t == "item_reference":
	case t == "mcp_call":
		var call partSum
		call.addCall(len("mcp__")+labelLen+len("__")+nameLen, args[0].n)
		sk.addID(id)
		sk.push(llm.RoleAssistant, &call, idLo)
		if outputSet {
			var result partSum
			result.addResult(&output)
			sk.push(llm.RoleTool, &result, len(sk.ids))
		}
	case strings.HasSuffix(t, "_call"):
		if nameLen == 0 {
			nameLen = len(t) - len("_call")
		}
		argsLen := 0
		for _, a := range args {
			if a.set {
				argsLen = a.n
				break
			}
		}
		if len(callID) == 0 {
			callID = id
		}
		var call partSum
		call.addCall(nameLen, argsLen)
		sk.addID(callID)
		sk.push(llm.RoleAssistant, &call, idLo)
	case strings.HasSuffix(t, "_output"):
		var result partSum
		result.addResult(&output)
		sk.push(llm.RoleTool, &result, idLo)
	default:
		return false
	}
	return true
}

// responsesContent measures what responsesContent decodes into ps, reporting whether it is set.
func (sk *skimmer) responsesContent(s *rawScan, ps *partSum) (set, ok bool) {
	switch s.peek() {
	case 'n':
		s.skip()
		return false, true
	case '"':
		ps.addText(s.text())
		return true, true
	case '{':
		return true, responsesPartLen(s, ps)
	case '[':
		return true, s.array(func() bool { return s.isNull() || responsesPartLen(s, ps) })
	}
	return false, false
}

// responsesPartLen measures a responsesPart and what append appends.
func responsesPartLen(s *rawScan, ps *partSum) bool {
	var (
		typ                 []byte
		textLen, refusalLen int
		audio, audioSeen    bool
	)
	if !s.object(func(name []byte) bool {
		var n int
		switch string(name) {
		case "type":
			return s.strVal(&typ)
		case "text":
			return s.strLen(&textLen)
		case "refusal":
			return s.strLen(&refusalLen)
		case "image_url", "file_id", "file_data", "file_url":
			return s.strLen(&n)
		case "input_audio":
			if audioSeen {
				return false
			}
			audioSeen = true
			if s.isNull() {
				return true
			}
			audio = true
			return s.object(func(name []byte) bool {
				if string(name) == "data" || string(name) == "format" {
					return s.strLen(&n)
				}
				s.skip()
				return true
			})
		}
		s.skip()
		return true
	}) {
		return false
	}
	switch t := string(typ); {
	case t == "input_image", t == "computer_screenshot", t == "input_file", t == "input_audio" && audio:
		ps.media++
	case t == "refusal":
		ps.addText(refusalLen)
	case t == "reasoning_text":
		ps.addReasoning(textLen)
	default:
		ps.addText(textLen)
	}
	return true
}

func responsesRole(role string) llm.Role {
	switch role {
	case "assistant":
		return llm.RoleAssistant
	case "system", "developer":
		return llm.RoleSystem
	}
	return llm.RoleUser
}

// responsesContent is message content or a tool output, decoded into parts: a string, an array
// of parts, or (a computer call's screenshot) a single part. set reports that the field was
// present and not null.
type responsesContent struct {
	parts []llm.Part
	set   bool
}

func (c *responsesContent) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	*c = responsesContent{set: true}
	switch dec.PeekKind() {
	case 'n':
		c.set = false
		_, err := dec.ReadToken()
		return err
	case '"':
		s, err := readString(dec)
		c.parts = appendText(nil, llm.PartText, s)
		return err
	case '{':
		var p responsesPart
		if err := json.UnmarshalDecode(dec, &p); err != nil {
			return err
		}
		c.parts = p.append(nil)
		return nil
	case '[':
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		for dec.PeekKind() != ']' {
			var p responsesPart
			if err := json.UnmarshalDecode(dec, &p); err != nil {
				return err
			}
			c.parts = p.append(c.parts)
		}
		_, err := dec.ReadToken()
		return err
	}
	return fmt.Errorf("content is neither a string, an object nor an array")
}

type responsesPart struct {
	Type       string `json:"type"`
	Text       string `json:"text"`
	Refusal    string `json:"refusal"`
	ImageURL   string `json:"image_url"`
	FileID     string `json:"file_id"`
	FileData   string `json:"file_data"`
	FileURL    string `json:"file_url"`
	InputAudio *struct {
		Data   string `json:"data"`
		Format string `json:"format"`
	} `json:"input_audio"`
}

func (p responsesPart) append(parts []llm.Part) []llm.Part {
	switch p.Type {
	case "input_image", "computer_screenshot":
		if p.ImageURL != "" {
			return append(parts, mediaPart(urlMedia("image", p.ImageURL)))
		}
		return append(parts, mediaPart(fileMedia("image", p.FileID)))
	case "input_file":
		switch {
		case p.FileData != "":
			return append(parts, mediaPart(inlineMedia("file", p.FileData)))
		case p.FileURL != "":
			return append(parts, mediaPart(urlMedia("file", p.FileURL)))
		}
		return append(parts, mediaPart(fileMedia("file", p.FileID)))
	case "input_audio":
		if p.InputAudio != nil {
			return append(parts, mediaPart(&llm.Media{Type: "audio", MIME: "audio/" + p.InputAudio.Format, Data: p.InputAudio.Data}))
		}
	case "refusal":
		return appendText(parts, llm.PartText, p.Refusal)
	case "reasoning_text":
		return appendText(parts, llm.PartReasoning, p.Text)
	}
	return appendText(parts, llm.PartText, p.Text)
}

type responsesResponse struct {
	ID                string `json:"id"`
	Model             string `json:"model"`
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []responsesItem `json:"output"`
	Usage  *struct {
		InputTokens        int64 `json:"input_tokens"`
		OutputTokens       int64 `json:"output_tokens"`
		InputTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
	Error *apiError `json:"error"`
}

// response decodes r with output items; output item types the codec does not know are dropped,
// as a response has no Unknown to surface them in.
func (r *responsesResponse) response(items []responsesItem) *llm.Response {
	resp := &llm.Response{ID: r.ID, Model: r.Model, StopReason: r.Status}
	if r.Status == "incomplete" && r.IncompleteDetails != nil && r.IncompleteDetails.Reason != "" {
		resp.StopReason = r.IncompleteDetails.Reason
	}
	if u := r.Usage; u != nil {
		resp.Usage = llm.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheRead: u.InputTokensDetails.CachedTokens}
	}
	var msgs []llm.Message
	for i := range items {
		msgs, _ = items[i].appendMessages(msgs)
	}
	for _, m := range msgs {
		resp.Parts = append(resp.Parts, m.Parts...)
	}
	return resp
}

func (openAIResponses) DecodeResponse(body []byte) (*llm.Response, error) {
	decode := decodeResponsesResponse
	if isSSE(body) {
		decode = decodeResponsesStream
	}
	resp, err := decode(body)
	if err != nil {
		return nil, fmt.Errorf("codec: openai responses response: %w", err)
	}
	return resp, nil
}

func decodeResponsesResponse(body []byte) (*llm.Response, error) {
	var r responsesResponse
	if err := unmarshal(body, &r); err != nil {
		return nil, err
	}
	// A failed response carries an error too, but it is still a response; an error reply has no ID.
	if r.ID == "" && r.Error != nil {
		return nil, r.Error.err()
	}
	return r.response(r.Output), nil
}

// decodeResponsesStream prefers the full response a terminal event carries and falls back to
// the items streamed as done: some backends (Codex's, with store=false) send a terminal response
// with an empty output.
func decodeResponsesStream(body []byte) (*llm.Response, error) {
	var created, final *responsesResponse
	items := map[int]responsesItem{}
	for s := newSSEScanner(body); s.next(); {
		if inertResponsesEvent(s.data) {
			continue
		}
		var e struct {
			Type        string             `json:"type"`
			Response    *responsesResponse `json:"response"`
			OutputIndex int                `json:"output_index"`
			Item        *responsesItem     `json:"item"`
			Code        string             `json:"code"`
			Message     string             `json:"message"`
			Error       *apiError          `json:"error"`
		}
		if err := unmarshal(s.data, &e); err != nil {
			return nil, fmt.Errorf("event %q: %w", s.name, err)
		}
		switch e.Type {
		case "response.created", "response.in_progress":
			if e.Response != nil {
				created = e.Response
			}
		case "response.output_item.done":
			if e.Item != nil {
				items[e.OutputIndex] = *e.Item
			}
		case "response.completed", "response.incomplete", "response.failed":
			final = e.Response
		case "error":
			if e.Error != nil {
				return nil, e.Error.err()
			}
			return nil, (&apiError{Type: e.Code, Message: e.Message}).err()
		}
	}
	r := final
	if r == nil {
		// Without a terminal event the stream ended early: there is no status to report.
		r = &responsesResponse{}
		if created != nil {
			r.ID, r.Model = created.ID, created.Model
		}
	}
	if len(r.Output) > 0 {
		return r.response(r.Output), nil
	}
	var done []responsesItem
	for _, i := range slices.Sorted(maps.Keys(items)) {
		done = append(done, items[i])
	}
	return r.response(done), nil
}

// inertResponsesEvent is the fast path: it reports that an event is one the decoder reads and
// ignores (text and argument deltas, part and item announcements: every type but the ones
// decodeResponsesStream acts on), checking what decoding it would check, so that it is skipped
// without being decoded. Anything else goes to the decoder.
func inertResponsesEvent(data []byte) bool {
	s := newJSONReader(data)
	var typ []byte
	var seen uint8
	ok := s.object(func(name []byte) bool {
		switch string(name) {
		case "type":
			if seen&1 != 0 {
				return false
			}
			seen |= 1
			var ok bool
			typ, ok = s.quoted()
			return ok
		case "output_index":
			if seen&2 != 0 {
				return false
			}
			seen |= 2
			if s.isNull() {
				return true
			}
			_, ok := s.intValue()
			return ok
		case "response", "item", "code", "message", "error":
			return s.isNull()
		}
		return s.skip()
	}, false)
	if !ok || !s.done() {
		return false
	}
	switch string(unquotedType(typ)) {
	case "response.created", "response.in_progress", "response.output_item.done",
		"response.completed", "response.incomplete", "response.failed", "error":
		return false
	}
	return true
}
