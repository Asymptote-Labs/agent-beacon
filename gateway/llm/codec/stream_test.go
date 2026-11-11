package codec

import (
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/gateway/internal/synth"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// checkStream asserts that DecodeResponse agrees with the reference decoders on body: the same
// response, or errors with the same message.
func checkStream(t testing.TB, p llm.Protocol, body []byte) {
	t.Helper()
	want, werr := decodeResponseRef(p, body)
	got, err := DecodeResponse(p, body)
	if (werr == nil) != (err == nil) || werr != nil && werr.Error() != err.Error() {
		t.Fatalf("%s: error %v, reference %v\nbody: %q", p, err, werr, body)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: response\n%+v %s\nreference\n%+v %s\nbody: %q", p, got, describeParts(got), want, describeParts(want), body)
	}
}

func describeParts(r *llm.Response) []string {
	if r == nil {
		return nil
	}
	return describeResponse(r)
}

// streamWords splits text into deltas a word long, as models stream.
func streamWords(text string) []string {
	var out []string
	for len(text) > 0 {
		i := strings.IndexAny(text[1:], " \n")
		if i < 0 {
			out = append(out, text)
			break
		}
		out = append(out, text[:i+1])
		text = text[i+1:]
	}
	return out
}

// streamText is the text of turn k of a synthetic session as its stream carries it: the long
// one has ~120 KB of reasoning before the narration.
func streamText(s *synth.Script, k int, long bool) (string, llm.ToolCall) {
	resp, err := refAnthropicStream(s.SSE(k, synth.Benign, long))
	if err != nil {
		panic(err)
	}
	return llm.JoinText(resp.Parts), llm.ToolCalls(resp.Parts)[0]
}

func sseData(b *strings.Builder, event string, v any) {
	if event != "" {
		b.WriteString("event: " + event + "\n")
	}
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	b.WriteString("data: ")
	b.Write(data)
	b.WriteString("\n\n")
}

// chatSSE renders a Chat Completions stream as OpenAI sends one: a role chunk, text deltas, a
// tool call whose arguments arrive in pieces, the finish chunk, a usage chunk and [DONE].
func chatSSE(text string, call llm.ToolCall) []byte {
	type function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	}
	type toolCall struct {
		Index    int      `json:"index"`
		ID       string   `json:"id,omitempty"`
		Type     string   `json:"type,omitempty"`
		Function function `json:"function"`
	}
	type delta struct {
		Role      string     `json:"role,omitempty"`
		Content   *string    `json:"content,omitempty"`
		Refusal   *string    `json:"refusal,omitempty"`
		ToolCalls []toolCall `json:"tool_calls,omitempty"`
	}
	type choice struct {
		Index        int     `json:"index"`
		Delta        delta   `json:"delta"`
		Logprobs     any     `json:"logprobs"`
		FinishReason *string `json:"finish_reason"`
	}
	type chunk struct {
		ID          string   `json:"id"`
		Object      string   `json:"object"`
		Created     int      `json:"created"`
		Model       string   `json:"model"`
		Fingerprint string   `json:"system_fingerprint"`
		Choices     []choice `json:"choices"`
		Usage       any      `json:"usage"`
		Obfuscation string   `json:"obfuscation,omitempty"`
	}
	var b strings.Builder
	emit := func(c chunk) {
		c.ID, c.Object, c.Created, c.Model, c.Fingerprint = "chatcmpl-1", "chat.completion.chunk", 1761696000, "gpt-4o-2024-08-06", "fp_1"
		sseData(&b, "", c)
	}
	empty := ""
	emit(chunk{Choices: []choice{{Delta: delta{Role: "assistant", Content: &empty, Refusal: nil}}}})
	for _, w := range streamWords(text) {
		emit(chunk{Choices: []choice{{Delta: delta{Content: &w}}}, Obfuscation: "x7"})
	}
	emit(chunk{Choices: []choice{{Delta: delta{ToolCalls: []toolCall{{ID: call.ID, Type: "function", Function: function{Name: call.Name}}}}}}})
	for i := 0; i < len(call.Arguments); i += 16 {
		emit(chunk{Choices: []choice{{Delta: delta{ToolCalls: []toolCall{{Function: function{Arguments: call.Arguments[i:min(i+16, len(call.Arguments))]}}}}}}})
	}
	stop := "tool_calls"
	emit(chunk{Choices: []choice{{FinishReason: &stop}}})
	emit(chunk{Choices: []choice{}, Usage: map[string]any{"prompt_tokens": 9000, "completion_tokens": 300, "total_tokens": 9300,
		"prompt_tokens_details": map[string]int{"cached_tokens": 8000, "audio_tokens": 0}}})
	b.WriteString("data: [DONE]\n\n")
	return []byte(b.String())
}

