package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/asymptote-labs/agent-beacon/gateway/internal/synth"
)

// httpConn is a worker's route to one listener: the transport it shares with other workers and
// the URL of the adapter's endpoint.
type httpConn struct {
	rt   http.RoundTripper
	url  *url.URL
	host string
	h2   bool
}

var jsonHeader = http.Header{"Content-Type": {"application/json"}}

// newTransports builds, per listener port, the transports workers share. HTTP/1.1 spreads -conns
// connections over up to 16 transports per port (a Transport serializes its connection pool on
// one mutex); h2c gives each of the -conns connections its own Transport so the pool size is
// exact, and queues streams past the server's MaxConcurrentStreams instead of dialing more.
func newTransports(proto string, conns int) []http.RoundTripper {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	var out []http.RoundTripper
	if proto == "h2c" {
		for range conns {
			var p http.Protocols
			p.SetUnencryptedHTTP2(true)
			out = append(out, &http.Transport{
				Proxy: nil, DialContext: dialer.DialContext, Protocols: &p,
				MaxConnsPerHost: 1, MaxIdleConnsPerHost: 1, DisableCompression: true,
				HTTP2: &http.HTTP2Config{StrictMaxConcurrentRequests: true, MaxReceiveBufferPerConnection: 2 << 20},
			})
		}
		return out
	}
	shards := min(conns, 16)
	per := (conns + shards - 1) / shards
	for range shards {
		var p http.Protocols
		p.SetHTTP1(true)
		out = append(out, &http.Transport{
			Proxy: nil, DialContext: dialer.DialContext, Protocols: &p,
			MaxConnsPerHost: per, MaxIdleConnsPerHost: per, MaxIdleConns: per * 2,
			// Default 4 KB read/write buffers: at 70k connections larger ones cost gigabytes, and
			// piecesReader's WriteTo hands large pieces past the write buffer anyway.
			IdleConnTimeout: 5 * time.Minute, DisableCompression: true,
		})
	}
	return out
}

// piecesReader streams a body held as pieces without joining them.
type piecesReader struct {
	p   [][]byte
	off int
}

