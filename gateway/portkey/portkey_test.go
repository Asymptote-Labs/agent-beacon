package portkey

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/internal/gatewaytest"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

type fakeHandler struct {
	verdict gateway.Verdict
	calls   []*gateway.Call
}

func (f *fakeHandler) Admit(context.Context, int64) (func(), gateway.Verdict, bool) {
	return func() {}, gateway.Verdict{}, true
}

func (f *fakeHandler) Handle(_ context.Context, c *gateway.Call) gateway.Verdict {
	f.calls = append(f.calls, c)
	return f.verdict
}

const (
	chatRequest  = `{"model":"gpt-4o","messages":[{"role":"user","content":"list files"}],"stream":false}`
	chatResponse = `{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":"tool_calls"}]}`
)

// payload builds a webhook body the way Portkey's webhook plugin does: the span context spread
// at top level, request headers removed, eventType added.
func payload(event, requestType, req, resp string, streaming bool) string {
	return `{"request":{"json":` + req + `,"text":"list files","isStreamingRequest":` + boolJSON(streaming) +
		`,"isTransformed":false},"response":{"json":` + resp + `,"text":"","statusCode":200,"isTransformed":false},` +
		`"provider":"openai","requestType":"` + requestType + `",` +
		`"metadata":{"_user":"alice","team":"red","session_id":"s-1","tier":3},"eventType":"` + event + `"}`
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func post(t *testing.T, cfg Config, h gateway.Handler, auth, body string) (int, string) {
	t.Helper()
	a, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.(*adapter).handler(h))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/portkey/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, strings.TrimSpace(string(b))
}

func TestWebhookCalls(t *testing.T) {
	identity := gateway.Identity{User: "alice", Team: "red", Key: "3"}
	cfg := Config{IdentityMetadata: IdentityMetadata{User: "_user", Team: "team", Key: "tier"}}
	tests := []struct {
		name     string
		body     string
		phase    gateway.Phase
		protocol llm.Protocol
		wantBody string
	}{
		{"chat request", payload("beforeRequestHook", "chatComplete", chatRequest, `{}`, false),
			gateway.PhaseRequest, llm.OpenAIChat, chatRequest},
		{"chat response", payload("afterRequestHook", "chatComplete", chatRequest, chatResponse, false),
			gateway.PhaseResponse, llm.OpenAIChat, chatResponse},
		{"anthropic messages request", payload("beforeRequestHook", "messages", chatRequest, `{}`, false),
			gateway.PhaseRequest, llm.AnthropicMessages, chatRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &fakeHandler{}
			status, body := post(t, cfg, h, "", tt.body)
			if status != http.StatusOK || body != `{"verdict":true}` {
				t.Fatalf("reply = %d %s, want 200 {\"verdict\":true}", status, body)
			}
			if len(h.calls) != 1 {
				t.Fatalf("Handle called %d times, want 1", len(h.calls))
			}
			c := h.calls[0]
			if c.Gateway != "portkey" || c.Phase != tt.phase || c.Protocol != tt.protocol ||
				!c.Enforce || !c.Normalized || c.Request != nil || c.Response != nil {
				t.Errorf("call = %+v", c)
			}
			if string(c.Body) != tt.wantBody {
				t.Errorf("Body = %s, want %s", c.Body, tt.wantBody)
			}
			if c.Client.Identity != identity || c.Client.Session != "s-1" || len(c.Client.Headers) != 0 {
				t.Errorf("client = %+v", c.Client)
			}
			// The reply's call names the model the request asked for, and carries the request;
			// the request's model is read from its body by the pipeline.
			if want := map[gateway.Phase]string{gateway.PhaseRequest: "", gateway.PhaseResponse: "gpt-4o"}[tt.phase]; c.Model != want {
				t.Errorf("Model = %q, want %q", c.Model, want)
			}
			if want := map[gateway.Phase]string{gateway.PhaseRequest: "", gateway.PhaseResponse: chatRequest}[tt.phase]; string(c.RequestBody) != want {
				t.Errorf("RequestBody = %s, want %s", c.RequestBody, want)
			}
			if c.ID != callID([]byte(chatRequest)) {
				t.Errorf("ID = %q, want hash of request.json", c.ID)
			}
		})
	}
}

