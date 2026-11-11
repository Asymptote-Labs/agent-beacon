package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"sync"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/asymptote-labs/agent-beacon/gateway/internal/synth"
)

func dialEnvoy(addr string) (*grpc.ClientConn, error) {
	cc, err := grpc.NewClient("passthrough:///"+addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64<<20), grpc.MaxCallSendMsgSize(64<<20)),
		grpc.WithInitialWindowSize(1<<20), grpc.WithInitialConnWindowSize(16<<20))
	if err != nil {
		return nil, err
	}
	cc.Connect()
	return cc, nil
}

type envoyConn struct {
	client extprocv3.ExternalProcessorClient
}

// envoyMsgs are a worker's reusable ext_proc messages. SendMsg marshals before it returns, so a
// message can be refilled for the next send.
type envoyMsgs struct {
	reqHeaders           *extprocv3.ProcessingRequest
	path, reqID, session *corev3.HeaderValue
	reqBody              *extprocv3.ProcessingRequest
	respBody             *extprocv3.ProcessingRequest
	resp                 extprocv3.ProcessingResponse
	scratch              [][]byte
}

func hv(k, v string) *corev3.HeaderValue { return &corev3.HeaderValue{Key: k, RawValue: []byte(v)} }

const modelPath = "/v1/messages?beta=true"

func newEnvoyMsgs() *envoyMsgs {
	m := &envoyMsgs{path: hv(":path", modelPath), reqID: hv("x-request-id", ""), session: hv("x-claude-code-session-id", "")}
	// A Claude Code request's headers as Envoy forwards them.
	hdrs := []*corev3.HeaderValue{
		hv(":method", "POST"), m.path, hv(":authority", "api.anthropic.com"), hv(":scheme", "https"),
		hv("content-type", "application/json"), hv("user-agent", synth.UserAgent+" (external, cli)"),
		m.reqID, hv("x-api-key", "sk-ant-stress-0000"), hv("anthropic-version", "2023-06-01"),
		hv("anthropic-beta", "claude-code-20250219,interleaved-thinking-2025-05-14"), m.session,
		hv("x-forwarded-for", "10.0.0.7, 172.17.0.1"), hv("x-user", "stress-user"),
	}
	m.reqHeaders = &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestHeaders{
		RequestHeaders: &extprocv3.HttpHeaders{Headers: &corev3.HeaderMap{Headers: hdrs}},
	}}
	m.reqBody = &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestBody{RequestBody: &extprocv3.HttpBody{}}}
	m.respBody = &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseBody{ResponseBody: &extprocv3.HttpBody{}}}
	return m
}

func respHeaders(kv ...string) *extprocv3.ProcessingRequest {
	m := &corev3.HeaderMap{}
	for i := 0; i < len(kv); i += 2 {
		m.Headers = append(m.Headers, hv(kv[i], kv[i+1]))
	}
	return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{
		ResponseHeaders: &extprocv3.HttpHeaders{Headers: m},
	}}
}

// Response headers are read-only once built, so workers share them.
var (
	respJSON = respHeaders(":status", "200", "content-type", "application/json", "request-id", "req_stress")
	respSSE  = respHeaders(":status", "200", "content-type", "text/event-stream; charset=utf-8", "cache-control", "no-cache")
	respGzip = respHeaders(":status", "200", "content-type", "application/json", "content-encoding", "gzip")
)

// bodyPool holds contiguous request bodies only for the duration of a send.
var bodyPool = sync.Pool{New: func() any { return new([]byte) }}

