package envoy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/internal/gatewaytest"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// recorder is a Handler that records calls and answers with a scripted verdict.
type recorder struct {
	mu      sync.Mutex
	calls   []gateway.Call
	verdict func(*gateway.Call) gateway.Verdict
}

func (r *recorder) Admit(context.Context, int64) (func(), gateway.Verdict, bool) {
	return func() {}, gateway.Verdict{}, true
}

func (r *recorder) Handle(_ context.Context, c *gateway.Call) gateway.Verdict {
	r.mu.Lock()
	r.calls = append(r.calls, *c)
	r.mu.Unlock()
	if r.verdict == nil {
		return gateway.Verdict{}
	}
	return r.verdict(c)
}

func blockOn(phase gateway.Phase, reason string) func(*gateway.Call) gateway.Verdict {
	return func(c *gateway.Call) gateway.Verdict {
		if c.Phase != phase {
			return gateway.Verdict{}
		}
		return gateway.Verdict{Action: gateway.Block, Reason: reason, Rules: []string{"rule-1"}}
	}
}

// exchangeWith plays one HTTP exchange through the adapter as Envoy does: each message is sent
// and its answer awaited before the next.
func exchangeWith(t *testing.T, cfg Config, h gateway.Handler, msgs []*extprocv3.ProcessingRequest) []*extprocv3.ProcessingResponse {
	t.Helper()
	a, err := New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- a.(*Adapter).serve(ctx, lis, h) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("serve: %v", err)
		}
	})

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stream, err := extprocv3.NewExternalProcessorClient(conn).Process(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var resps []*extprocv3.ProcessingResponse
	for _, m := range msgs {
		if err := stream.Send(m); err != nil {
			t.Fatalf("send: %v", err)
		}
		resp, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		resps = append(resps, resp)
		// Envoy ends the exchange on an immediate response.
		if resp.GetImmediateResponse() != nil {
			break
		}
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("stream end: %v, want EOF", err)
	}
	return resps
}

func headerMap(kv ...string) *corev3.HeaderMap {
	m := &corev3.HeaderMap{}
	for i := 0; i < len(kv); i += 2 {
		m.Headers = append(m.Headers, &corev3.HeaderValue{Key: kv[i], RawValue: []byte(kv[i+1])})
	}
	return m
}

func reqHeaders(kv ...string) *extprocv3.ProcessingRequest {
	return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestHeaders{
		RequestHeaders: &extprocv3.HttpHeaders{Headers: headerMap(kv...)},
	}}
}

func reqBody(b string) *extprocv3.ProcessingRequest {
	return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestBody{
		RequestBody: &extprocv3.HttpBody{Body: []byte(b), EndOfStream: true},
	}}
}

func respHeaders(kv ...string) *extprocv3.ProcessingRequest {
	return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{
		ResponseHeaders: &extprocv3.HttpHeaders{Headers: headerMap(kv...)},
	}}
}

func respBody(b string, end bool) *extprocv3.ProcessingRequest {
	return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseBody{
		ResponseBody: &extprocv3.HttpBody{Body: []byte(b), EndOfStream: end},
	}}
}

// kinds names each answer, with "continue" for a pass-through and "immediate" for a reply.
func kinds(resps []*extprocv3.ProcessingResponse) []string {
	var out []string
	for _, r := range resps {
		var common *extprocv3.CommonResponse
		switch v := r.Response.(type) {
		case *extprocv3.ProcessingResponse_ImmediateResponse:
			out = append(out, "immediate")
			continue
		case *extprocv3.ProcessingResponse_RequestHeaders:
			common = v.RequestHeaders.GetResponse()
		case *extprocv3.ProcessingResponse_ResponseHeaders:
			common = v.ResponseHeaders.GetResponse()
		case *extprocv3.ProcessingResponse_RequestBody:
			common = v.RequestBody.GetResponse()
		case *extprocv3.ProcessingResponse_ResponseBody:
			common = v.ResponseBody.GetResponse()
		default:
			out = append(out, "unexpected")
			continue
		}
		if common.GetStatus() != extprocv3.CommonResponse_CONTINUE || common.GetHeaderMutation() != nil || common.GetBodyMutation() != nil {
			out = append(out, "mutated")
			continue
		}
		out = append(out, "continue")
	}
	return out
}

func gzipped(s string) string {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.String()
}