// Through a real pipeline, a decider that blocks a marker denies a request or reply that carries
// it, with its reason and rule in the data Portkey reports to the client.
func TestBlockRenderedThroughPipeline(t *testing.T) {
	p := gatewaytest.Pipeline(t, gatewaytest.Marker{Text: "MARKER-7f3"})
	marked := strings.Replace(chatRequest, "list files", "MARKER-7f3", 1)
	markedReply := strings.Replace(chatResponse, `\"ls\"`, `\"echo MARKER-7f3\"`, 1)
	for _, tc := range []struct{ name, body, want string }{
		{"clean request", payload("beforeRequestHook", "chatComplete", chatRequest, `{}`, false), `{"verdict":true}`},
		{"marked request", payload("beforeRequestHook", "chatComplete", marked, `{}`, false),
			`{"verdict":false,"data":{"reason":"` + gatewaytest.Reason + `","rules":["test-marker"]}}`},
		{"marked reply", payload("afterRequestHook", "chatComplete", chatRequest, markedReply, false),
			`{"verdict":false,"data":{"reason":"` + gatewaytest.Reason + `","rules":["test-marker"]}}`},
	} {
		if status, body := post(t, Config{}, p, "", tc.body); status != http.StatusOK || body != tc.want {
			t.Errorf("%s: %d %s, want %s", tc.name, status, body, tc.want)
		}
	}
}

func TestBothPhasesShareID(t *testing.T) {
	h := &fakeHandler{}
	post(t, Config{}, h, "", payload("beforeRequestHook", "chatComplete", chatRequest, `{}`, false))
	post(t, Config{}, h, "", payload("afterRequestHook", "chatComplete", chatRequest, chatResponse, false))
	other := strings.Replace(chatRequest, "list files", "rm files", 1)
	post(t, Config{}, h, "", payload("beforeRequestHook", "chatComplete", other, `{}`, false))
	if len(h.calls) != 3 {
		t.Fatalf("Handle called %d times, want 3", len(h.calls))
	}
	if h.calls[0].ID != h.calls[1].ID {
		t.Errorf("request ID %q != response ID %q", h.calls[0].ID, h.calls[1].ID)
	}
	if h.calls[0].ID == h.calls[2].ID {
		t.Errorf("different requests share ID %q", h.calls[0].ID)
	}
	if h.calls[0].Client.Identity.User != "alice" {
		t.Errorf("default identity user = %q, want alice from _user", h.calls[0].Client.Identity.User)
	}
}

func TestVerdictReply(t *testing.T) {
	tests := []struct {
		name    string
		verdict gateway.Verdict
		want    string
	}{
		{"allow", gateway.Verdict{Action: gateway.Allow}, `{"verdict":true}`},
		{"block", gateway.Verdict{Action: gateway.Block, Reason: "curl piped to shell", Rules: []string{"BCN-001"}},
			`{"verdict":false,"data":{"reason":"curl piped to shell","rules":["BCN-001"]}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &fakeHandler{verdict: tt.verdict}
			status, body := post(t, Config{}, h, "", payload("afterRequestHook", "chatComplete", chatRequest, chatResponse, false))
			if status != http.StatusOK || body != tt.want {
				t.Errorf("reply = %d %s, want 200 %s", status, body, tt.want)
			}
		})
	}
}

func TestSkippedWithoutHandle(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"completion", payload("beforeRequestHook", "complete", `{"model":"gpt-3.5-turbo-instruct","prompt":"hi"}`, `{}`, false)},
		{"embedding", payload("beforeRequestHook", "embed", `{"model":"text-embedding-3-small","input":"hi"}`, `{}`, false)},
		{"streamed response", payload("afterRequestHook", "chatComplete", chatRequest, `null`, true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &fakeHandler{verdict: gateway.Verdict{Action: gateway.Block}}
			status, body := post(t, Config{}, h, "", tt.body)
			if status != http.StatusOK || body != `{"verdict":true}` {
				t.Errorf("reply = %d %s, want 200 {\"verdict\":true}", status, body)
			}
			if len(h.calls) != 0 {
				t.Errorf("Handle called %d times, want 0", len(h.calls))
			}
		})
	}
}

func TestToken(t *testing.T) {
	tests := []struct {
		name   string
		auth   string
		status int
		calls  int
	}{
		{"match", "Bearer s3cret", http.StatusOK, 1},
		{"mismatch", "Bearer wrong", http.StatusUnauthorized, 0},
		{"missing", "", http.StatusUnauthorized, 0},
		{"not bearer", "s3cret", http.StatusUnauthorized, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &fakeHandler{}
			status, _ := post(t, Config{Token: "s3cret"}, h, tt.auth, payload("beforeRequestHook", "chatComplete", chatRequest, `{}`, false))
			if status != tt.status || len(h.calls) != tt.calls {
				t.Errorf("status %d, %d calls; want %d, %d", status, len(h.calls), tt.status, tt.calls)
			}
		})
	}
}

func TestMalformedPayload(t *testing.T) {
	h := &fakeHandler{}
	status, _ := post(t, Config{}, h, "", `{"request":`)
	if status != http.StatusBadRequest || len(h.calls) != 0 {
		t.Errorf("status %d, %d calls; want 400, 0", status, len(h.calls))
	}
}
