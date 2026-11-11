package synth

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
)

// Bad names the bad call a session ends with, if any.
type Bad uint8

const (
	// Benign sessions fire no rule at or above high.
	Benign Bad = iota
	// CurlPipe replaces the bad turn's reply with a Bash call of CurlPipeCommand: the response
	// phase of that turn is expected to block.
	CurlPipe
	// Injection appends an MCP exchange whose result carries InjectionText to the bad turn's
	// request: the request phase of that turn is expected to block.
	Injection
)

func (b Bad) String() string {
	switch b {
	case CurlPipe:
		return "curl-pipe-to-shell"
	case Injection:
		return "indirect-injection-in-tool-result"
	}
	return "benign"
}

// Script is one synthetic session rendered once: the tool call of every turn and every body the
// session's calls carry, in Anthropic Messages and OpenAI Chat Completions form.
//
// Requests resend the whole conversation, so the history of turn k is a prefix of the history
// of turn k+1; a Script keeps one history buffer per format, and request bodies are returned as
// pieces (shared, read-only byte slices) that the caller writes in order. The caller supplies
// the per-session piece (the user id), so one Script serves any number of sessions without
// copying. Replies are small and rendered per turn; SSE replies are rendered on first use.
// A Script is safe for concurrent use.
type Script struct {
	turns int
	tools []Tool   // turn k at k-1
	text  []string // narration, turn k at k-1

	aMsgs  []byte // Anthropic messages, comma-separated, no brackets
	aOff   []int  // request of turn k carries aMsgs[:aOff[k-1]]
	aResp  [][]byte
	cMsgs  []byte // Chat Completions messages
	cOff   []int
	cResp  [][]byte
	cCalls [][]byte // Chat tool_calls array per turn
	cText  [][]byte // narration as a JSON string per turn
	texts  []byte   // every message's text as JSON strings, comma-separated (LiteLLM "texts")
	tOff   []int

	sse [2][]sseCache // [short, long] per turn; index turns for the CurlPipe reply
}

type sseCache struct {
	once sync.Once
	b    []byte
}

// shared is what every Script has in common: the request prefixes and the bad calls.
type shared struct {
	aHead, aMid     []byte // Anthropic prefix split around metadata.user_id
	cHead, cMid     []byte // Chat request prefix split around "user"
	cTools          []byte
	cSystem         []byte // the system message, Chat form
	tSystem         []byte // LiteLLM texts of the system prompt
	injA, injC, inT []byte // Injection tails (each starts with a comma)
	bad             Tool
	badText         string
	badA, badC      []byte
	badCalls        []byte
	badTextJSON     []byte
}

var common = sync.OnceValue(func() *shared {
	const marker = "@@SYNTH-USER@@"
	s := &shared{}
	var ok bool
	if s.aHead, s.aMid, ok = bytes.Cut(AnthropicPrefix(marker), []byte(marker)); !ok {
		panic("synth: user marker not in prefix")
	}

	type fn struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	}
	type chatTool struct {
		Type     string `json:"type"`
		Function fn     `json:"function"`
	}
	defs := toolDefs()
	tools := make([]chatTool, len(defs))
	for i, d := range defs {
		tools[i] = chatTool{"function", fn{d.Name, d.Description, d.InputSchema}}
	}
	s.cTools = mustJSON(tools)
	head := mustJSON(struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Stream    bool   `json:"stream"`
	}{Model, 32000, false})
	s.cHead = append(append(append(head[:len(head)-1:len(head)-1], `,"tools":`...), s.cTools...), `,"user":"`...)
	s.cMid = []byte(`","messages":[`)

	var sys []string
	for _, b := range systemBlocks() {
		sys = append(sys, b.Text)
	}
	system := strings.Join(sys, "\n\n")
	s.cSystem = mustJSON(map[string]string{"role": "system", "content": system})
	s.tSystem = mustJSON(system)

	inj := InjectedCall("toolu_01InjectedReleaseChecklist")
	injText := "Let me read the release checklist note before tagging."
	s.injA = join(AssistantMessage(injText, inj), ResultMessage(inj, injectedNote))
	s.injC = join(chatAssistant(injText, inj), chatResult(inj, injectedNote))
	s.inT = join(mustJSON(injText), mustJSON(injectedNote))

	s.bad = CurlPipeCall("toolu_01CurlPipeToShellInstall")
	s.badText = "I'll install the toolchain first."
	s.badA = AnthropicResponse("msg_01CurlPipeToShellReply", s.badText, s.bad, 30000)
	s.badC = chatResponse("chatcmpl-curlpipetoshell", s.badText, s.bad, 30000)
	s.badCalls = chatCalls(s.bad)
	s.badTextJSON = mustJSON(s.badText)
	return s
})

