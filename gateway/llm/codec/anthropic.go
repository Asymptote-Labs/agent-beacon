package codec

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// anthropic decodes Anthropic Messages.
type anthropic struct{}

func (anthropic) Protocol() llm.Protocol { return llm.AnthropicMessages }

func (anthropic) ErrorBody(status int, message string) []byte {
	return anthropicErrorBody(status, message)
}

// anthropicRequestFields are the top-level request fields the codec maps, plus the scalar knobs
// it ignores because they shape sampling or billing, not what the model reads.
var anthropicRequestFields = set(
	"model", "stream", "system", "messages", "tools", "tool_choice", "output_format", "response_format",
	"max_tokens", "temperature", "top_p", "top_k", "stop_sequences", "metadata", "thinking",
	"service_tier", "container", "context_management", "anthropic_version", "anthropic_beta",
)

func (a anthropic) DecodeRequest(body []byte) (*llm.Request, error) { return a.request(body, nil) }

func (anthropic) request(body []byte, skim *skimInput) (*llm.Request, error) {
	req, err := decodeAnthropicRequest(body, skim)
	if err != nil {
		return nil, fmt.Errorf("codec: anthropic request: %w", err)
	}
	return req, nil
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content anthropicContent `json:"content"`
}

type anthropicTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema jsontext.Value `json:"input_schema"`
}

func decodeAnthropicRequest(body []byte, skim *skimInput) (*llm.Request, error) {
	req := &llm.Request{}
	var (
		system anthropicContent
		tools  []llm.Tool
	)
	formats := newFormats("tool_choice", "output_format", "response_format")
	unknown, err := decodeRequestObject(body, anthropicRequestFields, func(dec *jsontext.Decoder, name []byte) (bool, error) {
		switch string(name) {
		case "model":
			return true, json.UnmarshalDecode(dec, &req.Model)
		case "stream":
			return true, json.UnmarshalDecode(dec, &req.Stream)
		case "system":
			if v, ok, err := skim.member(dec, "system"); ok {
				system, _ = v.([]llm.Part)
				return true, err
			}
			system = nil
			return true, json.UnmarshalDecode(dec, &system)
		case "messages":
			if skim != nil {
				var err error
				req.Messages, req.Elided, _, err = skim.messages(dec, anthropic{})
				return true, err
			}
			req.Messages = nil
			var m anthropicMessage
			return true, readArray(dec, func() error {
				m = anthropicMessage{}
				if err := json.UnmarshalDecode(dec, &m); err != nil {
					return err
				}
				req.Messages = append(req.Messages, m.message())
				return nil
			})
		case "tools":
			if v, ok, err := skim.member(dec, "tools"); ok {
				tools, _ = v.([]llm.Tool)
				return true, err
			}
			var ts []anthropicTool
			err := json.UnmarshalDecode(dec, &ts)
			tools = anthropicTools(ts)
			return true, err
		}
		return formats.read(dec, name)
	})
	if err != nil {
		return nil, err
	}
	req.Unknown, req.Format = unknown, formats.list()
	if len(system) > 0 {
		req.Messages = slices.Insert(req.Messages, 0, llm.Message{Role: llm.RoleSystem, Parts: system})
	}
	req.Tools = tools
	return req, nil
}

// anthropicTools maps tool definitions; none map to nil.
func anthropicTools(tools []anthropicTool) []llm.Tool {
	var out []llm.Tool
	for _, t := range tools {
		kind := t.Type
		if kind == "" || kind == "custom" {
			kind = "function"
		}
		out = append(out, llm.Tool{Name: t.Name, Description: t.Description, Parameters: t.InputSchema, Kind: kind})
	}
	return out
}

// decodeAnthropicTools decodes a tools value alone, as decodeAnthropicRequest decodes it.
func decodeAnthropicTools(raw []byte) (any, error) {
	var tools []anthropicTool
	if err := unmarshal(raw, &tools); err != nil {
		return nil, err
	}
	return anthropicTools(tools), nil
}