// responsesSSE renders a Responses stream as OpenAI sends one, every event named and typed.
func responsesSSE(text string, call llm.ToolCall) []byte {
	type obj = map[string]any
	var b strings.Builder
	seq := 0
	event := func(typ string, fields ...any) {
		// An ordered object: type and sequence number first, as OpenAI sends them.
		var s strings.Builder
		s.WriteString(`{"type":"` + typ + `","sequence_number":` + fmt.Sprint(seq))
		for i := 0; i < len(fields); i += 2 {
			v, _ := json.Marshal(fields[i+1])
			s.WriteString(`,"` + fields[i].(string) + `":` + string(v))
		}
		s.WriteString("}")
		seq++
		b.WriteString("event: " + typ + "\ndata: " + s.String() + "\n\n")
	}
	msg := obj{"id": "msg_1", "type": "message", "status": "completed", "role": "assistant",
		"content": []any{obj{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}}}
	fc := obj{"id": "fc_1", "type": "function_call", "status": "completed", "arguments": call.Arguments, "call_id": call.ID, "name": call.Name}
	resp := func(status string, output []any, usage any) obj {
		return obj{"id": "resp_1", "object": "response", "created_at": 1761696000, "status": status, "model": "gpt-5-codex",
			"output": output, "usage": usage, "error": nil, "incomplete_details": nil, "reasoning": obj{"effort": "medium"}}
	}
	event("response.created", "response", resp("in_progress", []any{}, nil))
	event("response.in_progress", "response", resp("in_progress", []any{}, nil))
	event("response.output_item.added", "output_index", 0, "item", obj{"id": "msg_1", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}})
	event("response.content_part.added", "item_id", "msg_1", "output_index", 0, "content_index", 0, "part", obj{"type": "output_text", "text": "", "annotations": []any{}})
	for _, w := range streamWords(text) {
		event("response.output_text.delta", "item_id", "msg_1", "output_index", 0, "content_index", 0, "delta", w, "logprobs", []any{}, "obfuscation", "Qk")
	}
	event("response.output_text.done", "item_id", "msg_1", "output_index", 0, "content_index", 0, "text", text, "logprobs", []any{})
	event("response.content_part.done", "item_id", "msg_1", "output_index", 0, "content_index", 0, "part", obj{"type": "output_text", "text": text, "annotations": []any{}})
	event("response.output_item.done", "output_index", 0, "item", msg)
	event("response.output_item.added", "output_index", 1, "item", obj{"id": "fc_1", "type": "function_call", "status": "in_progress", "arguments": "", "call_id": call.ID, "name": call.Name})
	for i := 0; i < len(call.Arguments); i += 16 {
		event("response.function_call_arguments.delta", "item_id", "fc_1", "output_index", 1, "delta", call.Arguments[i:min(i+16, len(call.Arguments))])
	}
	event("response.function_call_arguments.done", "item_id", "fc_1", "output_index", 1, "arguments", call.Arguments)
	event("response.output_item.done", "output_index", 1, "item", fc)
	event("response.completed", "response", resp("completed", []any{msg, fc},
		obj{"input_tokens": 9000, "input_tokens_details": obj{"cached_tokens": 8000}, "output_tokens": 300, "total_tokens": 9300}))
	return []byte(b.String())
}

