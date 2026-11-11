package codec

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"maps"
	"slices"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// openAIChat decodes OpenAI Chat Completions.
type openAIChat struct{}

func (openAIChat) Protocol() llm.Protocol { return llm.OpenAIChat }

func (openAIChat) ErrorBody(status int, message string) []byte {
	return openAIErrorBody(status, message)
}

// chatRequestFields are the top-level request fields the codec maps, plus the scalar knobs it
// ignores because they shape sampling, output modality or billing, not what the model reads.
var chatRequestFields = set(
	"model", "stream", "messages", "tools", "functions", "tool_choice", "response_format", "function_call",
	"max_tokens", "max_completion_tokens", "temperature", "top_p", "n", "stop", "presence_penalty",
	"frequency_penalty", "logit_bias", "logprobs", "top_logprobs", "seed", "user", "metadata", "store",
	"stream_options", "parallel_tool_calls", "service_tier", "reasoning_effort", "modalities", "audio",
	"verbosity", "safety_identifier", "prompt_cache_key", "prompt_cache_retention",
)

func (c openAIChat) DecodeRequest(body []byte) (*llm.Request, error) { return c.request(body, nil) }

func (openAIChat) request(body []byte, skim *skimInput) (*llm.Request, error) {
	req, err := decodeChatRequest(body, skim)
	if err != nil {
		return nil, fmt.Errorf("codec: openai chat request: %w", err)
	}
	return req, nil
}

type chatTool struct {
	Type     string        `json:"type"`
	Name     string        `json:"name"`
	Function *chatFunction `json:"function"`
	Custom   *struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		Format      compactText `json:"format"`
	} `json:"custom"`
}

func decodeChatRequest(body []byte, skim *skimInput) (*llm.Request, error) {
	req := &llm.Request{}
	var (
		tools     []llm.Tool
		functions []chatFunction
	)
	formats := newFormats("tool_choice", "response_format", "function_call")
	unknown, err := decodeRequestObject(body, chatRequestFields, func(dec *jsontext.Decoder, name []byte) (bool, error) {
		switch string(name) {
		case "model":
			return true, json.UnmarshalDecode(dec, &req.Model)
		case "stream":
			return true, json.UnmarshalDecode(dec, &req.Stream)
		case "messages":
			if skim != nil {
				var err error
				req.Messages, req.Elided, _, err = skim.messages(dec, openAIChat{})
				return true, err
			}
			req.Messages = nil
			var m chatMessage
			return true, readArray(dec, func() error {
				m = chatMessage{}
				if err := json.UnmarshalDecode(dec, &m); err != nil {
					return err
				}
				req.Messages = append(req.Messages, m.decode())
				return nil
			})
		case "tools":
			if v, ok, err := skim.member(dec, "tools"); ok {
				tools, _ = v.([]llm.Tool)
				return true, err
			}
			var ts []chatTool
			err := json.UnmarshalDecode(dec, &ts)
			tools = chatTools(ts)
			return true, err
		case "functions":
			functions = nil
			return true, json.UnmarshalDecode(dec, &functions)
		}
		return formats.read(dec, name)
	})
	if err != nil {
		return nil, err
	}
	req.Unknown, req.Format = unknown, formats.list()
	// Tools may be shared with other requests (see the memo): functions go to a copy.
	req.Tools = tools
	for _, f := range functions {
		req.Tools = append(slices.Clip(req.Tools), f.tool())
	}
	return req, nil
}

// chatTools maps tool definitions; none map to nil.
func chatTools(tools []chatTool) []llm.Tool {
	var out []llm.Tool
	for _, t := range tools {
		switch {
		case t.Type == "function" && t.Function != nil:
			out = append(out, t.Function.tool())
		case t.Type == "custom" && t.Custom != nil:
			out = append(out, llm.Tool{Name: t.Custom.Name, Description: t.Custom.Description,
				Parameters: t.Custom.Format.params(), Kind: "custom"})
		default:
			name := t.Name
			if name == "" {
				name = t.Type
			}
			out = append(out, llm.Tool{Name: name, Kind: t.Type})
		}
	}
	return out
}

// decodeChatTools decodes a tools value alone, as decodeChatRequest decodes it.
func decodeChatTools(raw []byte) (any, error) {
	var tools []chatTool
	if err := unmarshal(raw, &tools); err != nil {
		return nil, err
	}
	return chatTools(tools), nil
}