// decodeAnthropicSystem decodes a system value alone, as decodeAnthropicRequest decodes it.
func decodeAnthropicSystem(raw []byte) (any, error) {
	var system anthropicContent
	if err := unmarshal(raw, &system); err != nil {
		return nil, err
	}
	return []llm.Part(system), nil
}

// anthropicRole maps a message role. Claude Code sends system messages inside messages, which
// the API documents only as user and assistant.
func anthropicRole(role string) llm.Role {
	switch role {
	case "assistant":
		return llm.RoleAssistant
	case "system":
		return llm.RoleSystem
	}
	return llm.RoleUser
}

func (m *anthropicMessage) message() llm.Message {
	return llm.Message{Role: anthropicRole(m.Role), Parts: m.Content}
}

func (anthropic) decode(raw []byte, msgs []llm.Message) ([]llm.Message, bool, string, error) {
	var m anthropicMessage
	if err := unmarshal(raw, &m); err != nil {
		return nil, false, "", err
	}
	return append(msgs, m.message()), true, "", nil
}

// measure mirrors anthropicMessage's decoding.
func (anthropic) measure(sk *skimmer, raw []byte) bool {
	s := rawScan{b: raw}
	var (
		role    []byte
		content partSum
		seen    bool
	)
	idLo := len(sk.ids)
	switch s.peek() {
	case 'n':
	case '{':
		s.open()
		for {
			name, ok := s.member()
			if !ok {
				break
			}
			if !plainName(name) {
				return false
			}
			switch string(name) {
			case "role":
				ok = s.strVal(&role)
			case "content":
				ok = !seen && sk.anthropicContent(&s, &content, true)
				seen = true
			default:
				s.skip()
			}
			if !ok {
				return false
			}
		}
	default:
		return false
	}
	sk.push(anthropicRole(string(role)), &content, idLo)
	return true
}

// anthropicContent measures what anthropicContent decodes into ps; top records the IDs of the
// calls of a message's own content.
func (sk *skimmer) anthropicContent(s *rawScan, ps *partSum, top bool) bool {
	switch s.peek() {
	case 'n':
		s.skip()
		return true
	case '"':
		ps.addText(s.text())
		return true
	case '[':
		s.open()
		for s.elem() {
			if !sk.anthropicBlock(s, ps, top) {
				return false
			}
		}
		return true
	}
	return false
}

// anthropicBlock measures what anthropicBlock decodes and appendParts appends. It gives up on
// a repeated type or content, whose decoding depends on member order, on documents and server
// tool results, whose content it does not mirror.
func (sk *skimmer) anthropicBlock(s *rawScan, ps *partSum, top bool) bool {
	if s.peek() != '{' {
		return false
	}
	s.open()
	var (
		typ, id                                 []byte
		typSeen, contentSeen                    bool
		textLen, thinkingLen, nameLen, inputLen int
		content                                 partSum
		early                                   []byte // content before type
	)
	for {
		name, ok := s.member()
		if !ok {
			break
		}
		if !plainName(name) {
			return false
		}
		switch string(name) {
		case "type":
			typ = nil
			ok = !typSeen && s.strVal(&typ)
			typSeen = true
		case "text":
			textLen = 0
			ok = s.strLen(&textLen)
		case "thinking":
			thinkingLen = 0
			ok = s.strLen(&thinkingLen)
		case "id":
			id = nil
			ok = s.strVal(&id)
		case "name":
			nameLen = 0
			ok = s.strLen(&nameLen)
		case "tool_use_id":
			var n int
			ok = s.strLen(&n)
		case "is_error":
			ok = s.boolVal()
		case "input":
			inputLen = s.compactValLen()
		case "source":
			ok = sk.anthropicSource(s)
		case "content":
			if contentSeen {
				return false
			}
			contentSeen = true
			if len(typ) == 0 {
				early = s.value()
				break
			}
			ok = sk.anthropicBlockContent(s, typ, &content)
		default:
			s.skip()
		}
		if !ok {
			return false
		}
	}
	if early != nil {
		es := rawScan{b: early}
		if !sk.anthropicBlockContent(&es, typ, &content) {
			return false
		}
	}
	switch string(typ) {
	case "text":
		ps.addText(textLen)
	case "thinking":
		ps.addReasoning(thinkingLen)
	case "redacted_thinking":
	case "image":
		ps.media++
	case "document":
		return false
	case "tool_use", "server_tool_use", "mcp_tool_use":
		ps.addCall(nameLen, inputLen)
		if top {
			sk.addID(id)
		}
	case "tool_result":
		ps.addResult(&content)
	default:
		if bytes.HasSuffix(typ, []byte("_tool_result")) {
			return false
		}
		ps.addText(textLen)
	}
	return true
}

