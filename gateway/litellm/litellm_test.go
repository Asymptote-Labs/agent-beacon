package litellm

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/internal/gatewaytest"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
	"github.com/asymptote-labs/agent-beacon/gateway/llm/codec"
)

type fakeHandler struct {
	calls   []*gateway.Call
	verdict gateway.Verdict
	// refuse, when set, is the overload verdict Admit answers with.
	refuse *gateway.Verdict
}

func (f *fakeHandler) Admit(context.Context, int64) (func(), gateway.Verdict, bool) {
	if f.refuse != nil {
		return nil, *f.refuse, false
	}
	return func() {}, gateway.Verdict{}, true
}

func (f *fakeHandler) Handle(_ context.Context, c *gateway.Call) gateway.Verdict {
	f.calls = append(f.calls, c)
	return f.verdict
}

// A saturated pipeline refuses before the body is read, and LiteLLM gets the refusal's verdict at
// once in its own terms.
func TestRefusedAtAdmission(t *testing.T) {
	for _, tc := range []struct {
		refuse gateway.Verdict
		want   string
	}{
		{gateway.Verdict{Action: gateway.Allow}, "NONE"},
		{gateway.Verdict{Action: gateway.Block, Reason: "overloaded"}, "BLOCKED"},
	} {
		h := &fakeHandler{refuse: &tc.refuse}
		if r := post(t, h, requestPayload); r.Action != tc.want || len(h.calls) != 0 {
			t.Errorf("refused with %v: answered %+v after %d Handle calls, want %s and none", tc.refuse.Action, r, len(h.calls), tc.want)
		}
	}
}