// chatFunction is a function definition (tools[].function, legacy functions[]) or a call to one
// (tool_calls[].function, legacy function_call); the two share a name.
type chatFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  jsontext.Value `json:"parameters"`
	Arguments   string         `json:"arguments"`
}

func (f chatFunction) tool() llm.Tool {
	return llm.Tool{Name: f.Name, Description: f.Description, Parameters: f.Parameters, Kind: "function"}
}

type chatToolCall struct {
	Index    int             `json:"index"`
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function chatFunction    `json:"function"`
	Custom   *chatCustomCall `json:"custom"`
}

// chatCustomCall is a call to a custom tool, whose input is free text rather than JSON.
type chatCustomCall struct {
	Name  string `json:"name"`
	Input string `json:"input"`
}

// name and args are the call's tool name and input, whether it calls a function or a custom
// (free-text input) tool.
func (c chatToolCall) name() string {
	if c.Custom != nil {
		return c.Custom.Name
	}
	return c.Function.Name
}

func (c chatToolCall) args() string {
	if c.Custom != nil {
		return c.Custom.Input
	}
	return c.Function.Arguments
}

// chatMessage is a request message, a response message, or a stream delta.
type chatMessage struct {
	Role             string         `json:"role"`
	Content          chatContent    `json:"content"`
	Name             string         `json:"name"`
	ToolCallID       string         `json:"tool_call_id"`
	ToolCalls        []chatToolCall `json:"tool_calls"`
	FunctionCall     *chatFunction  `json:"function_call"`
	ReasoningContent string         `json:"reasoning_content"`
	Refusal          string         `json:"refusal"`
}

func (m chatMessage) decode() llm.Message {
	content := []llm.Part(m.Content)
	switch m.Role {
	case "system", "developer":
		return llm.Message{Role: llm.RoleSystem, Parts: content}
	case "assistant":
		return llm.Message{Role: llm.RoleAssistant, Parts: m.assistantParts(content)}
	case "tool":
		return llm.Message{Role: llm.RoleTool, Parts: []llm.Part{resultPart(m.ToolCallID, content, false)}}
	case "function":
		// Legacy function results name the function, not a call.
		return llm.Message{Role: llm.RoleTool, Parts: []llm.Part{resultPart(m.Name, content, false)}}
	}
	return llm.Message{Role: llm.RoleUser, Parts: content}
}

// assistantParts orders an assistant turn as the model produced it: reasoning, text, then calls.
func (m chatMessage) assistantParts(content []llm.Part) []llm.Part {
	if m.ReasoningContent == "" && m.Refusal == "" && len(m.ToolCalls) == 0 && m.FunctionCall == nil {
		return content
	}
	parts := appendText(nil, llm.PartReasoning, m.ReasoningContent)
	parts = append(parts, content...)
	parts = appendText(parts, llm.PartText, m.Refusal)
	for _, c := range m.ToolCalls {
		parts = append(parts, callPart(c.ID, c.name(), c.args()))
	}
	if m.FunctionCall != nil {
		parts = append(parts, callPart("", m.FunctionCall.Name, m.FunctionCall.Arguments))
	}
	return parts
}

func (openAIChat) decode(raw []byte, msgs []llm.Message) ([]llm.Message, bool, string, error) {
	var m chatMessage
	if err := unmarshal(raw, &m); err != nil {
		return nil, false, "", err
	}
	return append(msgs, m.decode()), true, "", nil
}

