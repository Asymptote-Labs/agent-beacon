package codec

import (
	"bytes"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// The stream decoders as they were before streams were decoded event by event without
// allocating, kept verbatim as the reference the current ones must agree with.

func decodeResponseRef(p llm.Protocol, body []byte) (*llm.Response, error) {
	if !isSSE(body) {
		return DecodeResponse(p, body)
	}
	var (
		resp *llm.Response
		err  error
		what string
	)
	switch p {
	case llm.AnthropicMessages:
		resp, err = refAnthropicStream(body)
		what = "anthropic response"
	case llm.OpenAIChat:
		resp, err = refChatStream(body)
		what = "openai chat response"
	default:
		resp, err = refResponsesStream(body)
		what = "openai responses response"
	}
	if err != nil {
		return nil, fmt.Errorf("codec: %s: %w", what, err)
	}
	return resp, nil
}

type refSSEEvent struct {
	name string
	data string
}

func refSSEEvents(body []byte) iter.Seq[refSSEEvent] {
	return func(yield func(refSSEEvent) bool) {
		var (
			name    string
			data    strings.Builder
			hasData bool
			pending bool
		)
		dispatch := func() bool {
			if !pending {
				return true
			}
			ev := refSSEEvent{name: name, data: data.String()}
			name, hasData, pending = "", false, false
			data.Reset()
			return yield(ev)
		}
		for len(body) > 0 {
			var line []byte
			line, body = refCutLine(body)
			if len(line) == 0 {
				if !dispatch() {
					return
				}
				continue
			}
			if line[0] == ':' {
				continue
			}
			field, value, _ := bytes.Cut(line, []byte(":"))
			value = bytes.TrimPrefix(value, []byte(" "))
			switch string(field) {
			case "event":
				name, pending = string(value), true
			case "data":
				if hasData {
					data.WriteByte('\n')
				}
				data.Write(value)
				hasData, pending = true, true
			}
		}
		dispatch()
	}
}

func refCutLine(b []byte) (line, rest []byte) {
	i := bytes.IndexAny(b, "\r\n")
	if i < 0 {
		return b, nil
	}
	if b[i] == '\r' && i+1 < len(b) && b[i+1] == '\n' {
		return b[:i], b[i+2:]
	}
	return b[:i], b[i+1:]
}

type refAnthropicStreamBlock struct {
	start anthropicBlock
	text  strings.Builder
	args  strings.Builder
}

func refAnthropicStream(body []byte) (*llm.Response, error) {
	resp := &llm.Response{}
	blocks := map[int]*refAnthropicStreamBlock{}
	for ev := range refSSEEvents(body) {
		var e struct {
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
		if err := unmarshal([]byte(ev.data), &e); err != nil {
			return nil, fmt.Errorf("event %q: %w", ev.name, err)
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
			b := &refAnthropicStreamBlock{start: *e.ContentBlock}
			b.text.WriteString(e.ContentBlock.Text)
			b.text.WriteString(e.ContentBlock.Thinking)
			blocks[e.Index] = b
		case "content_block_delta":
			b, ok := blocks[e.Index]
			if !ok {
				return nil, fmt.Errorf("content_block_delta for unstarted block %d", e.Index)
			}
			b.text.WriteString(e.Delta.Text)
			b.text.WriteString(e.Delta.Thinking)
			b.args.WriteString(e.Delta.PartialJSON)
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

func (b *refAnthropicStreamBlock) appendParts(parts []llm.Part) []llm.Part {
	switch b.start.Type {
	case "text":
		return appendText(parts, llm.PartText, b.text.String())
	case "thinking":
		return appendText(parts, llm.PartReasoning, b.text.String())
	case "tool_use", "server_tool_use", "mcp_tool_use":
		args := b.args.String()
		if args == "" {
			args = "{}"
		}
		return append(parts, callPart(b.start.ID, b.start.Name, args))
	}
	return b.start.appendParts(parts)
}

func refChatStream(body []byte) (*llm.Response, error) {
	resp := &llm.Response{}
	var reasoning, text, refusal strings.Builder
	calls := map[int]*chatToolCall{}
	var legacy *chatFunction
	for ev := range refSSEEvents(body) {
		if strings.TrimSpace(ev.data) == "[DONE]" {
			break
		}
		var chunk chatResponse
		if err := unmarshal([]byte(ev.data), &chunk); err != nil {
			return nil, fmt.Errorf("chunk: %w", err)
		}
		if chunk.Error != nil {
			return nil, chunk.Error.err()
		}
		if chunk.ID != "" {
			resp.ID = chunk.ID
		}
		if chunk.Model != "" {
			resp.Model = chunk.Model
		}
		if chunk.Usage != nil {
			resp.Usage = chunk.Usage.usage()
		}
		for _, c := range chunk.Choices {
			if c.Index != 0 {
				continue
			}
			d := c.Delta
			reasoning.WriteString(d.ReasoningContent)
			refusal.WriteString(d.Refusal)
			for _, p := range d.Content {
				if p.Kind == llm.PartText {
					text.WriteString(p.Text)
				}
			}
			for _, tc := range d.ToolCalls {
				call, ok := calls[tc.Index]
				if !ok {
					call = &chatToolCall{Index: tc.Index}
					calls[tc.Index] = call
				}
				refMergeCall(call, tc)
			}
			if d.FunctionCall != nil {
				if legacy == nil {
					legacy = &chatFunction{}
				}
				if d.FunctionCall.Name != "" {
					legacy.Name = d.FunctionCall.Name
				}
				legacy.Arguments += d.FunctionCall.Arguments
			}
			if c.FinishReason != "" {
				resp.StopReason = c.FinishReason
			}
		}
	}
	m := chatMessage{ReasoningContent: reasoning.String(), Refusal: refusal.String(), FunctionCall: legacy}
	for _, i := range slices.Sorted(maps.Keys(calls)) {
		m.ToolCalls = append(m.ToolCalls, *calls[i])
	}
	resp.Parts = m.assistantParts(appendText(nil, llm.PartText, text.String()))
	return resp, nil
}

func refMergeCall(c *chatToolCall, f chatToolCall) {
	if f.ID != "" {
		c.ID = f.ID
	}
	if f.Function.Name != "" {
		c.Function.Name = f.Function.Name
	}
	c.Function.Arguments += f.Function.Arguments
	if f.Custom != nil {
		if c.Custom == nil {
			c.Custom = &chatCustomCall{}
		}
		if f.Custom.Name != "" {
			c.Custom.Name = f.Custom.Name
		}
		c.Custom.Input += f.Custom.Input
	}
}

func refResponsesStream(body []byte) (*llm.Response, error) {
	var created, final *responsesResponse
	items := map[int]responsesItem{}
	for ev := range refSSEEvents(body) {
		var e struct {
			Type        string             `json:"type"`
			Response    *responsesResponse `json:"response"`
			OutputIndex int                `json:"output_index"`
			Item        *responsesItem     `json:"item"`
			Code        string             `json:"code"`
			Message     string             `json:"message"`
			Error       *apiError          `json:"error"`
		}
		if err := unmarshal([]byte(ev.data), &e); err != nil {
			return nil, fmt.Errorf("event %q: %w", ev.name, err)
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