func (r *piecesReader) Read(b []byte) (int, error) {
	n := 0
	for n < len(b) && len(r.p) > 0 {
		c := copy(b[n:], r.p[0][r.off:])
		n += c
		r.off += c
		if r.off == len(r.p[0]) {
			r.p, r.off = r.p[1:], 0
		}
	}
	if n == 0 && len(r.p) == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (r *piecesReader) WriteTo(w io.Writer) (int64, error) {
	var n int64
	for len(r.p) > 0 {
		c, err := w.Write(r.p[0][r.off:])
		n += int64(c)
		if err != nil {
			return n, err
		}
		r.p, r.off = r.p[1:], 0
	}
	return n, nil
}

func (r *piecesReader) Close() error { return nil }

func size(pieces [][]byte) int {
	n := 0
	for _, p := range pieces {
		n += len(p)
	}
	return n
}

// truncate keeps the first n bytes of pieces.
func truncate(pieces [][]byte, n int) [][]byte {
	var out [][]byte
	for _, p := range pieces {
		if n <= 0 {
			break
		}
		c := min(n, len(p))
		out = append(out, p[:c])
		n -= c
	}
	return out
}

// ---- envelopes ----

// Static envelope text. Dynamic parts (call id, session id) are written per call.
var (
	llHeaders = `"request_headers":{"user-agent":"` + synth.UserAgent + `","anthropic-version":"2023-06-01","authorization":"[present]","x-claude-code-session-id":"`
	llData    = `"},"request_data":{"user_api_key_hash":"88dc28d0f030c55ed4ab77ed8faf098196cb1c05df778539800c9f1243fe6b4b","user_api_key_alias":"stress-key","user_api_key_user_id":"stress-user","user_api_key_team_id":"stress-team","user_api_key_org_id":"stress-org"}}`
	llModel   = `","model":"` + synth.Model + `",`

	pkTail    = `,"text":"","statusCode":0,"isTransformed":false},`
	pkMeta    = `"metadata":{"_user":"stress-user","team":"stress-team","session_id":"`
	pkFirst   = `,"text":` + strconv.Quote(synth.FirstPrompt) + `,"isStreamingRequest":false,"isTransformed":false},"response":{"json":`
	pkOpenAI  = `"provider":"openai","requestType":"chatComplete",`
	pkAnthro  = `"provider":"anthropic","requestType":"messages",`
	pkBefore  = `"},"eventType":"beforeRequestHook"}`
	pkAfter   = `"},"eventType":"afterRequestHook"}`
	pkResp200 = `,"text":"","statusCode":200,"isTransformed":false},`
)

func b(s string) []byte { return []byte(s) }

var (
	bLLReq       = b(`{"input_type":"request","litellm_call_id":"`)
	bLLResp      = b(`{"input_type":"response","litellm_call_id":"`)
	bLLTrace     = b(`","litellm_trace_id":"`)
	bLLModelMsgs = b(llModel + `"structured_messages":[`)
	bLLModelText = b(llModel + `"texts":[`)
	bLLTools     = b(`],"tools":`)
	bLLTexts     = b(`,"texts":[`)
	bLLCalls     = b(`],"tool_calls":`)
	bLLHeadersR  = b(`],` + llHeaders)
	bLLHeaders   = b(`,` + llHeaders)
	bLLData      = b(llData)
	bPKReq       = b(`{"request":{"json":`)
	bPKFirst     = b(pkFirst)
	bPKNull      = b(`null` + pkTail)
	bPK200       = b(pkResp200)
	bPKOpenAI    = b(pkOpenAI + pkMeta)
	bPKAnthro    = b(pkAnthro + pkMeta)
	bPKBefore    = b(pkBefore)
	bPKAfter     = b(pkAfter)
)

// dyn holds a call's dynamic pieces: the call id, the session id and the user id built on it.
type dyn struct {
	call, session, user []byte
}

func newDyn(p turnPlan) dyn {
	buf := make([]byte, 0, len(p.callID)+2*len(p.sessionKey)+16)
	buf = append(buf, p.callID...)
	c := len(buf)
	buf = append(buf, p.sessionKey...)
	s := len(buf)
	buf = append(append(buf, "user_stress_"...), p.sessionKey...)
	return dyn{call: buf[:c:c], session: buf[c:s:s], user: buf[s:]}
}

func (w *worker) requestEnvelope(p turnPlan, d dyn) [][]byte {
	s := p.s.script
	pc := make([][]byte, 0, 20)
	switch w.tgt {
	case tLiteLLM:
		pc = append(pc, bLLReq, d.call, bLLTrace, d.session, bLLModelMsgs)
		pc = s.ChatMessages(pc, p.k, p.reqBad)
		pc = append(pc, bLLTools, s.ChatTools(), bLLTexts)
		pc = s.Texts(pc, p.k, p.reqBad)
		return append(pc, bLLHeadersR, d.session, bLLData)
	default:
		pc = append(pc, bPKReq)
		pc = w.portkeyJSON(pc, p, d)
		pc = append(pc, bPKFirst, bPKNull)
		return append(pc, w.portkeyProvider(p), d.session, bPKBefore)
	}
}

func (w *worker) responseEnvelope(p turnPlan, d dyn) [][]byte {
	s := p.s.script
	pc := make([][]byte, 0, 20)
	switch w.tgt {
	case tLiteLLM:
		pc = append(pc, bLLResp, d.call, bLLTrace, d.session, bLLModelText, s.ChatText(p.k, p.respBad),
			bLLCalls, s.ChatToolCalls(p.k, p.respBad), bLLHeaders, d.session, bLLData)
		return pc
	default:
		pc = append(pc, bPKReq)
		pc = w.portkeyJSON(pc, p, d)
		pc = append(pc, bPKFirst)
		if p.s.alt {
			pc = append(pc, s.AnthropicResponse(p.k, p.respBad))
		} else {
			pc = append(pc, s.ChatResponse(p.k, p.respBad))
		}
		return append(pc, bPK200, w.portkeyProvider(p), d.session, bPKAfter)
	}
}

// portkeyJSON is request.json: the client's body, byte-identical in both phases, as Portkey
// serializes it from the same object (the adapter's call id is its hash).
func (w *worker) portkeyJSON(pc [][]byte, p turnPlan, d dyn) [][]byte {
	if p.s.alt {
		return p.s.script.AnthropicRequest(pc, p.k, d.user, p.reqBad)
	}
	return p.s.script.ChatRequest(pc, p.k, d.user, p.reqBad)
}

func (w *worker) portkeyProvider(p turnPlan) []byte {
	if p.s.alt {
		return bPKAnthro
	}
	return bPKOpenAI
}

var (
	litellmAllow = []byte(`"action":"NONE"`)
	litellmBlock = []byte(`"action":"BLOCKED"`)
	portkeyAllow = []byte(`"verdict":true`)
	portkeyBlock = []byte(`"verdict":false`)
)

// verdict reads the adapter's answer.
func (w *worker) verdict(body []byte) (blocked, ok bool) {
	allow, block := litellmAllow, litellmBlock
	if w.tgt == tPortkey {
		allow, block = portkeyAllow, portkeyBlock
	}
	switch {
	case bytes.Contains(body, block):
		return true, true
	case bytes.Contains(body, allow):
		return false, true
	}
	return false, false
}

func (c *httpConn) request(ctx context.Context, path string, body io.ReadCloser, n int) *http.Request {
	u := c.url
	if path != "" {
		u = &url.URL{Scheme: "http", Host: c.host, Path: path}
	}
	req := &http.Request{
		Method: http.MethodPost, URL: u, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: jsonHeader, Body: body, ContentLength: int64(n), Host: c.host,
	}
	return req.WithContext(ctx)
}

// httpTurn plays one turn against LiteLLM or Portkey: the request-phase callout, then, unless
// it was blocked or failed, the response-phase callout.
func (w *worker) httpTurn(ctx context.Context, p turnPlan) {
	d := newDyn(p)
	blocked, ok := w.post(ctx, phRequest, w.requestEnvelope(p, d), p.expReq)
	if !ok || blocked {
		return
	}
	w.post(ctx, phResponse, w.responseEnvelope(p, d), p.expResp)
}

// post sends one callout and judges its verdict; ok is false when it failed.
func (w *worker) post(ctx context.Context, ph phase, pieces [][]byte, exp expect) (blocked, ok bool) {
	cctx, cancel := context.WithTimeout(ctx, w.run.cfg.timeout)
	defer cancel()
	req := w.http.request(cctx, "", &piecesReader{p: pieces}, size(pieces))
	start := w.begin()
	resp, err := w.http.rt.RoundTrip(req)
	var body []byte
	if err == nil {
		body, err = w.readBody(resp)
	}
	w.end(start, ph, err == nil && resp.StatusCode == http.StatusOK)
	m := w.run.m
	switch {
	case err != nil:
		m.recordErr(w.sh, w.tgt, err)
		return false, false
	case resp.StatusCode != http.StatusOK:
		w.sh.status[w.tgt][min(resp.StatusCode, 599)].Add(1)
		m.sample(fmt.Sprintf("%s http %d", w.tgt, resp.StatusCode), string(body))
		return false, false
	}
	if blocked, ok = w.verdict(body); !ok {
		w.sh.viol[w.tgt][vReplyShape].Add(1)
		m.sample(w.tgt.String()+" "+violationNames[vReplyShape], string(body))
		return false, false
	}
	w.sh.judge(w.tgt, exp, blocked)
	return blocked, true
}

// readBody reads a reply into the worker's buffer; the slice is valid until the next call.
func (w *worker) readBody(resp *http.Response) ([]byte, error) {
	w.buf.Reset()
	_, err := w.buf.ReadFrom(resp.Body)
	resp.Body.Close()
	return w.buf.Bytes(), err
}

// ---- chaos over HTTP ----

const chaosPad = "Please summarize the eviction design notes for the cache package and list the open questions. "

// smallBody is a short valid callout for chaos actions (~4 KB): a request-phase call of one user
// message.
func (w *worker) smallBody(cid, sid string) []byte {
	pad := bytes.Repeat([]byte(chaosPad), 40)
	if w.tgt == tLiteLLM {
		return fmt.Appendf(nil, `{"input_type":"request","litellm_call_id":%q,"litellm_trace_id":%q,"model":%q,"structured_messages":[{"role":"user","content":%q}],"request_headers":{"x-claude-code-session-id":%q}}`,
			cid, sid, synth.Model, pad, sid)
	}
	return fmt.Appendf(nil, `{"request":{"json":{"model":%q,"messages":[{"role":"user","content":%q}]}},"requestType":"chatComplete","metadata":{"session_id":%q},"eventType":"beforeRequestHook"}`,
		synth.Model, pad, sid)
}

func (w *worker) httpChaos(ctx context.Context, a chaosAction) {
	m := w.run.m
	cid, sid := w.chaosIDs()
	switch a {
	case chaosMalformed:
		p := w.plan()
		full := w.requestEnvelope(p, newDyn(p))
		w.chaosPost(ctx, a, "", truncate(full, size(full)/2), http.StatusBadRequest, w.run.cfg.timeout)
	case chaosUnknownPath:
		body := w.smallBody(cid, sid)
		w.chaosPost(ctx, a, "/v1/stress-unknown-path", [][]byte{body}, http.StatusNotFound, w.run.cfg.timeout)
	case chaosSlowloris:
		body := w.smallBody(cid, sid)
		timeout := max(w.run.cfg.timeout, 30*time.Second)
		if w.http.h2 {
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			start := w.begin()
			resp, err := w.http.rt.RoundTrip(w.http.request(cctx, "", &trickle{ctx: cctx, b: body}, len(body)))
			w.chaosReply(start, a, resp, err, http.StatusOK)
			return
		}
		w.rawH1(ctx, a, body, timeout)
	case chaosCancel:
		p := w.plan()
		full := w.requestEnvelope(p, newDyn(p))
		half := w.r.IntN(2) == 0
		if !w.http.h2 {
			w.rawH1Drop(ctx, full, half)
			return
		}
		// h2c: the stream is reset when the context ends, after HEADERS (and half the DATA).
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stopAt := 0
		if half {
			stopAt = size(full) / 2
		}
		start := w.begin()
		t := time.AfterFunc(20*time.Millisecond, cancel)
		resp, err := w.http.rt.RoundTrip(w.http.request(cctx, "", &stalling{ctx: cctx, r: piecesReader{p: full}, left: stopAt}, size(full)))
		t.Stop()
		w.end(start, phChaos, false)
		where := map[bool]string{true: "mid-body", false: "after headers"}[half]
		if err == nil {
			resp.Body.Close()
			m.chaosOutcome(w.tgt, "cancel "+where, fmt.Sprintf("completed with %d before cancel", resp.StatusCode), false)
			return
		}
		m.chaosOutcome(w.tgt, "cancel "+where, "stream reset by client", errors.Is(err, context.Canceled))
	}
}

func (w *worker) chaosIDs() (string, string) {
	w.storm++
	sid := w.run.runID + "-chaos-w" + strconv.Itoa(w.id) + "-n" + strconv.FormatUint(w.storm, 10)
	return sid + "-t1", sid
}

func (w *worker) chaosPost(ctx context.Context, a chaosAction, path string, pieces [][]byte, want int, timeout time.Duration) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := w.begin()
	resp, err := w.http.rt.RoundTrip(w.http.request(cctx, path, &piecesReader{p: pieces}, size(pieces)))
	w.chaosReply(start, a, resp, err, want)
}