// measure mirrors chatMessage's decoding and decode.
func (openAIChat) measure(sk *skimmer, raw []byte) bool {
	s := rawScan{b: raw}
	var (
		role                              []byte
		content, calls                    partSum
		reasoningLen, refusalLen          int
		contentSeen, callsSeen, fcallSeen bool
		fcall                             bool
		fcallName, fcallArgs              int
	)
	idLo := len(sk.ids)
	if !s.isNull() && !s.object(func(name []byte) bool {
		var n int
		switch string(name) {
		case "role":
			return s.strVal(&role)
		case "content":
			ok := !contentSeen && sk.chatContent(&s, &content)
			contentSeen = true
			return ok
		case "name", "tool_call_id":
			return s.strLen(&n)
		case "tool_calls":
			ok := !callsSeen && sk.chatToolCalls(&s, &calls)
			callsSeen = true
			return ok
		case "function_call":
			if fcallSeen {
				return false
			}
			fcallSeen = true
			if s.isNull() {
				return true
			}
			fcall = true
			return chatFunctionLen(&s, &fcallName, &fcallArgs)
		case "reasoning_content":
			return s.strLen(&reasoningLen)
		case "refusal":
			return s.strLen(&refusalLen)
		}
		s.skip()
		return true
	}) {
		return false
	}
	var ps partSum
	r := llm.RoleUser
	switch string(role) {
	case "system", "developer":
		r, ps = llm.RoleSystem, content
	case "assistant":
		r = llm.RoleAssistant
		ps.addReasoning(reasoningLen)
		ps.addParts(&content)
		ps.addText(refusalLen)
		ps.addParts(&calls)
		if fcall {
			ps.addCall(fcallName, fcallArgs)
			sk.addID(nil)
		}
	case "tool", "function":
		r = llm.RoleTool
		ps.addResult(&content)
	default:
		ps = content
	}
	if r != llm.RoleAssistant {
		sk.ids = sk.ids[:idLo]
	}
	sk.push(r, &ps, idLo)
	return true
}