const (
	messagesReq = `{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	messagesRep = `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}]}`
)

// modelHeaders are a Claude Code request's headers as Envoy forwards them.
var modelHeaders = []string{
	":method", "POST",
	":path", "/v1/messages?beta=true",
	":authority", "api.anthropic.com",
	"user-agent", "claude-cli/2.0.1 (external, cli)",
	"x-request-id", "req-1",
	"x-api-key", "sk-ant-secret",
	"authorization", "Bearer sk-ant-secret",
	"anthropic-version", "2023-06-01",
	"X-Claude-Code-Session-Id", "sess-1",
	"x-forwarded-for", "10.0.0.7, 172.17.0.1",
	"x-user", "alice",
}

type phaseCall struct {
	phase   gateway.Phase
	enforce bool
	body    string
}

func TestExchange(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		verdict func(*gateway.Call) gateway.Verdict
		msgs    []*extprocv3.ProcessingRequest
		answers []string
		calls   []phaseCall
	}{
		{
			name: "allowed model call",
			msgs: []*extprocv3.ProcessingRequest{
				reqHeaders(modelHeaders...), reqBody(messagesReq),
				respHeaders(":status", "200", "content-type", "application/json"), respBody(messagesRep, true),
			},
			answers: []string{"continue", "continue", "continue", "continue"},
			calls: []phaseCall{
				{gateway.PhaseRequest, true, messagesReq},
				{gateway.PhaseResponse, true, messagesRep},
			},
		},
		{
			name:    "blocked request",
			verdict: blockOn(gateway.PhaseRequest, "prompt injection"),
			msgs: []*extprocv3.ProcessingRequest{
				reqHeaders(modelHeaders...), reqBody(messagesReq),
				respHeaders(":status", "200"), respBody(messagesRep, true),
			},
			answers: []string{"continue", "immediate"},
			calls:   []phaseCall{{gateway.PhaseRequest, true, messagesReq}},
		},
		{
			name:    "buffered reply blocked",
			verdict: blockOn(gateway.PhaseResponse, "exfiltration"),
			msgs: []*extprocv3.ProcessingRequest{
				reqHeaders(modelHeaders...), reqBody(messagesReq),
				respHeaders(":status", "200"), respBody(messagesRep, true),
			},
			answers: []string{"continue", "continue", "continue", "immediate"},
			calls: []phaseCall{
				{gateway.PhaseRequest, true, messagesReq},
				{gateway.PhaseResponse, true, messagesRep},
			},
		},
		{
			name:    "streamed reply is recorded, not enforced",
			verdict: blockOn(gateway.PhaseResponse, "exfiltration"),
			msgs: []*extprocv3.ProcessingRequest{
				reqHeaders(modelHeaders...), reqBody(messagesReq),
				respHeaders(":status", "200", "content-type", "text/event-stream"),
				respBody("event: message_start\n", false),
				respBody("data: {}\n\n", false),
				respBody("event: message_stop\ndata: {}\n\n", true),
			},
			answers: []string{"continue", "continue", "continue", "continue", "continue", "continue"},
			calls: []phaseCall{
				{gateway.PhaseRequest, true, messagesReq},
				{gateway.PhaseResponse, false, "event: message_start\ndata: {}\n\nevent: message_stop\ndata: {}\n\n"},
			},
		},
		{
			name: "model listing passes",
			msgs: []*extprocv3.ProcessingRequest{
				reqHeaders(":method", "GET", ":path", "/v1/models", "x-request-id", "req-2"),
				respHeaders(":status", "200"), respBody(`{"data":[]}`, true),
			},
			answers: []string{"continue", "continue", "continue"},
		},
		{
			name: "listing stored completions passes",
			msgs: []*extprocv3.ProcessingRequest{
				reqHeaders(":method", "GET", ":path", "/v1/chat/completions"),
				respHeaders(":status", "200"), respBody(`{"data":[]}`, true),
			},
			answers: []string{"continue", "continue", "continue"},
		},
		{
			name: "gzip reply decompressed",
			msgs: []*extprocv3.ProcessingRequest{
				reqHeaders(modelHeaders...), reqBody(messagesReq),
				respHeaders(":status", "200", "content-encoding", "gzip"), respBody(gzipped(messagesRep), true),
			},
			answers: []string{"continue", "continue", "continue", "continue"},
			calls: []phaseCall{
				{gateway.PhaseRequest, true, messagesReq},
				{gateway.PhaseResponse, true, messagesRep},
			},
		},
		{
			name: "error reply not analyzed",
			msgs: []*extprocv3.ProcessingRequest{
				reqHeaders(modelHeaders...), reqBody(messagesReq),
				respHeaders(":status", "429"), respBody(`{"type":"error"}`, true),
			},
			answers: []string{"continue", "continue", "continue", "continue"},
			calls:   []phaseCall{{gateway.PhaseRequest, true, messagesReq}},
		},
		{
			name: "body over max_body not analyzed",
			cfg:  Config{MaxBody: len(messagesRep)},
			msgs: []*extprocv3.ProcessingRequest{
				reqHeaders(modelHeaders...), reqBody(messagesReq + strings.Repeat(" ", len(messagesRep))),
				respHeaders(":status", "200"), respBody(messagesRep, true),
			},
			answers: []string{"continue", "continue", "continue", "continue"},
			calls:   []phaseCall{{gateway.PhaseResponse, true, messagesRep}},
		},
		{
			name: "gzip bomb over max_body not analyzed",
			cfg:  Config{MaxBody: 1 << 10},
			msgs: []*extprocv3.ProcessingRequest{
				reqHeaders(modelHeaders...), reqBody(messagesReq),
				respHeaders(":status", "200", "content-encoding", "gzip"), respBody(gzipped(strings.Repeat("a", 1<<20)), true),
			},
			answers: []string{"continue", "continue", "continue", "continue"},
			calls:   []phaseCall{{gateway.PhaseRequest, true, messagesReq}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{verdict: tc.verdict}
			got := kinds(exchangeWith(t, tc.cfg, rec, tc.msgs))
			if strings.Join(got, ",") != strings.Join(tc.answers, ",") {
				t.Errorf("answers %v, want %v", got, tc.answers)
			}
			if len(rec.calls) != len(tc.calls) {
				t.Fatalf("got %d calls, want %d", len(rec.calls), len(tc.calls))
			}
			for i, want := range tc.calls {
				c := rec.calls[i]
				if c.Phase != want.phase || c.Enforce != want.enforce || string(c.Body) != want.body {
					t.Errorf("call %d: phase %v enforce %v body %q, want %v %v %q", i, c.Phase, c.Enforce, c.Body, want.phase, want.enforce, want.body)
				}
				if c.Gateway != "envoy" || c.ID != "req-1" || c.Protocol != llm.AnthropicMessages {
					t.Errorf("call %d: gateway %q id %q protocol %q", i, c.Gateway, c.ID, c.Protocol)
				}
				// A reply carries the body of the request analyzed before it, if any.
				wantReq := ""
				if want.phase == gateway.PhaseResponse && i > 0 {
					wantReq = string(rec.calls[0].Body)
				}
				if string(c.RequestBody) != wantReq {
					t.Errorf("call %d: RequestBody %q, want %q", i, c.RequestBody, wantReq)
				}
			}
		})
	}
}

func TestRequestClient(t *testing.T) {
	rec := &recorder{}
	cfg := Config{IdentityHeaders: IdentityHeaders{User: "X-User"}}
	exchangeWith(t, cfg, rec, []*extprocv3.ProcessingRequest{reqHeaders(modelHeaders...), reqBody(messagesReq)})
	if len(rec.calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(rec.calls))
	}
	c := rec.calls[0].Client
	want := map[string]string{
		"user-agent":               "claude-cli/2.0.1 (external, cli)",
		"x-request-id":             "req-1",
		"anthropic-version":        "2023-06-01",
		"x-claude-code-session-id": "sess-1",
		"x-forwarded-for":          "10.0.0.7, 172.17.0.1",
		"x-user":                   "alice",
	}
	if !maps.Equal(c.Headers, want) {
		t.Errorf("headers %v, want %v", c.Headers, want)
	}
	if c.UserAgent != "claude-cli/2.0.1 (external, cli)" || c.Address != "10.0.0.7" || c.Identity.User != "alice" {
		t.Errorf("client %+v", c)
	}
}

func TestGeneratedIDSpansPhases(t *testing.T) {
	rec := &recorder{}
	exchangeWith(t, Config{}, rec, []*extprocv3.ProcessingRequest{
		reqHeaders(":method", "POST", ":path", "/v1/chat/completions"), reqBody(`{"model":"gpt-4o","messages":[]}`),
		respHeaders(":status", "200"), respBody(`{"id":"c1","choices":[]}`, true),
	})
	if len(rec.calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(rec.calls))
	}
	if id := rec.calls[0].ID; id == "" || rec.calls[1].ID != id || rec.calls[0].Protocol != llm.OpenAIChat {
		t.Errorf("ids %q %q protocol %q", rec.calls[0].ID, rec.calls[1].ID, rec.calls[0].Protocol)
	}
}

func TestBlockedReply(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		reason   string
		wantBody string
	}{
		{
			name:     "anthropic",
			path:     "/v1/messages",
			reason:   "prompt injection",
			wantBody: `{"error":{"message":"prompt injection","type":"permission_error"},"type":"error"}`,
		},
		{
			name:     "openai chat",
			path:     "/openai/v1/chat/completions",
			reason:   "prompt injection",
			wantBody: `{"error":{"code":null,"message":"prompt injection","param":null,"type":"invalid_request_error"}}`,
		},
		{
			name:     "no reason given",
			path:     "/v1/messages",
			wantBody: `{"error":{"message":"blocked by Beacon gateway policy","type":"permission_error"},"type":"error"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{verdict: func(*gateway.Call) gateway.Verdict {
				return gateway.Verdict{Action: gateway.Block, Reason: tc.reason}
			}}
			resps := exchangeWith(t, Config{}, rec, []*extprocv3.ProcessingRequest{
				reqHeaders(":method", "POST", ":path", tc.path), reqBody(`{}`),
			})
			ir := resps[len(resps)-1].GetImmediateResponse()
			if ir == nil {
				t.Fatalf("last answer %v, want an immediate response", resps[len(resps)-1])
			}
			if ir.GetStatus().GetCode() != 403 || ir.GetDetails() != "beacon_gateway_blocked" {
				t.Errorf("status %v details %q", ir.GetStatus().GetCode(), ir.GetDetails())
			}
			set := ir.GetHeaders().GetSetHeaders()
			if len(set) != 1 || set[0].GetHeader().GetKey() != "content-type" || string(set[0].GetHeader().GetRawValue()) != "application/json" {
				t.Errorf("headers %v", set)
			}
			var got any
			if err := json.Unmarshal(ir.GetBody(), &got); err != nil {
				t.Fatalf("body %q: %v", ir.GetBody(), err)
			}
			// Re-encoding sorts the keys, so the comparison ignores field order.
			if g, _ := json.Marshal(got); string(g) != tc.wantBody {
				t.Errorf("body %s, want %s", g, tc.wantBody)
			}
		})
	}
}