// chaosReply judges a chaos action's HTTP reply against the status it should get.
func (w *worker) chaosReply(start time.Time, a chaosAction, resp *http.Response, err error, want int) {
	m := w.run.m
	if err == nil {
		var body []byte
		body, err = w.readBody(resp)
		if err == nil {
			w.end(start, phChaos, true)
			outcome := "http " + strconv.Itoa(resp.StatusCode)
			ok := resp.StatusCode == want
			if ok && want == http.StatusOK {
				if blocked, valid := w.verdict(body); !valid || blocked {
					outcome, ok = outcome+" with verdict "+string(bytes.TrimSpace(body)), false
				} else {
					outcome += " allowed"
				}
			}
			m.chaosOutcome(w.tgt, chaosNames[a], outcome, ok)
			return
		}
	}
	w.end(start, phChaos, false)
	m.chaosOutcome(w.tgt, chaosNames[a], "error: "+m.recordErr(w.sh, w.tgt, err), false)
}

// rawH1 sends body over a fresh HTTP/1.1 connection at 1 KB/s and reads the verdict.
func (w *worker) rawH1(ctx context.Context, a chaosAction, body []byte, timeout time.Duration) {
	m := w.run.m
	start := w.begin()
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", w.http.host)
	if err != nil {
		w.end(start, phChaos, false)
		m.chaosOutcome(w.tgt, chaosNames[a], "error: "+m.recordErr(w.sh, w.tgt, err), false)
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	_, err = conn.Write(w.h1Head(len(body)))
	for off := 0; err == nil && off < len(body); off += 1024 {
		if off > 0 {
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				err = ctx.Err()
				continue
			}
		}
		_, err = conn.Write(body[off:min(off+1024, len(body))])
	}
	var resp *http.Response
	if err == nil {
		resp, err = http.ReadResponse(bufio.NewReader(conn), nil)
	}
	w.chaosReply(start, a, resp, err, http.StatusOK)
}