// streamBodies are synthetic streams of each protocol: short and long, benign and bad turns.
func streamBodies() map[llm.Protocol][][]byte {
	s := synth.NewScript(1, 0, 20, 3000)
	out := map[llm.Protocol][][]byte{}
	for _, k := range []int{1, 5, 20} {
		for _, long := range []bool{false, true} {
			for _, bad := range []synth.Bad{synth.Benign, synth.CurlPipe, synth.Injection} {
				out[llm.AnthropicMessages] = append(out[llm.AnthropicMessages], s.SSE(k, bad, long))
			}
			text, call := streamText(s, k, long)
			out[llm.OpenAIChat] = append(out[llm.OpenAIChat], chatSSE(text, call))
			out[llm.OpenAIResponses] = append(out[llm.OpenAIResponses], responsesSSE(text, call))
		}
	}
	return out
}

// streamVariants derives streams a decoder must take in stride from a well-formed one: other
// line endings, comments, multi-line data, unknown events.
func streamVariants(body []byte) [][]byte {
	s := string(body)
	var multi strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		if rest, ok := strings.CutPrefix(line, "data: {"); ok {
			multi.WriteString("data: {\ndata:" + rest)
			continue
		}
		multi.WriteString(line)
	}
	return [][]byte{
		[]byte(strings.ReplaceAll(s, "\n", "\r\n")),
		[]byte(strings.ReplaceAll(s, "\n", "\r")),
		[]byte(": keep-alive\n\n" + strings.ReplaceAll(s, "\n\n", "\n: ping\nid: 9\nretry: 100\n\n")),
		[]byte(multi.String()),
		[]byte(strings.Replace(s, "\n\n", "\n\nevent: mystery\ndata: {\"type\":\"mystery\",\"x\":[1,{\"y\":null}],\"z\":-1.5e3}\n\n", 3)),
		[]byte(strings.TrimRight(s, "\n")),
		[]byte(s[:len(s)/2]),
		[]byte(s[:len(s)*3/4]),
	}
}