// chatToolCalls measures tool_calls, recording the calls' IDs.
func (sk *skimmer) chatToolCalls(s *rawScan, calls *partSum) bool {
	if s.isNull() {
		return true
	}
	if s.peek() != '[' {
		return false
	}
	s.open()
	for s.elem() {
		var (
			id                                   []byte
			fnName, fnArgs, customName, customIn int
			fnSeen, customSeen, custom           bool
		)
		if !s.isNull() && !s.object(func(name []byte) bool {
			var n int
			switch string(name) {
			case "index":
				return s.intVal()
			case "id":
				return s.strVal(&id)
			case "type":
				return s.strLen(&n)
			case "function":
				if fnSeen {
					return false
				}
				fnSeen = true
				return s.isNull() || chatFunctionLen(s, &fnName, &fnArgs)
			case "custom":
				if customSeen {
					return false
				}
				customSeen = true
				if s.isNull() {
					return true
				}
				custom = true
				return s.object(func(name []byte) bool {
					switch string(name) {
					case "name":
						return s.strLen(&customName)
					case "input":
						return s.strLen(&customIn)
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
		if custom {
			fnName, fnArgs = customName, customIn
		}
		calls.addCall(fnName, fnArgs)
		sk.addID(id)
	}
	return true
}

// chatFunctionLen measures a chatFunction object's name and arguments.
func chatFunctionLen(s *rawScan, nameLen, argsLen *int) bool {
	return s.object(func(name []byte) bool {
		var n int
		switch string(name) {
		case "name":
			return s.strLen(nameLen)
		case "description":
			return s.strLen(&n)
		case "arguments":
			return s.strLen(argsLen)
		}
		s.skip() // parameters, a raw value, among them
		return true
	})
}

// chatContent measures what chatContent decodes into ps.
func (sk *skimmer) chatContent(s *rawScan, ps *partSum) bool {
	switch s.peek() {
	case 'n':
		s.skip()
		return true
	case '"':
		ps.addText(s.text())
		return true
	case '[':
	default:
		return false
	}
	s.open()
	for s.elem() {
		if s.isNull() {
			continue // a zero part: no text
		}
		var (
			typ                         []byte
			textLen, refusalLen         int
			image, audio, file          bool
			imageSeen, audioSeen, fSeen bool
		)
		// once reads a pointer member, which a repeat would merge into: set reports it non-null.
		once := func(seen, set *bool, obj func(name []byte) bool) bool {
			if *seen {
				return false
			}
			*seen = true
			if s.isNull() {
				return true
			}
			*set = true
			return s.object(obj)
		}
		strs := func(name []byte) bool {
			var n int
			return s.strLen(&n)
		}
		if !s.object(func(name []byte) bool {
			switch string(name) {
			case "type":
				return s.strVal(&typ)
			case "text":
				return s.strLen(&textLen)
			case "refusal":
				return s.strLen(&refusalLen)
			case "image_url":
				if !imageSeen && s.peek() == '"' {
					imageSeen, image = true, true
					s.skip()
					return true
				}
				return once(&imageSeen, &image, func(name []byte) bool {
					if string(name) == "url" {
						return strs(name)
					}
					s.skip()
					return true
				})
			case "input_audio":
				return once(&audioSeen, &audio, func(name []byte) bool {
					if string(name) == "data" || string(name) == "format" {
						return strs(name)
					}
					s.skip()
					return true
				})
			case "file":
				return once(&fSeen, &file, func(name []byte) bool {
					if string(name) == "file_data" || string(name) == "file_id" {
						return strs(name)
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
		switch {
		case string(typ) == "image_url" && image, string(typ) == "input_audio" && audio, string(typ) == "file" && file:
			ps.media++
		case string(typ) == "refusal":
			ps.addText(refusalLen)
		default:
			ps.addText(textLen)
		}
	}
	return true
}

// chatContent is a content field, null, a string or an array of parts, decoded into parts.
type chatContent []llm.Part

func (c *chatContent) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	switch dec.PeekKind() {
	case 'n':
		_, err := dec.ReadToken()
		return err
	case '"':
		s, err := readString(dec)
		*c = appendText(nil, llm.PartText, s)
		return err
	case '[':
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		var parts []llm.Part
		for dec.PeekKind() != ']' {
			var p chatContentPart
			if err := json.UnmarshalDecode(dec, &p); err != nil {
				return err
			}
			parts = p.append(parts)
		}
		*c = parts
		_, err := dec.ReadToken()
		return err
	}
	return fmt.Errorf("content is neither a string nor an array")
}

type chatContentPart struct {
	Type       string        `json:"type"`
	Text       string        `json:"text"`
	Refusal    string        `json:"refusal"`
	ImageURL   *chatImageURL `json:"image_url"`
	InputAudio *struct {
		Data   string `json:"data"`
		Format string `json:"format"`
	} `json:"input_audio"`
	File *struct {
		FileData string `json:"file_data"`
		FileID   string `json:"file_id"`
	} `json:"file"`
}

// chatImageURL is image_url as an object or, as some OpenAI-compatible clients send it, a bare
// string.
type chatImageURL struct {
	URL string `json:"url"`
}

func (u *chatImageURL) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	if dec.PeekKind() == '"' {
		var err error
		u.URL, err = readString(dec)
		return err
	}
	type plain chatImageURL
	return json.UnmarshalDecode(dec, (*plain)(u))
}

func (p chatContentPart) append(parts []llm.Part) []llm.Part {
	switch p.Type {
	case "image_url":
		if p.ImageURL != nil {
			return append(parts, mediaPart(urlMedia("image", p.ImageURL.URL)))
		}
	case "input_audio":
		if p.InputAudio != nil {
			return append(parts, mediaPart(&llm.Media{Type: "audio", MIME: "audio/" + p.InputAudio.Format, Data: p.InputAudio.Data}))
		}
	case "file":
		if p.File != nil {
			if p.File.FileData != "" {
				return append(parts, mediaPart(inlineMedia("file", p.File.FileData)))
			}
			return append(parts, mediaPart(fileMedia("file", p.File.FileID)))
		}
	case "refusal":
		return appendText(parts, llm.PartText, p.Refusal)
	}
	return appendText(parts, llm.PartText, p.Text)
}

type chatUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u chatUsage) usage() llm.Usage {
	return llm.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, CacheRead: u.PromptTokensDetails.CachedTokens}
}

// chatResponse is a completion or a stream chunk.
type chatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int         `json:"index"`
		Message      chatMessage `json:"message"`
		Delta        chatMessage `json:"delta"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
	Error *apiError  `json:"error"`
}

func (openAIChat) DecodeResponse(body []byte) (*llm.Response, error) {
	decode := decodeChatResponse
	if isSSE(body) {
		decode = decodeChatStream
	}
	resp, err := decode(body)
	if err != nil {
		return nil, fmt.Errorf("codec: openai chat response: %w", err)
	}
	return resp, nil
}

// decodeChatResponse decodes the first choice; gateways and agents ask for one (n=1).
func decodeChatResponse(body []byte) (*llm.Response, error) {
	var r chatResponse
	if err := unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Error != nil {
		return nil, r.Error.err()
	}
	resp := &llm.Response{ID: r.ID, Model: r.Model}
	if r.Usage != nil {
		resp.Usage = r.Usage.usage()
	}
	if len(r.Choices) > 0 {
		c := r.Choices[0]
		resp.Parts = c.Message.assistantParts(c.Message.Content)
		resp.StopReason = c.FinishReason
	}
	return resp, nil
}

// chatStream is a Chat Completions reply being assembled from its chunks.
type chatStream struct {
	resp                     *llm.Response
	reasoning, text, refusal []byte
	calls                    map[int]*chatStreamCall
	legacy                   *chatStreamCall
	ops                      []chatOp // the fast path's reading of the current chunk
}

// chatStreamCall is a tool call being assembled from its fragments.
type chatStreamCall struct {
	id, name    string
	args        []byte
	custom      bool
	customName  string
	customInput []byte
}

func (st *chatStream) call(index int) *chatStreamCall {
	c, ok := st.calls[index]
	if !ok {
		c = &chatStreamCall{}
		st.calls[index] = c
	}
	return c
}

// merge folds a tool call fragment in: the first fragment names the call, later ones carry
// argument text.
func (c *chatStreamCall) merge(f *chatToolCall) {
	if f.ID != "" {
		c.id = f.ID
	}
	if f.Function.Name != "" {
		c.name = f.Function.Name
	}
	c.args = append(c.args, f.Function.Arguments...)
	if f.Custom != nil {
		c.custom = true
		if f.Custom.Name != "" {
			c.customName = f.Custom.Name
		}
		c.customInput = append(c.customInput, f.Custom.Input...)
	}
}

// apply folds in a chunk the decoder decoded.
func (st *chatStream) apply(chunk *chatResponse) error {
	if chunk.Error != nil {
		return chunk.Error.err()
	}
	if chunk.ID != "" {
		st.resp.ID = chunk.ID
	}
	if chunk.Model != "" {
		st.resp.Model = chunk.Model
	}
	if chunk.Usage != nil {
		st.resp.Usage = chunk.Usage.usage()
	}
	for _, c := range chunk.Choices {
		if c.Index != 0 {
			continue
		}
		d := c.Delta
		st.reasoning = append(st.reasoning, d.ReasoningContent...)
		st.refusal = append(st.refusal, d.Refusal...)
		for _, p := range d.Content {
			if p.Kind == llm.PartText {
				st.text = append(st.text, p.Text...)
			}
		}
		for i := range d.ToolCalls {
			st.call(d.ToolCalls[i].Index).merge(&d.ToolCalls[i])
		}
		if d.FunctionCall != nil {
			if st.legacy == nil {
				st.legacy = &chatStreamCall{}
			}
			if d.FunctionCall.Name != "" {
				st.legacy.name = d.FunctionCall.Name
			}
			st.legacy.args = append(st.legacy.args, d.FunctionCall.Arguments...)
		}
		if c.FinishReason != "" {
			st.resp.StopReason = c.FinishReason
		}
	}
	return nil
}

// chatOp is one effect of a chunk, as the fast path reads it: q is a string as in the input.
type chatOp struct {
	kind  chatOpKind
	index int
	s     jsonString
	usage llm.Usage
}

type chatOpKind uint8

const (
	chatID chatOpKind = iota + 1 // zero is no op
	chatModel
	chatUsageOp
	chatReasoning
	chatRefusal
	chatText
	chatCallTouch // a tool call fragment at index; the ops after it carry its fields
	chatCallID
	chatCallName
	chatCallArgs
	chatFinish
)

// readChatChunk is the fast path: it reads a chunk into st.ops, reporting false for any shape
// chatResponse's decoding does not reduce to what it reads (an error, content parts, a legacy
// function call, a custom tool call, a choice's message, a repeated member, a member name that
// could match case-insensitively, a value of an unexpected type).
func (st *chatStream) readChatChunk(data []byte) bool {
	st.ops = st.ops[:0]
	s := newJSONReader(data)
	var seen uint8
	once := func(bit uint8) bool {
		if seen&bit != 0 {
			return false
		}
		seen |= bit
		return true
	}
	// str adds an op of kind for a string member; null adds none.
	str := func(kind chatOpKind, index int) bool {
		if s.isNull() {
			return true
		}
		q, ok := s.string()
		if ok {
			st.ops = append(st.ops, chatOp{kind: kind, index: index, s: q})
		}
		return ok
	}
	return s.object(func(name []byte) bool {
		switch string(name) {
		case "id":
			return once(1) && str(chatID, 0)
		case "model":
			return once(2) && str(chatModel, 0)
		case "usage":
			return once(4) && st.readChatUsage(s)
		case "choices":
			return once(8) && (s.isNull() || s.array(func() bool { return s.isNull() || st.readChatChoice(s) }))
		case "error":
			return once(16) && s.isNull()
		}
		return s.skip()
	}, false) && s.done()
}

func (st *chatStream) readChatUsage(s *jsonReader) bool {
	if s.isNull() {
		return true
	}
	var u llm.Usage
	var seen uint8
	ok := s.object(func(name []byte) bool {
		switch string(name) {
		case "prompt_tokens":
			return seen&1 == 0 && intOrNull(s, &u.InputTokens, &seen, 1)
		case "completion_tokens":
			return seen&2 == 0 && intOrNull(s, &u.OutputTokens, &seen, 2)
		case "prompt_tokens_details":
			if seen&4 != 0 {
				return false
			}
			seen |= 4
			if s.isNull() {
				return true
			}
			var inner uint8
			return s.object(func(name []byte) bool {
				if string(name) == "cached_tokens" {
					return inner == 0 && intOrNull(s, &u.CacheRead, &inner, 1)
				}
				return s.skip()
			}, false)
		}
		return s.skip()
	}, false)
	st.ops = append(st.ops, chatOp{kind: chatUsageOp, usage: u})
	return ok
}

// intOrNull reads an integer member into *n, null leaving it 0, and marks bit seen.
func intOrNull(s *jsonReader, n *int64, seen *uint8, bit uint8) bool {
	*seen |= bit
	if s.isNull() {
		return true
	}
	v, ok := s.intValue()
	*n = int64(v)
	return ok
}

func (st *chatStream) readChatChoice(s *jsonReader) bool {
	index, mark := 0, len(st.ops)
	var seen uint8
	ok := s.object(func(name []byte) bool {
		switch string(name) {
		case "index":
			if seen&1 != 0 {
				return false
			}
			seen |= 1
			if s.isNull() {
				return true
			}
			var ok bool
			index, ok = s.intValue()
			return ok
		case "delta":
			if seen&2 != 0 {
				return false
			}
			seen |= 2
			return s.isNull() || st.readChatDelta(s)
		case "finish_reason":
			if seen&4 != 0 {
				return false
			}
			seen |= 4
			if s.isNull() {
				return true
			}
			q, ok := s.string()
			if ok {
				st.ops = append(st.ops, chatOp{kind: chatFinish, s: q})
			}
			return ok
		case "message":
			return false
		}
		return s.skip()
	}, false)
	if index != 0 {
		st.ops = st.ops[:mark] // another choice: read for its validity, not applied
	}
	return ok
}

func (st *chatStream) readChatDelta(s *jsonReader) bool {
	var seen uint8
	once := func(bit uint8) bool {
		if seen&bit != 0 {
			return false
		}
		seen |= bit
		return true
	}
	str := func(kind chatOpKind) bool {
		if s.isNull() {
			return true
		}
		q, ok := s.string()
		if ok {
			st.ops = append(st.ops, chatOp{kind: kind, s: q})
		}
		return ok
	}
	return s.object(func(name []byte) bool {
		switch string(name) {
		case "content":
			return once(1) && str(chatText)
		case "reasoning_content":
			return once(2) && str(chatReasoning)
		case "refusal":
			return once(4) && str(chatRefusal)
		case "role", "name", "tool_call_id":
			if s.isNull() {
				return true
			}
			_, ok := s.quoted()
			return ok
		case "tool_calls":
			return once(8) && (s.isNull() || s.array(func() bool { return st.readChatCall(s) }))
		case "function_call":
			return false
		}
		return s.skip()
	}, false)
}

func (st *chatStream) readChatCall(s *jsonReader) bool {
	touch := len(st.ops)
	st.ops = append(st.ops, chatOp{kind: chatCallTouch})
	if s.isNull() {
		return true
	}
	var seen uint8
	str := func(bit uint8, kind chatOpKind) bool {
		if seen&bit != 0 {
			return false
		}
		seen |= bit
		if s.isNull() {
			return true
		}
		q, ok := s.string()
		if ok && kind != 0 {
			st.ops = append(st.ops, chatOp{kind: kind, s: q})
		}
		return ok
	}
	return s.object(func(name []byte) bool {
		switch string(name) {
		case "index":
			if seen&1 != 0 {
				return false
			}
			seen |= 1
			if s.isNull() {
				return true
			}
			var ok bool
			st.ops[touch].index, ok = s.intValue()
			return ok
		case "id":
			return str(2, chatCallID)
		case "type":
			return str(4, 0)
		case "function":
			if seen&8 != 0 {
				return false
			}
			seen |= 8
			if s.isNull() {
				return true
			}
			var fn uint8
			return s.object(func(name []byte) bool {
				var bit uint8
				var kind chatOpKind
				switch string(name) {
				case "name":
					bit, kind = 1, chatCallName
				case "arguments":
					bit, kind = 2, chatCallArgs
				case "description":
					bit = 4
				default:
					return s.skip() // parameters, a raw value, among them
				}
				if fn&bit != 0 {
					return false
				}
				fn |= bit
				if s.isNull() {
					return true
				}
				q, ok := s.string()
				if ok && kind != 0 {
					st.ops = append(st.ops, chatOp{kind: kind, s: q})
				}
				return ok
			}, false)
		case "custom":
			return false
		}
		return s.skip()
	}, false)
}

// applyOps folds in a chunk the fast path read, as apply would the decoded chunk.
func (st *chatStream) applyOps() {
	var call *chatStreamCall
	for i := range st.ops {
		op := &st.ops[i]
		switch op.kind {
		case chatID:
			if !op.s.empty() {
				op.s.set(&st.resp.ID)
			}
		case chatModel:
			if !op.s.empty() {
				op.s.set(&st.resp.Model)
			}
		case chatUsageOp:
			st.resp.Usage = op.usage
		case chatReasoning:
			st.reasoning = op.s.appendTo(st.reasoning)
		case chatRefusal:
			st.refusal = op.s.appendTo(st.refusal)
		case chatText:
			st.text = op.s.appendTo(st.text)
		case chatCallTouch:
			call = st.call(op.index)
		case chatCallID:
			if !op.s.empty() {
				op.s.set(&call.id)
			}
		case chatCallName:
			if !op.s.empty() {
				op.s.set(&call.name)
			}
		case chatCallArgs:
			call.args = op.s.appendTo(call.args)
		case chatFinish:
			if !op.s.empty() {
				op.s.set(&st.resp.StopReason)
			}
		}
	}
}

func decodeChatStream(body []byte) (*llm.Response, error) {
	st := &chatStream{resp: &llm.Response{}, calls: map[int]*chatStreamCall{}}
	for s := newSSEScanner(body); s.next(); {
		if bytes.Equal(bytes.TrimSpace(s.data), []byte("[DONE]")) {
			break
		}
		if st.readChatChunk(s.data) {
			st.applyOps()
			continue
		}
		var chunk chatResponse
		if err := unmarshal(s.data, &chunk); err != nil {
			return nil, fmt.Errorf("chunk: %w", err)
		}
		if err := st.apply(&chunk); err != nil {
			return nil, err
		}
	}
	m := chatMessage{ReasoningContent: bytesString(st.reasoning), Refusal: bytesString(st.refusal)}
	if st.legacy != nil {
		m.FunctionCall = &chatFunction{Name: st.legacy.name, Arguments: bytesString(st.legacy.args)}
	}
	for _, i := range slices.Sorted(maps.Keys(st.calls)) {
		c := st.calls[i]
		tc := chatToolCall{Index: i, ID: c.id, Function: chatFunction{Name: c.name, Arguments: bytesString(c.args)}}
		if c.custom {
			tc.Custom = &chatCustomCall{Name: c.customName, Input: bytesString(c.customInput)}
		}
		m.ToolCalls = append(m.ToolCalls, tc)
	}
	resp := st.resp
	resp.Parts = m.assistantParts(appendText(nil, llm.PartText, bytesString(st.text)))
	return resp, nil
}