// rawH1Drop opens a connection, sends the request head (and half the body), and drops it.
func (w *worker) rawH1Drop(ctx context.Context, body [][]byte, half bool) {
	m := w.run.m
	where := map[bool]string{true: "mid-body", false: "after headers"}[half]
	start := w.begin()
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", w.http.host)
	if err == nil {
		conn.SetDeadline(time.Now().Add(w.run.cfg.timeout))
		_, err = conn.Write(w.h1Head(size(body)))
		if err == nil && half {
			_, err = (&piecesReader{p: truncate(body, size(body)/2)}).WriteTo(conn)
		}
		time.Sleep(5 * time.Millisecond)
		conn.Close()
	}
	w.end(start, phChaos, false)
	if err != nil {
		m.chaosOutcome(w.tgt, "drop conn "+where, "error: "+m.recordErr(w.sh, w.tgt, err), false)
		return
	}
	m.chaosOutcome(w.tgt, "drop conn "+where, "dropped", true)
}

func (w *worker) h1Head(n int) []byte {
	return fmt.Appendf(nil, "POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		w.http.url.Path, w.http.host, n)
}

// trickle is a body sent at 1 KB/s.
type trickle struct {
	ctx  context.Context
	b    []byte
	sent bool
}

func (t *trickle) Read(p []byte) (int, error) {
	if len(t.b) == 0 {
		return 0, io.EOF
	}
	if t.sent {
		select {
		case <-time.After(time.Second):
		case <-t.ctx.Done():
			return 0, t.ctx.Err()
		}
	}
	t.sent = true
	n := copy(p[:min(len(p), 1024)], t.b)
	t.b = t.b[n:]
	return n, nil
}

func (t *trickle) Close() error { return nil }

// stalling sends left bytes of a body, then blocks until its context ends.
type stalling struct {
	ctx  context.Context
	r    piecesReader
	left int
}

func (s *stalling) Read(p []byte) (int, error) {
	if s.left <= 0 {
		<-s.ctx.Done()
		return 0, s.ctx.Err()
	}
	n, err := s.r.Read(p[:min(len(p), s.left)])
	s.left -= n
	return n, err
}

func (s *stalling) Close() error { return nil }