// gzipBomb inflates past the adapter's default max_body (32 MiB) from a few tens of KB.
var gzipBomb = sync.OnceValue(func() []byte {
	var b bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	line := bytes.Repeat([]byte(`{"type":"text","text":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), 1024)
	for n := 0; n < 40<<20; n += len(line) {
		zw.Write(line)
	}
	zw.Close()
	return b.Bytes()
})

type answer uint8

const (
	aOther answer = iota
	aReqHeaders
	aReqBody
	aRespHeaders
	aRespBody
	aImmediate
)

func kindOf(r *extprocv3.ProcessingResponse) answer {
	switch r.Response.(type) {
	case *extprocv3.ProcessingResponse_RequestHeaders:
		return aReqHeaders
	case *extprocv3.ProcessingResponse_RequestBody:
		return aReqBody
	case *extprocv3.ProcessingResponse_ResponseHeaders:
		return aRespHeaders
	case *extprocv3.ProcessingResponse_ResponseBody:
		return aRespBody
	case *extprocv3.ProcessingResponse_ImmediateResponse:
		return aImmediate
	}
	return aOther
}

type procStream = grpc.BidiStreamingClient[extprocv3.ProcessingRequest, extprocv3.ProcessingResponse]

// send sends m and waits for its answer, as Envoy does.
func (w *worker) send(st procStream, m *extprocv3.ProcessingRequest) (answer, error) {
	if err := st.SendMsg(m); err != nil {
		if errors.Is(err, io.EOF) {
			// The stream ended; its status says why.
			if err = st.RecvMsg(&w.ev.resp); err == nil {
				err = io.ErrUnexpectedEOF
			}
		}
		return aOther, err
	}
	if err := st.RecvMsg(&w.ev.resp); err != nil {
		return aOther, err
	}
	return kindOf(&w.ev.resp), nil
}

// expectAnswer counts a protocol violation when got is not want; an immediate response is
// checked for the configured block status.
func (w *worker) expectAnswer(got, want answer) bool {
	if got == want {
		if got == aImmediate && w.ev.resp.GetImmediateResponse().GetStatus().GetCode() != 403 {
			w.sh.viol[tEnvoy][vImmediate].Add(1)
			w.run.m.sample("envoy "+violationNames[vImmediate], w.ev.resp.String())
		}
		return true
	}
	w.sh.viol[tEnvoy][vMessageKind].Add(1)
	w.run.m.sample("envoy "+violationNames[vMessageKind], w.ev.resp.String())
	return false
}

// finish half-closes the stream and expects the server to end it, as the adapter's tests do.
func (w *worker) finish(st procStream) bool {
	if err := st.CloseSend(); err != nil {
		w.run.m.recordErr(w.sh, tEnvoy, err)
		return false
	}
	if err := st.RecvMsg(&w.ev.resp); !errors.Is(err, io.EOF) {
		if err != nil {
			w.run.m.recordErr(w.sh, tEnvoy, err)
		} else {
			w.sh.viol[tEnvoy][vStreamEnd].Add(1)
		}
		return false
	}
	return true
}

func (w *worker) setRequestHeaders(callID, session string) {
	w.ev.reqID.RawValue = append(w.ev.reqID.RawValue[:0], callID...)
	w.ev.session.RawValue = append(w.ev.session.RawValue[:0], session...)
}

// sendRequestBody joins pieces into a pooled buffer and sends them as one request_body.
func (w *worker) sendRequestBody(st procStream, pieces [][]byte, end bool) (answer, error) {
	buf := bodyPool.Get().(*[]byte)
	*buf = synth.AppendJoin((*buf)[:0], pieces)
	rb := w.ev.reqBody.GetRequestBody()
	rb.Body, rb.EndOfStream = *buf, end
	a, err := w.send(st, w.ev.reqBody)
	rb.Body = nil
	bodyPool.Put(buf)
	return a, err
}

// envoyTurn plays one turn as one ext_proc stream: request_headers → request_body → response
// headers → response body (one message when BUFFERED, 1-64 byte chunks when STREAMED).
func (w *worker) envoyTurn(ctx context.Context, p turnPlan) {
	cctx, cancel := context.WithTimeout(ctx, w.run.cfg.timeout)
	defer cancel()
	m := w.run.m
	ex := w.begin()
	ok := false
	defer func() { w.end(ex, phExchange, ok) }()

	st, err := w.envoy.client.Process(cctx)
	if err != nil {
		m.recordErr(w.sh, tEnvoy, err)
		return
	}
	w.setRequestHeaders(p.callID, p.sessionKey)
	a, err := w.send(st, w.ev.reqHeaders)
	if err != nil {
		m.recordErr(w.sh, tEnvoy, err)
		return
	}
	if !w.expectAnswer(a, aReqHeaders) {
		return
	}

	user := append([]byte("user_stress_"), p.sessionKey...)
	w.ev.scratch = p.s.script.AnthropicRequest(w.ev.scratch[:0], p.k, user, p.reqBad)
	t0 := time.Now()
	a, err = w.sendRequestBody(st, w.ev.scratch, true)
	if err != nil {
		m.recordErr(w.sh, tEnvoy, err)
		return
	}
	w.sh.hist[tEnvoy][phRequest].record(time.Since(t0))
	if a == aImmediate {
		w.expectAnswer(a, aImmediate)
		w.sh.judge(tEnvoy, p.expReq, true)
		ok = w.finish(st)
		return
	}
	if !w.expectAnswer(a, aReqBody) {
		return
	}
	w.sh.judge(tEnvoy, p.expReq, false)

	streamed := w.run.cfg.scenario == "envoy-streamed"
	headers, reply := respJSON, p.s.script.AnthropicResponse(p.k, p.respBad)
	if streamed || p.s.alt {
		headers, reply = respSSE, p.s.script.SSE(p.k, p.respBad, streamed)
	}
	if a, err = w.send(st, headers); err != nil {
		m.recordErr(w.sh, tEnvoy, err)
		return
	}
	if !w.expectAnswer(a, aRespHeaders) {
		return
	}

	rb := w.ev.respBody.GetResponseBody()
	t0 = time.Now()
	blocked := false
	for off := 0; off < len(reply); {
		n := len(reply) - off
		if streamed {
			n = min(n, 1+w.r.IntN(64))
		}
		rb.Body, rb.EndOfStream = reply[off:off+n], off+n == len(reply)
		off += n
		if a, err = w.send(st, w.ev.respBody); err != nil {
			m.recordErr(w.sh, tEnvoy, err)
			return
		}
		if a == aImmediate {
			w.expectAnswer(a, aImmediate)
			blocked = true
			if streamed {
				// The adapter documents a streamed reply as recorded, not enforced.
				w.sh.viol[tEnvoy][vMessageKind].Add(1)
				m.sample("envoy immediate response to a STREAMED chunk", w.ev.resp.String())
			}
			break
		}
		if !w.expectAnswer(a, aRespBody) {
			return
		}
	}
	rb.Body = nil
	w.sh.hist[tEnvoy][phResponse].record(time.Since(t0))
	w.sh.judge(tEnvoy, p.expResp, blocked)
	ok = w.finish(st)
}

// ---- chaos over ext_proc ----

func (w *worker) envoySmallBody() []byte {
	return []byte(`{"model":"` + synth.Model + `","max_tokens":1024,"messages":[{"role":"user","content":"` +
		string(bytes.Repeat([]byte(chaosPad), 40)) + `"}]}`)
}

func (w *worker) envoyChaos(ctx context.Context, a chaosAction) {
	m := w.run.m
	name := chaosNames[a]
	timeout := w.run.cfg.timeout
	if a == chaosSlowloris {
		timeout = max(timeout, 30*time.Second)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := w.begin()
	done := false
	outcome := func(s string, expected bool) {
		w.end(start, phChaos, expected)
		m.chaosOutcome(tEnvoy, name, s, expected)
		done = true
	}
	fail := func(err error) { outcome("error: "+m.recordErr(w.sh, tEnvoy, err), false) }
	defer func() {
		if !done {
			outcome("protocol violation", false)
		}
	}()

	st, err := w.envoy.client.Process(cctx)
	if err != nil {
		fail(err)
		return
	}
	cid, sid := w.chaosIDs()
	w.setRequestHeaders(cid, sid)
	if a == chaosUnknownPath {
		w.ev.path.RawValue = []byte("/v1/stress-unknown-path")
	}
	ans, err := w.send(st, w.ev.reqHeaders)
	w.ev.path.RawValue = []byte(modelPath)
	if err != nil {
		fail(err)
		return
	}
	if !w.expectAnswer(ans, aReqHeaders) {
		return
	}
	body := w.envoySmallBody()

	switch a {
	case chaosCancel:
		half := w.r.IntN(2) == 0
		where := "after headers"
		if half {
			where = "mid-body"
			if ans, err = w.sendRequestBody(st, [][]byte{body[:len(body)/2]}, false); err != nil {
				fail(err)
				return
			}
			if !w.expectAnswer(ans, aReqBody) {
				return
			}
		}
		name = "cancel stream " + where
		cancel()
		outcome("stream cancelled by client", true)
		return
	case chaosSlowloris:
		for off := 0; off < len(body); off += 1024 {
			if off > 0 {
				select {
				case <-time.After(time.Second):
				case <-cctx.Done():
					fail(cctx.Err())
					return
				}
			}
			end := min(off+1024, len(body))
			if ans, err = w.sendRequestBody(st, [][]byte{body[off:end]}, end == len(body)); err != nil {
				fail(err)
				return
			}
			if !w.expectAnswer(ans, aReqBody) {
				return
			}
		}
	case chaosMalformed:
		p := w.plan()
		w.ev.scratch = p.s.script.AnthropicRequest(w.ev.scratch[:0], p.k, []byte("user_stress_"+sid), synth.Benign)
		if ans, err = w.sendRequestBody(st, truncate(w.ev.scratch, size(w.ev.scratch)/2), true); err != nil {
			fail(err)
			return
		}
		// The pipeline allows a body it cannot decode.
		if !w.expectAnswer(ans, aReqBody) {
			return
		}
	case chaosUnknownPath, chaosGzipBomb:
		if ans, err = w.sendRequestBody(st, [][]byte{body}, true); err != nil {
			fail(err)
			return
		}
		if !w.expectAnswer(ans, aReqBody) {
			return
		}
	}
	if a == chaosGzipBomb {
		if ans, err = w.send(st, respGzip); err != nil {
			fail(err)
			return
		}
		if !w.expectAnswer(ans, aRespHeaders) {
			return
		}
		rb := w.ev.respBody.GetResponseBody()
		rb.Body, rb.EndOfStream = gzipBomb(), true
		ans, err = w.send(st, w.ev.respBody)
		rb.Body = nil
		if err != nil {
			fail(err)
			return
		}
		// Past max_body after inflating: passed through unanalyzed.
		if !w.expectAnswer(ans, aRespBody) {
			return
		}
	}
	if !w.finish(st) {
		outcome("stream did not end cleanly", false)
		return
	}
	outcome("continue, stream ended cleanly", true)
}