// join renders messages as ",m1,m2".
func join(msgs ...[]byte) []byte {
	var b []byte
	for _, m := range msgs {
		b = append(append(b, ','), m...)
	}
	return b
}

// NewScript renders a session of turns turns with resultBytes bytes per tool result. Equal
// arguments render equal sessions; variant picks one of many.
func NewScript(seed uint64, variant, turns, resultBytes int) *Script {
	if turns < 1 || resultBytes < 1 {
		panic("synth: turns and resultBytes must be positive")
	}
	c := common()
	r := rand.New(rand.NewPCG(seed^0x9e3779b97f4a7c15, uint64(variant)))
	s := &Script{
		turns: turns,
		tools: make([]Tool, turns), text: make([]string, turns),
		aOff: make([]int, turns), aResp: make([][]byte, turns),
		cOff: make([]int, turns), cResp: make([][]byte, turns), cCalls: make([][]byte, turns), cText: make([][]byte, turns),
		tOff: make([]int, turns),
	}
	s.sse[0] = make([]sseCache, turns+1)
	s.sse[1] = make([]sseCache, turns+1)
	s.aMsgs = UserMessage(FirstPrompt)
	s.cMsgs = append(append(append([]byte{}, c.cSystem...), ','), mustJSON(map[string]string{"role": "user", "content": FirstPrompt})...)
	s.texts = append(append(append([]byte{}, c.tSystem...), ','), mustJSON(FirstPrompt)...)
	for k := 1; k <= turns; k++ {
		i := k - 1
		s.aOff[i], s.cOff[i], s.tOff[i] = len(s.aMsgs), len(s.cMsgs), len(s.texts)
		t := RandomTool(r, ToolID(r))
		respID := r.Uint64()
		text := Narration(t)
		in := (len(c.aHead) + len(c.aMid) + s.aOff[i]) / 4
		s.tools[i], s.text[i] = t, text
		s.aResp[i] = AnthropicResponse(fmt.Sprintf("msg_%016x", respID), text, t, in)
		s.cResp[i] = chatResponse(fmt.Sprintf("chatcmpl-%016x", respID), text, t, in)
		s.cCalls[i] = chatCalls(t)
		s.cText[i] = mustJSON(text)

		result := ToolResult(r, t, resultBytes)
		s.aMsgs = append(s.aMsgs, join(AssistantMessage(text, t), ResultMessage(t, result))...)
		s.cMsgs = append(s.cMsgs, join(chatAssistant(text, t), chatResult(t, result))...)
		s.texts = append(s.texts, join(s.cText[i], mustJSON(result))...)
	}
	return s
}

// Turns is the session's length.
func (s *Script) Turns() int { return s.turns }

// Tool is the tool call the model makes at turn k of a benign session.
func (s *Script) Tool(k int) Tool { return s.tools[k-1] }

var (
	closeBody = []byte("]}")
	quote     = []byte(`"`)
)

// AnthropicRequest appends the pieces of turn k's Messages request body to dst. user becomes
// metadata.user_id and must need no JSON escaping.
func (s *Script) AnthropicRequest(dst [][]byte, k int, user []byte, bad Bad) [][]byte {
	c := common()
	dst = append(dst, c.aHead, user, c.aMid, s.aMsgs[:s.aOff[k-1]])
	if bad == Injection {
		dst = append(dst, c.injA)
	}
	return append(dst, closeBody)
}