// Through a real pipeline, a decider that blocks a marker replaces a BUFFERED request or reply
// that carries it with the provider-shaped error, and a STREAMED reply, which the client already
// holds, is let through.
func TestBlockRenderedThroughPipeline(t *testing.T) {
	p := gatewaytest.Pipeline(t, gatewaytest.Marker{Text: "MARKER-7f3"})
	headers := reqHeaders(":method", "POST", ":path", "/v1/messages", "user-agent", "claude-cli/2.1.306")
	clean := reqBody(`{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`)
	marked := `{"id":"msg_1","model":"claude-x-1","content":[{"type":"text","text":"MARKER-7f3"}]}`
	for _, tc := range []struct {
		name string
		msgs []*extprocv3.ProcessingRequest
		want []string
	}{
		{"marked request", []*extprocv3.ProcessingRequest{headers, reqBody(`{"model":"claude-x","messages":[{"role":"user","content":"MARKER-7f3"}]}`)},
			[]string{"continue", "immediate"}},
		{"marked buffered reply", []*extprocv3.ProcessingRequest{headers, clean, respHeaders(":status", "200"), respBody(marked, true)},
			[]string{"continue", "continue", "continue", "immediate"}},
		{"marked streamed reply", []*extprocv3.ProcessingRequest{headers, clean, respHeaders(":status", "200"), respBody(marked[:20], false), respBody(marked[20:], true)},
			[]string{"continue", "continue", "continue", "continue", "continue"}},
		{"clean exchange", []*extprocv3.ProcessingRequest{headers, clean, respHeaders(":status", "200"), respBody(`{"id":"m","model":"x","content":[{"type":"text","text":"ok"}]}`, true)},
			[]string{"continue", "continue", "continue", "continue"}},
	} {
		resps := exchangeWith(t, Config{}, p, tc.msgs)
		if got := kinds(resps); !slices.Equal(got, tc.want) {
			t.Errorf("%s: answers %v, want %v", tc.name, got, tc.want)
			continue
		}
		if ir := resps[len(resps)-1].GetImmediateResponse(); ir != nil && !strings.Contains(string(ir.GetBody()), gatewaytest.Reason) {
			t.Errorf("%s: block body %s", tc.name, ir.GetBody())
		}
	}
}

// The reply's call names the model the request asked for, so both phases' events name one model.
func TestModelSpansPhases(t *testing.T) {
	rec := &recorder{}
	exchangeWith(t, Config{}, rec, []*extprocv3.ProcessingRequest{
		reqHeaders(":method", "POST", ":path", "/v1/messages"),
		reqBody(`{"model":"claude-x","messages":[]}`),
		respHeaders(":status", "200"),
		respBody(`{"id":"m","model":"claude-x-20260101","content":[]}`, true),
	})
	if len(rec.calls) != 2 || rec.calls[0].Model != "claude-x" || rec.calls[1].Model != "claude-x" {
		t.Errorf("calls %+v", rec.calls)
	}
}

func TestNewValidates(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"success status", Config{BlockStatus: 200}},
		{"undefined status", Config{BlockStatus: 499}},
		{"negative max_body", Config{MaxBody: -1}},
		{"credential identity header", Config{IdentityHeaders: IdentityHeaders{Key: "X-Api-Key"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg, slog.New(slog.DiscardHandler)); err == nil {
				t.Error("New accepted an invalid config")
			}
		})
	}
}