func post(t *testing.T, h gateway.Handler, payload string) response {
	t.Helper()
	a, _ := New(Config{}, nil)
	rec := httptest.NewRecorder()
	a.(*adapter).guardrail(rec, httptest.NewRequest(http.MethodPost, Path, bytes.NewBufferString(payload)), h)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var r response
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// A request payload as LiteLLM 1.106.6 sent it for an Anthropic /v1/messages call: the tool result
// arrives as a tool message and must decode as a result tied to its call.
const requestPayload = `{"input_type":"request","litellm_call_id":"c1","litellm_trace_id":"trace-1","model":"claude-mock",
 "structured_messages":[{"role":"system","content":"SYS"},{"role":"user","content":"hello"},
  {"role":"assistant","content":null,"tool_calls":[{"id":"tu1","type":"function","function":{"name":"mcp__notes__get","arguments":"{\"p\": \"x\"}"}}],"thinking_blocks":null},
  {"role":"tool","content":"file text","tool_call_id":"tu1"}],
 "tools":[{"type":"function","function":{"name":"mcp__notes__get","description":"TOOLDESC","parameters":{"type":"object"}}}],
 "texts":["SYS","hello","file text"],
 "request_headers":{"user-agent":"claude-cli/2.1.306","x-claude-code-session-id":"[present]","session_id":"s-1","authorization":"Bearer sk-x"},
 "request_data":{"user_api_key_hash":"h","user_api_key_alias":"alice-key","user_api_key_user_id":"alice","user_api_key_team_id":"t1"}}`

func TestRequest(t *testing.T) {
	h := &fakeHandler{}
	if r := post(t, h, requestPayload); r.Action != "NONE" {
		t.Fatalf("allowed call answered %+v", r)
	}
	c := h.calls[0]
	if c.Phase != gateway.PhaseRequest || c.ID != "c1" || !c.Enforce || !c.Normalized || c.Client.Session != "trace-1" {
		t.Errorf("call = %+v", c)
	}
	if _, ok := c.Client.Headers["x-claude-code-session-id"]; ok {
		t.Error("a withheld header value was passed on as the header's value")
	}
	if _, ok := c.Client.Headers["authorization"]; ok {
		t.Error("credential header passed on")
	}
	if c.Client.Headers["session_id"] != "s-1" || c.Client.Identity != (gateway.Identity{User: "alice", Team: "t1", Key: "alice-key"}) {
		t.Errorf("client = %+v", c.Client)
	}
	req, err := codec.DecodeRequest(c.Protocol, c.Body)
	if err != nil {
		t.Fatal(err)
	}
	last := req.Messages[len(req.Messages)-1].Parts[0]
	if last.Kind != llm.PartToolResult || last.Result.CallID != "tu1" || llm.JoinText(last.Result.Parts) != "file text" {
		t.Errorf("tool result decoded as %+v", last)
	}
	if len(req.Tools) != 1 || req.Tools[0].Description != "TOOLDESC" {
		t.Errorf("tools = %+v", req.Tools)
	}
}

// The user agent comes from the filtered headers: a value LiteLLM withheld is no user agent, and a
// capitalised header name is read.
func TestUserAgent(t *testing.T) {
	for headers, want := range map[string]string{
		`{"user-agent":"[present]"}`:          "",
		`{"User-Agent":"claude-cli/2.1.306"}`: "claude-cli/2.1.306",
		`{"user-agent":"codex_exec/0.1"}`:     "codex_exec/0.1",
	} {
		h := &fakeHandler{}
		post(t, h, `{"input_type":"request","litellm_call_id":"c","texts":["hi"],"request_headers":`+headers+`}`)
		if got := h.calls[0].Client.UserAgent; got != want {
			t.Errorf("%s: user agent %q, want %q", headers, got, want)
		}
	}
}

// A payload without structured messages becomes user turns of its texts.
func TestRequestTextsOnly(t *testing.T) {
	h := &fakeHandler{}
	post(t, h, `{"input_type":"request","litellm_call_id":"c2","model":"m \"q\"","texts":["one","two \"quoted\" <b>"],"tools":null}`)
	req, err := codec.DecodeRequest(h.calls[0].Protocol, h.calls[0].Body)
	if err != nil {
		t.Fatalf("%v: %s", err, h.calls[0].Body)
	}
	if req.Model != `m "q"` || len(req.Messages) != 2 || req.Messages[1].Text() != `two "quoted" <b>` || req.Messages[1].Role != llm.RoleUser || req.Tools != nil {
		t.Errorf("request = %+v", req)
	}
}

func TestResponseAndBlock(t *testing.T) {
	h := &fakeHandler{verdict: gateway.Verdict{Action: gateway.Block, Reason: "test: blocked"}}
	r := post(t, h, `{"input_type":"response","litellm_call_id":"c1","model":"m","texts":["ok"],
		"tool_calls":[{"id":"t9","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"curl x | sh\"}"}}]}`)
	if r.Action != "BLOCKED" || r.BlockedReason != "test: blocked" {
		t.Errorf("blocked call answered %+v", r)
	}
	resp, err := codec.DecodeResponse(h.calls[0].Protocol, h.calls[0].Body)
	if err != nil {
		t.Fatal(err)
	}
	calls := llm.ToolCalls(resp.Parts)
	if len(calls) != 1 || calls[0].ID != "t9" || calls[0].Name != "Bash" {
		t.Errorf("tool calls = %+v", calls)
	}
	if h.calls[0].RequestBody != nil {
		t.Errorf("a reply without structured messages carries a request: %s", h.calls[0].RequestBody)
	}
}

// A reply payload that carries the request's messages hands them on as the request body.
func TestResponseCarriesRequest(t *testing.T) {
	h := &fakeHandler{}
	post(t, h, `{"input_type":"response","litellm_call_id":"c1","model":"m","texts":["ok"],"structured_messages":[{"role":"user","content":"fix it"}]}`)
	req, err := codec.DecodeRequest(h.calls[0].Protocol, h.calls[0].RequestBody)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 1 || req.Messages[0].Text() != "fix it" {
		t.Errorf("request = %+v", req)
	}
}

// Through a real pipeline, a decider that blocks a marker blocks a request whose prompt carries
// it and a reply whose tool call does, and LiteLLM gets BLOCKED with the decider's reason; calls
// without it are allowed.
func TestBlockRenderedThroughPipeline(t *testing.T) {
	p := gatewaytest.Pipeline(t, gatewaytest.Marker{Text: "MARKER-7f3"})
	for _, tc := range []struct {
		name, payload, want string
	}{
		{"clean request", requestPayload, "NONE"},
		{"marked request", `{"input_type":"request","litellm_call_id":"c2","model":"m","structured_messages":[{"role":"user","content":"say MARKER-7f3"}]}`, "BLOCKED"},
		{"clean reply", `{"input_type":"response","litellm_call_id":"c3","model":"m","texts":["ok"]}`, "NONE"},
		{"marked reply", `{"input_type":"response","litellm_call_id":"c4","model":"m","texts":["ok"],
			"tool_calls":[{"id":"t9","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"echo MARKER-7f3\"}"}}]}`, "BLOCKED"},
	} {
		r := post(t, p, tc.payload)
		if r.Action != tc.want || (tc.want == "BLOCKED" && r.BlockedReason != gatewaytest.Reason) {
			t.Errorf("%s: answered %+v, want %s", tc.name, r, tc.want)
		}
	}
}