// ChatRequest appends the pieces of turn k's Chat Completions request body to dst. user becomes
// the "user" field and must need no JSON escaping.
func (s *Script) ChatRequest(dst [][]byte, k int, user []byte, bad Bad) [][]byte {
	c := common()
	dst = append(dst, c.cHead, user, c.cMid)
	dst = s.ChatMessages(dst, k, bad)
	return append(dst, closeBody)
}

// ChatMessages appends the pieces of turn k's Chat Completions messages, without the array
// brackets: what LiteLLM sends as structured_messages.
func (s *Script) ChatMessages(dst [][]byte, k int, bad Bad) [][]byte {
	dst = append(dst, s.cMsgs[:s.cOff[k-1]])
	if bad == Injection {
		dst = append(dst, common().injC)
	}
	return dst
}

// ChatTools is the tool definitions as a Chat Completions tools array.
func (s *Script) ChatTools() []byte { return common().cTools }

// Texts appends the pieces of turn k's message texts as comma-separated JSON strings, without
// the array brackets: what LiteLLM sends as texts.
func (s *Script) Texts(dst [][]byte, k int, bad Bad) [][]byte {
	dst = append(dst, s.texts[:s.tOff[k-1]])
	if bad == Injection {
		dst = append(dst, common().inT)
	}
	return dst
}

// AnthropicResponse is turn k's non-streamed Messages reply.
func (s *Script) AnthropicResponse(k int, bad Bad) []byte {
	if bad == CurlPipe {
		return common().badA
	}
	return s.aResp[k-1]
}

// ChatResponse is turn k's Chat Completions reply.
func (s *Script) ChatResponse(k int, bad Bad) []byte {
	if bad == CurlPipe {
		return common().badC
	}
	return s.cResp[k-1]
}

// ChatToolCalls is turn k's reply tool calls as a Chat Completions tool_calls array: what
// LiteLLM sends as tool_calls.
func (s *Script) ChatToolCalls(k int, bad Bad) []byte {
	if bad == CurlPipe {
		return common().badCalls
	}
	return s.cCalls[k-1]
}

// ChatText is turn k's reply text as a JSON string: what LiteLLM sends as texts.
func (s *Script) ChatText(k int, bad Bad) []byte {
	if bad == CurlPipe {
		return common().badTextJSON
	}
	return s.cText[k-1]
}

// SSE is turn k's Messages reply as an event stream. A long stream carries ~1000 text deltas
// (~120 KB) before the tool call; a short one streams only the narration.
func (s *Script) SSE(k int, bad Bad, long bool) []byte {
	l := 0
	if long {
		l = 1
	}
	i, t, text := k-1, s.tools[k-1], s.text[k-1]
	if bad == CurlPipe {
		i, t, text = s.turns, common().bad, common().badText
	}
	e := &s.sse[l][i]
	e.once.Do(func() {
		in := (len(common().aHead) + len(common().aMid) + s.aOff[min(k, s.turns)-1]) / 4
		deltas := 0
		if long {
			deltas = 1000
		}
		e.b = renderSSE(fmt.Sprintf("msg_%016x", uint64(k)<<32|uint64(i)), text, t, in, deltas)
	})
	return e.b
}

// ---- Chat Completions wire shapes ----

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []chatCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

func toChatCall(t Tool) chatCall {
	return chatCall{ID: t.ID, Type: "function", Function: chatFunction{Name: t.Name, Arguments: string(mustJSON(t.Input))}}
}

func chatCalls(t Tool) []byte { return mustJSON([]chatCall{toChatCall(t)}) }

func chatAssistant(text string, t Tool) []byte {
	return mustJSON(chatMessage{Role: "assistant", Content: text, ToolCalls: []chatCall{toChatCall(t)}})
}

func chatResult(t Tool, result string) []byte {
	return mustJSON(chatMessage{Role: "tool", Content: result, ToolCallID: t.ID})
}