// Adversarial streams, each a whole body.
var streamAdversarial = map[llm.Protocol][]string{
	llm.AnthropicMessages: func() []string {
		start := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude\",\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"Hi\"}}\n\n"
		tool := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"Bash\",\"input\":{}}}\n\n"
		delta := func(d string) string { return "event: content_block_delta\ndata: " + d + "\n\n" }
		end := "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		var out []string
		for _, d := range []string{
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" there"}}`,
			`{"delta":{"text":" sorted","type":"text_delta"},"index":0,"type":"content_block_delta"}`,
			` { "type" : "content_block_delta" , "index" : 0 , "delta" : { "type" : "text_delta" , "text" : "\" \\ \/ \b\f\n\r\t \u00e9 \ud83d\ude00 \ud800 \udc00x" } } `,
			"{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"raw é \xff\xfe \xed\xa0\x80\"}}",
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":null}}`,
			`{"type":"content_block_delta","index":0,"delta":null}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"a","text":"b"}}`,
			`{"type":"content_block_delta","index":0,"index":1,"delta":{"partial_json":"{\"a\":1}"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"a"},"delta":{"partial_json":"x"}}`,
			`{"Type":"content_block_delta","INDEX":0,"Delta":{"TEXT":"cased"}}`,
			`{"type":"content_block_delta","index":null,"delta":{"text":"null index"}}`,
			`{"type":"content_block_delta","delta":{"text":"no index"}}`,
			`{"type":"content_block_delta","index":-0,"delta":{"text":"minus zero"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\": \"ls\"}"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x","extra":[1,2,{"a":true}]},"more":{"b":false}}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"s","stop_reason":"odd"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"u"},"usage":{"output_tokens":3}}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"m"},"message":{"id":"other"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"\u0074"}}`,
			`{"t\u0079pe":"content_block_delta","index":0,"delta":{"text":"escaped name"}}`,
			`{"type":"content_block_delta","index":7,"delta":{"text":"unknown index"}}`,
			`{"type":"content_block_delta","index":"0","delta":{"text":"string index"}}`,
			`{"type":"content_block_delta","index":0.0,"delta":{"text":"float index"}}`,
			`{"type":"content_block_delta","index":01,"delta":{"text":"leading zero"}}`,
			`{"type":"content_block_delta","index":99999999999999999999,"delta":{"text":"huge"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":5}}`,
			`{"type":"content_block_delta","index":0,"delta":"x"}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"unterminated}}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"bad \x escape"}}`,
			"{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"text\":\"control \x01\"}}",
			`{"type":"content_block_delta","index":0,"delta":{"text":"trailing"}} x`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"two"}}{}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"tr"},"x":tru}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"num"},"x":1.}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"num"},"x":-}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"num"},"x":1e}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"num"},"x":[1,]}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"num"},"x":{"a":1,}}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"num"},"x":0123}`,
			`{"type":"content_block_delta","index":0,"delta":{"text":"num"},"x":[1e+5,-0.5E-3,0,true,false,null,"s",{}]}`,
			`[]`, `null`, `"string"`, ``, `[DONE]`,
			`{"type":"ping"}`,
			`{"type":"ping","index":"x"}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			`{"type":"error"}`,
		} {
			out = append(out, start+delta(d)+tool+end, start+tool+delta(d)+end)
		}
		out = append(out,
			delta(`{"type":"content_block_delta","index":0,"delta":{"text":"before start"}}`)+start+end,
			start+tool+"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\"}}\n\n",
			start, tool+start, start+start+end, start+"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"+end,
			"data: "+`{"type":"content_block_start","index":0,"content_block":null}`+"\n\n",
		)
		return out
	}(),
	llm.OpenAIChat: func() []string {
		chunk := func(c string) string { return "data: " + c + "\n\n" }
		head := chunk(`{"id":"c1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
		body := chunk(`{"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`)
		tail := chunk(`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`) + "data: [DONE]\n\n"
		var out []string
		for _, c := range []string{
			`{"id":"c1","choices":[{"index":0,"delta":{"content":" world"}}]}`,
			`{"choices":[{"delta":{"content":" esc \" \u00e9 \ud800"},"index":0}],"id":"c2","model":"m2"}`,
			"{\"choices\":[{\"index\":0,\"delta\":{\"content\":\"raw \xff\"}}]}",
			`{"choices":[{"index":0,"delta":{"content":null,"reasoning_content":"think","refusal":"no"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":[{"type":"text","text":"part"},{"type":"image_url","image_url":"u"}]}}]}`,
			`{"choices":[{"index":1,"delta":{"content":"other choice"}},{"index":0,"delta":{"content":" mine"}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"p\":"}},{"index":2,"id":"call_2","function":{"name":"b","arguments":"{}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":-3,"function":{"arguments":"neg"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"custom","custom":{"name":"apply_patch","input":"*** Begin"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[null]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":null}}]}`,
			`{"choices":[{"index":0,"delta":{"function_call":{"name":"legacy","arguments":"{\"a\""}}}]}`,
			`{"choices":[{"index":0,"delta":{"function_call":{"arguments":":1}"}}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"length"}]}`,
			`{"choices":[{"index":0,"delta":{"content":"x"},"message":{"content":"ignored"}}]}`,
			`{"choices":null,"usage":{"prompt_tokens":5,"completion_tokens":6,"prompt_tokens_details":{"cached_tokens":2}}}`,
			`{"choices":[],"usage":null}`,
			`{"usage":{"prompt_tokens":5.5}}`,
			`{"usage":{"prompt_tokens":"5"}}`,
			`{"choices":[{"index":"0","delta":{"content":"s"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":5}}]}`,
			`{"choices":{}}`,
			`{"choices":[{"index":0,"delta":{"content":"a","content":"b"}}]}`,
			`{"CHOICES":[{"INDEX":0,"Delta":{"Content":"cased"}}]}`,
			`{"id":null,"model":null,"choices":[{"index":0,"delta":{"content":"nulls"},"finish_reason":null}]}`,
			`{"error":{"type":"server_error","message":"boom"}}`,
			`{"error":null,"choices":[{"index":0,"delta":{"content":"no error"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"x"}}]} trailing`,
			`{"choices":[{"index":0,"delta":{"content":"unterminated}}]}`,
			`  [DONE]  `,
			`{"choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"logprobs":null,"finish_reason":null}],"usage":null,"obfuscation":"a","system_fingerprint":"fp","created":1,"service_tier":"default"}`,
		} {
			out = append(out, head+chunk(c)+body+tail, head+body+chunk(c))
		}
		out = append(out, head+body+"data: [DONE]\n\ndata: {not json\n\n", head+"data: [DONE]\n\n"+body, body, "data: {}\n\n", "data: \n\n")
		return out
	}(),
	llm.OpenAIResponses: func() []string {
		ev := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
		created := ev("response.created", `{"type":"response.created","response":{"id":"resp_1","model":"gpt-5","status":"in_progress","output":[]}}`)
		item := ev("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}}`)
		done := ev("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}],"usage":{"input_tokens":3,"output_tokens":4}}}`)
		var out []string
		for _, d := range []string{
			`{"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":0,"delta":"x","logprobs":[],"obfuscation":"q"}`,
			`{"type":"response.output_text.delta","output_index":"0","delta":"x"}`,
			`{"type":"response.output_text.delta","output_index":1.5,"delta":"x"}`,
			`{"type":"response.output_text.delta","item":5}`,
			`{"type":"response.output_text.delta","response":5}`,
			`{"type":"response.output_text.delta","code":5}`,
			`{"type":"response.output_text.delta","message":["x"]}`,
			`{"type":"response.output_text.delta","error":{"message":"not an error event"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"a\""}`,
			`{"type":"response.reasoning_summary_text.delta","summary_index":0,"delta":"plan"}`,
			`{"TYPE":"response.output_text.delta","delta":"x"}`,
			`{"type":"response.output_text.delta","delta":"unterminated}`,
			`{"type":"response.output_text.delta","delta":"x"} {}`,
			`{"type":"response.output_item.done","output_index":3,"item":{"type":"function_call","call_id":"c9","name":"shell","arguments":"{}"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":null}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":5}}`,
			`{"type":"response.in_progress","response":{"id":"resp_2","model":"gpt-5.1"}}`,
			`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`,
			`{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_error","message":"x"}}}`,
			`{"type":"error","code":"rate_limit_exceeded","message":"slow down"}`,
			`{"type":"error","error":{"type":"server_error","message":"boom"}}`,
			`{"type":"keepalive"}`,
			`null`, `[]`, ``,
		} {
			out = append(out, created+ev("x", d)+item+done, created+item+ev("x", d))
		}
		out = append(out, created+item, item, created, done+created, "data: "+`{"type":"response.created","response":{"id":"r"}}`+"\n\n")
		return out
	}(),
}

func TestStreamEquivalence(t *testing.T) {
	for p, bodies := range streamBodies() {
		for _, body := range bodies {
			checkStream(t, p, body)
			for _, v := range streamVariants(body) {
				checkStream(t, p, v)
			}
		}
	}
	for p, bodies := range streamAdversarial {
		for _, body := range bodies {
			checkStream(t, p, []byte(body))
			for _, v := range streamVariants([]byte(body)) {
				checkStream(t, p, v)
			}
		}
	}
}

// FuzzDecodeStream checks the stream decoders against the reference on arbitrary bodies.
func FuzzDecodeStream(f *testing.F) {
	protocols := []llm.Protocol{llm.AnthropicMessages, llm.OpenAIChat, llm.OpenAIResponses}
	for i, p := range protocols {
		for _, body := range streamAdversarial[p] {
			f.Add(uint8(i), []byte(body))
		}
		bodies := streamBodies()[p]
		f.Add(uint8(i), bodies[0])
	}
	f.Fuzz(func(t *testing.T, which uint8, body []byte) {
		checkStream(t, protocols[int(which)%len(protocols)], body)
	})
}

// BenchmarkDecodeStream decodes a short (~1 KB) and a long (~120 KB) stream of each protocol
// with the reference decoders (before) and the current ones.
func BenchmarkDecodeStream(b *testing.B) {
	s := synth.NewScript(1, 0, 20, 3000)
	for _, long := range []bool{false, true} {
		text, call := streamText(s, 5, long)
		size := "short"
		if long {
			size = "long"
		}
		for _, bc := range []struct {
			p    llm.Protocol
			body []byte
		}{
			{llm.AnthropicMessages, s.SSE(5, synth.Benign, long)},
			{llm.OpenAIChat, chatSSE(text, call)},
			{llm.OpenAIResponses, responsesSSE(text, call)},
		} {
			for _, impl := range []struct {
				name   string
				decode func(llm.Protocol, []byte) (*llm.Response, error)
			}{{"before", decodeResponseRef}, {"after", DecodeResponse}} {
				b.Run(fmt.Sprintf("%s/%s/%s", bc.p, size, impl.name), func(b *testing.B) {
					b.SetBytes(int64(len(bc.body)))
					b.ReportAllocs()
					for b.Loop() {
						if _, err := impl.decode(bc.p, bc.body); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}

// TestStreamFastPaths checks that the fast paths, not the decoder, take the events that make
// up a stream: deltas, pings, stops and every chunk.
func TestStreamFastPaths(t *testing.T) {
	s := synth.NewScript(1, 0, 20, 3000)
	text, call := streamText(s, 5, true)
	slow := map[llm.Protocol]int{}
	for sc := newSSEScanner(s.SSE(5, synth.Benign, true)); sc.next(); {
		if _, ok := readAnthropicDelta(sc.data); !ok {
			slow[llm.AnthropicMessages]++
		}
	}
	st := &chatStream{}
	for sc := newSSEScanner(chatSSE(text, call)); sc.next(); {
		if string(sc.data) != "[DONE]" && !st.readChatChunk(sc.data) {
			slow[llm.OpenAIChat]++
		}
	}
	for sc := newSSEScanner(responsesSSE(text, call)); sc.next(); {
		if !inertResponsesEvent(sc.data) {
			slow[llm.OpenAIResponses]++
		}
	}
	// Anthropic: message_start, two content_block_starts, message_delta. Responses: created,
	// in_progress, two output_item.added and two output_item.done (items are decoded, as their
	// shape can fail decoding), completed.
	want := map[llm.Protocol]int{llm.AnthropicMessages: 4, llm.OpenAIResponses: 7}
	if !reflect.DeepEqual(slow, want) {
		t.Errorf("events left to the decoder: %v, want %v", slow, want)
	}
}

// jsonSamples are values validJSON must judge as the decoder does, or give up on.
var jsonSamples = []string{
	`{}`, `[]`, ` {"a" : [1, -0, 0.5, 1e5, 1E+5, -1.5e-3, true, false, null, "s"]} `, `"\u00e9\ud800\"\\\/\b\f\n\r\t"`,
	"\"raw \xff\xfe\"", `{"a":1,"a":2}`, `0`, `-0`, `01`, `1.`, `.5`, `1e`, `1e+`, `-`, `+1`, `--1`, `1x`, `tru`, `nul`,
	`truex`, `[1,]`, `[,1]`, `{"a":1,}`, `{,}`, `{"a"}`, `{"a":}`, `{a:1}`, `{'a':1}`, `"\x"`, `"\u12"`, `"\u12g4"`,
	"\"\x01\"", "\"\x1f\"", "\"\x7f\"", `"unterminated`, `"`, ``, ` `, `{} {}`, `[] x`, `[1 2]`, `{"a":1 "b":2}`,
	"\ufeff{}", "{}\x00", `[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]`,
	"{\"a\":\t\r\n[\n]\n}", `1.0e00`, `123456789012345678901234567890`, `"` + strings.Repeat(`ab\n`, 40) + `"`,
}

// TestValidJSON checks that validJSON never accepts what the decoder rejects.
func TestValidJSON(t *testing.T) {
	for _, s := range jsonSamples {
		checkValidJSON(t, []byte(s))
	}
}

func checkValidJSON(t *testing.T, b []byte) {
	t.Helper()
	var v jsontext.Value // syntax only: a number the decoder skips is never parsed
	if validJSON(b) && unmarshal(b, &v) != nil {
		t.Fatalf("validJSON(%q) is true, the decoder rejects it", b)
	}
}

func FuzzValidJSON(f *testing.F) {
	for _, s := range jsonSamples {
		f.Add([]byte(s))
	}
	f.Fuzz(checkValidJSON)
}