// anthropicBlockContent measures decodeContent for a block of type typ.
func (sk *skimmer) anthropicBlockContent(s *rawScan, typ []byte, content *partSum) bool {
	switch {
	case string(typ) == "tool_result":
		*content = partSum{}
		return sk.anthropicContent(s, content, false)
	case bytes.HasSuffix(typ, []byte("_tool_result")):
		return false
	}
	s.skip()
	return true
}

// anthropicSource checks that a source decodes; only documents, which measurers give up on,
// read it.
func (sk *skimmer) anthropicSource(s *rawScan) bool {
	switch s.peek() {
	case 'n':
		s.skip()
		return true
	case '{':
	default:
		return false
	}
	s.open()
	for {
		name, ok := s.member()
		if !ok {
			return true
		}
		if !plainName(name) {
			return false
		}
		var n int
		switch string(name) {
		case "type", "media_type", "data", "url", "file_id":
			ok = s.strLen(&n)
		case "content":
			var c partSum
			ok = sk.anthropicContent(s, &c, false)
		default:
			s.skip()
		}
		if !ok {
			return false
		}
	}
}

// anthropicContent is a content field, a string or an array of blocks, decoded into parts.
type anthropicContent []llm.Part

func (c *anthropicContent) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
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
			var b anthropicBlock
			if err := b.decode(dec); err != nil {
				return err
			}
			parts = b.appendParts(parts)
		}
		*c = parts
		_, err := dec.ReadToken()
		return err
	}
	return fmt.Errorf("content is neither a string nor an array")
}

// anthropicBlock is any content block; fields are shared across block types. Content holds a
// tool result's parts, decoded as the block's type requires.
type anthropicBlock struct {
	Type      string
	Text      string
	Thinking  string
	ID        string
	Name      string
	Input     string // compact JSON
	ToolUseID string
	IsError   bool
	Content   []llm.Part
	Source    *anthropicSource
}

type anthropicSource struct {
	Type      string           `json:"type"`
	MediaType string           `json:"media_type"`
	Data      string           `json:"data"`
	URL       string           `json:"url"`
	FileID    string           `json:"file_id"`
	Content   anthropicContent `json:"content"`
}

func (b *anthropicBlock) UnmarshalJSONFrom(dec *jsontext.Decoder) error { return b.decode(dec) }

// decode reads a block by hand because how its content decodes depends on its type. Clients put
// type first; content that precedes it is decoded once the type is known, from the input it
// still aliases (see unmarshal).
func (b *anthropicBlock) decode(dec *jsontext.Decoder) error {
	var early jsontext.Value
	err := readObject(dec, func(name []byte) error {
		var err error
		switch string(foldName(name)) {
		case "type":
			b.Type, err = readString(dec)
		case "text":
			b.Text, err = readString(dec)
		case "thinking":
			b.Thinking, err = readString(dec)
		case "id":
			b.ID, err = readString(dec)
		case "name":
			b.Name, err = readString(dec)
		case "tool_use_id":
			b.ToolUseID, err = readString(dec)
		case "is_error":
			err = json.UnmarshalDecode(dec, &b.IsError)
		case "input":
			b.Input, err = readCompact(dec)
		case "source":
			err = json.UnmarshalDecode(dec, &b.Source)
		case "content":
			if b.Type != "" {
				return b.decodeContent(dec)
			}
			early, err = dec.ReadValue()
		default:
			err = dec.SkipValue()
		}
		return err
	})
	if err != nil || early == nil {
		return err
	}
	f := decodeFunc(b.decodeContent)
	return unmarshal(early, &f)
}