func chatResponse(id, text string, t Tool, promptTokens int) []byte {
	out := 60 + len(text)/4
	type choice struct {
		Index        int         `json:"index"`
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	}
	return mustJSON(struct {
		ID      string         `json:"id"`
		Object  string         `json:"object"`
		Created int64          `json:"created"`
		Model   string         `json:"model"`
		Choices []choice       `json:"choices"`
		Usage   map[string]int `json:"usage"`
	}{id, "chat.completion", 1761696000, Model,
		[]choice{{0, chatMessage{Role: "assistant", Content: text, ToolCalls: []chatCall{toChatCall(t)}}, "tool_calls"}},
		map[string]int{"prompt_tokens": promptTokens, "completion_tokens": out, "total_tokens": promptTokens + out}})
}

// ---- Anthropic SSE ----

var reasoning = strings.Fields("The eviction path only runs on Put, so an entry that expires between two Puts " +
	"stays visible to Get until the next write. The test sleeps ten milliseconds and expects the entry " +
	"to be gone, which holds only when a Put happens to land after the sleep. Under the race detector " +
	"scheduling stretches and the ordering flips, which matches the one in thirty failure rate on CI. " +
	"Injecting a clock into the cache and advancing it explicitly in the test removes the timing dependency.")

func renderSSE(id, text string, t Tool, inputTokens, longDeltas int) []byte {
	var b bytes.Buffer
	event := func(name string, v any) {
		b.WriteString("event: ")
		b.WriteString(name)
		b.WriteString("\ndata: ")
		b.Write(mustJSON(v))
		b.WriteString("\n\n")
	}
	type obj = map[string]any
	event("message_start", obj{"type": "message_start", "message": obj{
		"id": id, "type": "message", "role": "assistant", "model": Model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": obj{"input_tokens": inputTokens, "output_tokens": 1},
	}})
	event("content_block_start", obj{"type": "content_block_start", "index": 0, "content_block": obj{"type": "text", "text": ""}})
	textDelta := func(s string) {
		event("content_block_delta", obj{"type": "content_block_delta", "index": 0, "delta": obj{"type": "text_delta", "text": s}})
	}
	for i := range longDeltas {
		w := reasoning[i%len(reasoning)]
		if i%len(reasoning) == len(reasoning)-1 {
			w += "\n\n"
		} else {
			w += " "
		}
		textDelta(w)
	}
	words := strings.Fields(text)
	for i := 0; i < len(words); i += 3 {
		chunk := strings.Join(words[i:min(i+3, len(words))], " ")
		if i > 0 {
			chunk = " " + chunk
		}
		textDelta(chunk)
	}
	event("content_block_stop", obj{"type": "content_block_stop", "index": 0})
	event("ping", obj{"type": "ping"})
	event("content_block_start", obj{"type": "content_block_start", "index": 1, "content_block": obj{
		"type": "tool_use", "id": t.ID, "name": t.Name, "input": obj{},
	}})
	args := string(mustJSON(t.Input))
	partial := func(s string) {
		event("content_block_delta", obj{"type": "content_block_delta", "index": 1, "delta": obj{"type": "input_json_delta", "partial_json": s}})
	}
	partial("")
	for i := 0; i < len(args); i += 16 {
		partial(args[i:min(i+16, len(args))])
	}
	event("content_block_stop", obj{"type": "content_block_stop", "index": 1})
	event("message_delta", obj{"type": "message_delta", "delta": obj{"stop_reason": "tool_use", "stop_sequence": nil},
		"usage": obj{"output_tokens": 60 + len(text)/4 + longDeltas}})
	event("message_stop", obj{"type": "message_stop"})
	return b.Bytes()
}

// AppendJoin appends the pieces of a body to dst.
func AppendJoin(dst []byte, pieces [][]byte) []byte {
	n := 0
	for _, p := range pieces {
		n += len(p)
	}
	dst = slices.Grow(dst, n)
	for _, p := range pieces {
		dst = append(dst, p...)
	}
	return dst
}