// decodeContent decodes a tool result's content; other blocks' content is not read by the model
// as content (a search result's is carried by its own fields) and is skipped.
func (b *anthropicBlock) decodeContent(dec *jsontext.Decoder) error {
	switch {
	case b.Type == "tool_result":
		var c anthropicContent
		if err := c.UnmarshalJSONFrom(dec); err != nil {
			return fmt.Errorf("tool_result %s: %w", b.ToolUseID, err)
		}
		b.Content = c
	case strings.HasSuffix(b.Type, "_tool_result"):
		v, err := dec.ReadValue()
		if err != nil {
			return err
		}
		if b.Content, err = serverResultText(v); err != nil {
			return fmt.Errorf("%s %s: %w", b.Type, b.ToolUseID, err)
		}
	default:
		return dec.SkipValue()
	}
	return nil
}

// appendParts classifies the block by its type, whatever role carries it.
func (b *anthropicBlock) appendParts(parts []llm.Part) []llm.Part {
	switch b.Type {
	case "text":
		return appendText(parts, llm.PartText, b.Text)
	case "thinking":
		return appendText(parts, llm.PartReasoning, b.Thinking)
	case "redacted_thinking":
		// Encrypted reasoning the client cannot read and the model reads as opaque state.
		return parts
	case "image":
		return append(parts, mediaPart(anthropicMedia("image", b.Source)))
	case "document":
		// A plain-text or content document is read by the model as text, so it is text here too.
		switch {
		case b.Source != nil && b.Source.Type == "text":
			return appendText(parts, llm.PartText, b.Source.Data)
		case b.Source != nil && b.Source.Type == "content":
			return append(parts, b.Source.Content...)
		}
		return append(parts, mediaPart(anthropicMedia("document", b.Source)))
	case "tool_use", "server_tool_use", "mcp_tool_use":
		return append(parts, callPart(b.ID, b.Name, b.Input))
	}
	if b.Type == "tool_result" || strings.HasSuffix(b.Type, "_tool_result") {
		return append(parts, resultPart(b.ToolUseID, b.Content, b.IsError))
	}
	// A block type this codec predates still puts its text in front of the model.
	return appendText(parts, llm.PartText, b.Text)
}

func anthropicMedia(typ string, s *anthropicSource) *llm.Media {
	m := &llm.Media{Type: typ}
	if s == nil {
		return m
	}
	switch s.Type {
	case "base64":
		m.MIME, m.Data = s.MediaType, s.Data
	case "url":
		m.URL = s.URL
	case "file":
		return fileMedia(typ, s.FileID)
	}
	return m
}

// serverResultFields are the strings of a server tool result (web search, web fetch, code
// execution, MCP) the model reads, in the order they are emitted. Everything else in those
// results is opaque to the model (encrypted_content) or bookkeeping (page_age, error_code).
var serverResultFields = []string{"text", "title", "url", "stdout", "stderr"}

// serverResultText extracts the text parts of a server tool result's content, whose shape varies
// by tool: a string, a list of results, a single result or error object, nested documents.
func serverResultText(v jsontext.Value) ([]llm.Part, error) {
	if v.Kind() == 'n' {
		return nil, nil
	}
	var tree any
	if err := unmarshal(v, &tree); err != nil {
		return nil, err
	}
	return collectText(nil, tree), nil
}

func collectText(parts []llm.Part, v any) []llm.Part {
	switch v := v.(type) {
	case string:
		return appendText(parts, llm.PartText, v)
	case []any:
		for _, e := range v {
			parts = collectText(parts, e)
		}
	case map[string]any:
		for _, k := range serverResultFields {
			if s, ok := v[k].(string); ok {
				parts = appendText(parts, llm.PartText, s)
			}
		}
		// A text document source (web fetch's page) carries its text in data.
		if v["type"] == "text" {
			if s, ok := v["data"].(string); ok {
				parts = appendText(parts, llm.PartText, s)
			}
		}
		keys := make([]string, 0, len(v))
		for k, e := range v {
			switch e.(type) {
			case []any, map[string]any:
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			parts = collectText(parts, v[k])
		}
	}
	return parts
}

type anthropicUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// merge takes the counts u reports. Streams report input counts in message_start and output
// counts in message_delta; a later nonzero count supersedes an earlier one because counts are
// cumulative.
func (u anthropicUsage) merge(into *llm.Usage) {
	for _, f := range []struct {
		dst *int64
		v   int64
	}{
		{&into.InputTokens, u.InputTokens},
		{&into.OutputTokens, u.OutputTokens},
		{&into.CacheRead, u.CacheReadInputTokens},
		{&into.CacheWrite, u.CacheCreationInputTokens},
	} {
		if f.v != 0 {
			*f.dst = f.v
		}
	}
}

func (anthropic) DecodeResponse(body []byte) (*llm.Response, error) {
	decode := decodeAnthropicResponse
	if isSSE(body) {
		decode = decodeAnthropicStream
	}
	resp, err := decode(body)
	if err != nil {
		return nil, fmt.Errorf("codec: anthropic response: %w", err)
	}
	return resp, nil
}

func decodeAnthropicResponse(body []byte) (*llm.Response, error) {
	var r struct {
		Type       string           `json:"type"`
		ID         string           `json:"id"`
		Model      string           `json:"model"`
		Content    anthropicContent `json:"content"`
		StopReason string           `json:"stop_reason"`
		Usage      anthropicUsage   `json:"usage"`
		Error      *apiError        `json:"error"`
	}
	if err := unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Type == "error" && r.Error != nil {
		return nil, r.Error.err()
	}
	resp := &llm.Response{ID: r.ID, Model: r.Model, Parts: r.Content, StopReason: r.StopReason}
	r.Usage.merge(&resp.Usage)
	return resp, nil
}

// anthropicStreamBlock is a content block being assembled from its start event and deltas.
type anthropicStreamBlock struct {
	start anthropicBlock
	text  []byte // text or thinking
	args  []byte // tool input JSON fragments
}

// anthropicEvent is a stream event as the decoder reads one the fast path leaves to it. It is an
// unnamed struct type, as errors decoding an event have always named it.
type anthropicEvent = struct {
	Type    string `json:"type"`
	Message *struct {
		ID    string         `json:"id"`
		Model string         `json:"model"`
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	Index        int             `json:"index"`
	ContentBlock *anthropicBlock `json:"content_block"`
	Delta        struct {
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
	Error *apiError       `json:"error"`
}

// anthropicDelta is what the fast path reads of an event: a content_block_delta's index and its
// delta's strings (q nil when absent), or that the event changes nothing.
type anthropicDelta struct {
	inert                   bool
	index                   int
	text, thinking, partial jsonString
}

// readAnthropicDelta is the fast path: it reads content_block_delta events and events whose type
// the decoder ignores (ping, content_block_stop, message_stop), and reports false for any other,
// or for any shape anthropicEvent's decoding does not reduce to what it reads.
func readAnthropicDelta(data []byte) (d anthropicDelta, ok bool) {
	r := newJSONReader(data)
	var typ []byte
	var seenType, seenIndex, seenDelta bool
	ok = r.object(func(name []byte) bool {
		var ok bool
		switch string(name) {
		case "type":
			if seenType {
				return false
			}
			seenType = true
			typ, ok = r.quoted()
			return ok
		case "index":
			if seenIndex {
				return false
			}
			seenIndex = true
			d.index, ok = r.intValue()
			return ok
		case "delta":
			if seenDelta {
				return false
			}
			seenDelta = true
			return r.object(func(name []byte) bool {
				var dst *jsonString
				switch string(name) {
				case "text":
					dst = &d.text
				case "thinking":
					dst = &d.thinking
				case "partial_json":
					dst = &d.partial
				case "stop_reason":
					_, ok := r.quoted()
					return ok
				default:
					return r.skip()
				}
				if dst.q != nil {
					return false
				}
				*dst, ok = r.string()
				return ok
			}, false)
		case "message", "content_block", "usage", "error":
			return false
		}
		return r.skip()
	}, false)
	if !ok || !r.done() {
		return d, false
	}
	switch string(unquotedType(typ)) {
	case "content_block_delta":
		return d, true
	case "message_start", "content_block_start", "message_delta", "error":
		return d, false
	}
	d.inert = true
	return d, true
}

func decodeAnthropicStream(body []byte) (*llm.Response, error) {
	resp := &llm.Response{}
	blocks := map[int]*anthropicStreamBlock{}
	// Deltas run block by block: the last block looked up is kept at hand.
	lastIndex, last := 0, (*anthropicStreamBlock)(nil)
	for s := newSSEScanner(body); s.next(); {
		if d, ok := readAnthropicDelta(s.data); ok {
			if d.inert {
				continue
			}
			b := last
			if b == nil || lastIndex != d.index {
				if b, ok = blocks[d.index]; !ok {
					return nil, fmt.Errorf("content_block_delta for unstarted block %d", d.index)
				}
				lastIndex, last = d.index, b
			}
			if d.text.q != nil {
				b.text = d.text.appendTo(b.text)
			}
			if d.thinking.q != nil {
				b.text = d.thinking.appendTo(b.text)
			}
			if d.partial.q != nil {
				b.args = d.partial.appendTo(b.args)
			}
			continue
		}
		var e anthropicEvent
		if err := unmarshal(s.data, &e); err != nil {
			return nil, fmt.Errorf("event %q: %w", s.name, err)
		}
		switch e.Type {
		case "message_start":
			if e.Message != nil {
				resp.ID, resp.Model = e.Message.ID, e.Message.Model
				e.Message.Usage.merge(&resp.Usage)
			}
		case "content_block_start":
			if e.ContentBlock == nil {
				return nil, fmt.Errorf("content_block_start %d without content_block", e.Index)
			}
			b := &anthropicStreamBlock{start: *e.ContentBlock}
			b.text = append(append(b.text, e.ContentBlock.Text...), e.ContentBlock.Thinking...)
			blocks[e.Index] = b
			last = nil
		case "content_block_delta":
			b, ok := blocks[e.Index]
			if !ok {
				return nil, fmt.Errorf("content_block_delta for unstarted block %d", e.Index)
			}
			b.text = append(append(b.text, e.Delta.Text...), e.Delta.Thinking...)
			b.args = append(b.args, e.Delta.PartialJSON...)
		case "message_delta":
			if e.Delta.StopReason != "" {
				resp.StopReason = e.Delta.StopReason
			}
			if e.Usage != nil {
				e.Usage.merge(&resp.Usage)
			}
		case "error":
			if e.Error == nil {
				return nil, fmt.Errorf("stream error without detail")
			}
			return nil, e.Error.err()
		}
	}
	for _, i := range slices.Sorted(maps.Keys(blocks)) {
		resp.Parts = blocks[i].appendParts(resp.Parts)
	}
	return resp, nil
}

func (b *anthropicStreamBlock) appendParts(parts []llm.Part) []llm.Part {
	switch b.start.Type {
	case "text":
		return appendText(parts, llm.PartText, bytesString(b.text))
	case "thinking":
		return appendText(parts, llm.PartReasoning, bytesString(b.text))
	case "tool_use", "server_tool_use", "mcp_tool_use":
		args := bytesString(b.args)
		if args == "" {
			args = "{}"
		}
		return append(parts, callPart(b.start.ID, b.start.Name, args))
	}
	// Blocks that stream no deltas (server tool results) arrive whole in their start event.
	return b.start.appendParts(parts)
}
