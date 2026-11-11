// Package envoy adapts Envoy's External Processing filter (ext_proc). Envoy opens one gRPC stream
// per HTTP exchange and sends the request and response headers and bodies through it; the adapter
// hands the raw model-call bodies to the pipeline and turns a block into an ImmediateResponse.
//
// Envoy's body modes are inferred, not configured: a body that arrives whole (BUFFERED) can still
// be replaced, so its verdict is enforced; a response that arrives in chunks (STREAMED) has already
// reached the client, so its verdict is only recorded.
package envoy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/internal/jsonscan"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
	"github.com/asymptote-labs/agent-beacon/gateway/llm/codec"
)

const (
	defaultName        = "envoy"
	defaultAddr        = "127.0.0.1:4021"
	defaultBlockStatus = http.StatusForbidden
	defaultMaxBody     = 32 << 20

	// blockedDetails lands in Envoy's access log (%RESPONSE_CODE_DETAILS%), so operators can tell
	// a Beacon block from an upstream error.
	blockedDetails = "beacon_gateway_blocked"
	// defaultReason answers a blocked client when policy gave no message.
	defaultReason = "blocked by Beacon gateway policy"
	// stopTimeout bounds how long shutdown waits for open exchanges; a long streamed reply would
	// otherwise hold it for minutes.
	stopTimeout = 5 * time.Second
)

// Config configures the ext_proc server.
type Config struct {
	Name string `yaml:"name"`
	Addr string `yaml:"addr"`
	// BlockStatus is the HTTP status of the reply that replaces a blocked call.
	BlockStatus int `yaml:"block_status"`
	// MaxBody caps the bytes held per body (after decompression); larger bodies pass unanalyzed
	// rather than letting one exchange exhaust memory.
	MaxBody int `yaml:"max_body"`
	// IdentityHeaders names the request headers that carry the caller's identity, as set by
	// Envoy (jwt_authn claim_to_headers) or the gateway in front of it.
	IdentityHeaders IdentityHeaders `yaml:"identity_headers"`
}

// IdentityHeaders names a request header per identity field; empty names are not read.
type IdentityHeaders struct {
	User  string `yaml:"user"`
	Email string `yaml:"email"`
	Team  string `yaml:"team"`
	Org   string `yaml:"org"`
	Key   string `yaml:"key"`
}

// Adapter serves Envoy's ExternalProcessor service.
type Adapter struct {
	cfg Config
	log *slog.Logger
	// encodings holds the content encodings already reported as passed through undecoded, so
	// each is logged once rather than per reply.
	encodings sync.Map
}

// New validates cfg and applies defaults.
func New(cfg Config, log *slog.Logger) (gateway.Adapter, error) {
	if cfg.Name == "" {
		cfg.Name = defaultName
	}
	if cfg.Addr == "" {
		cfg.Addr = defaultAddr
	}
	if cfg.BlockStatus == 0 {
		cfg.BlockStatus = defaultBlockStatus
	}
	// Envoy rejects an ImmediateResponse whose status is not a defined StatusCode.
	if _, ok := typev3.StatusCode_name[int32(cfg.BlockStatus)]; !ok || cfg.BlockStatus < 400 {
		return nil, fmt.Errorf("envoy %s: block_status %d is not an HTTP error status Envoy accepts", cfg.Name, cfg.BlockStatus)
	}
	switch {
	case cfg.MaxBody == 0:
		cfg.MaxBody = defaultMaxBody
	case cfg.MaxBody < 0:
		return nil, fmt.Errorf("envoy %s: max_body %d is negative", cfg.Name, cfg.MaxBody)
	}
	ids := &cfg.IdentityHeaders
	for _, name := range []*string{&ids.User, &ids.Email, &ids.Team, &ids.Org, &ids.Key} {
		*name = strings.ToLower(*name)
		// Identity is recorded with every call; a credential header would put the secret there.
		if *name != "" && gateway.IsSecretHeader(*name) {
			return nil, fmt.Errorf("envoy %s: identity header %q carries credentials", cfg.Name, *name)
		}
	}
	return &Adapter{cfg: cfg, log: log}, nil
}

// Name returns the adapter instance's name.
func (a *Adapter) Name() string { return a.cfg.Name }

// Serve listens on cfg.Addr and processes Envoy's streams until ctx is cancelled.
func (a *Adapter) Serve(ctx context.Context, h gateway.Handler) error {
	lis, err := net.Listen("tcp", a.cfg.Addr)
	if err != nil {
		return fmt.Errorf("envoy %s: %w", a.cfg.Name, err)
	}
	a.log.Info("envoy ext_proc server listening", "gateway", a.cfg.Name, "addr", lis.Addr().String())
	return a.serve(ctx, lis, h)
}

func (a *Adapter) serve(ctx context.Context, lis net.Listener, h gateway.Handler) error {
	// Envoy sends a BUFFERED body as one message; gRPC's default 4 MiB receive limit would reject
	// bodies the adapter is configured to accept. Headers and framing ride on top of the body.
	srv := grpc.NewServer(grpc.MaxRecvMsgSize(a.cfg.MaxBody + 1<<20))
	extprocv3.RegisterExternalProcessorServer(srv, &processor{a: a, h: h})
	served := make(chan error, 1)
	go func() { served <- srv.Serve(lis) }()

	select {
	case err := <-served:
		return fmt.Errorf("envoy %s: %w", a.cfg.Name, err)
	case <-ctx.Done():
	}
	stopped := make(chan struct{})
	go func() {
		srv.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(stopTimeout):
		srv.Stop()
		<-stopped
	}
	<-served
	return nil
}

type processor struct {
	extprocv3.UnimplementedExternalProcessorServer
	a *Adapter
	h gateway.Handler
}

// Process runs one HTTP exchange: Envoy sends a message per phase and waits for each answer.
func (p *processor) Process(stream extprocv3.ExternalProcessor_ProcessServer) error {
	x := &exchange{a: p.a, h: p.h}
	defer x.request.free()
	defer x.response.free()
	ctx := stream.Context()
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		resp, err := x.process(ctx, req)
		if err != nil {
			return err
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

// exchange is the state of one HTTP exchange across its stream's messages.
type exchange struct {
	a        *Adapter
	h        gateway.Handler
	id       string
	client   gateway.Client
	protocol llm.Protocol
	encoding string
	// model is the model the request named, and requestBody the request's body, for the reply.
	model       string
	requestBody []byte
	request     body
	response    body
}

// body accumulates one direction's body.
type body struct {
	// skip marks a body that is not analyzed: not a model call, an error reply, or refused at
	// admission.
	skip bool
	// streamed records that chunks arrived before the end, so Envoy already forwarded them.
	streamed bool
	overflow bool
	done     bool
	buf      []byte
	// held is the memory reserved for the body at admission, released by free.
	held     int64
	releases []func()
}

// free releases the body's admission reservations.
func (b *body) free() {
	for _, r := range b.releases {
		r()
	}
	b.releases, b.held = nil, 0
}

// add appends a chunk and reports whether it completed a body to analyze.
func (b *body) add(chunk []byte, end bool, limit int) bool {
	if b.skip || b.done {
		return false
	}
	if !end {
		b.streamed = true
	}
	if !b.overflow {
		if len(b.buf)+len(chunk) > limit {
			b.overflow, b.buf = true, nil
		} else {
			b.buf = append(b.buf, chunk...)
		}
	}
	b.done = end
	return end
}

func (x *exchange) process(ctx context.Context, req *extprocv3.ProcessingRequest) (*extprocv3.ProcessingResponse, error) {
	switch r := req.Request.(type) {
	case *extprocv3.ProcessingRequest_RequestHeaders:
		n := x.requestHeaders(r.RequestHeaders)
		if v, ok := x.admit(ctx, &x.request, n); !ok && v.Action == gateway.Block {
			return x.blocked(v.Reason), nil
		}
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extprocv3.HeadersResponse{},
		}}, nil
	case *extprocv3.ProcessingRequest_RequestBody:
		if x.request.add(r.RequestBody.GetBody(), r.RequestBody.GetEndOfStream(), x.a.cfg.MaxBody) {
			if v, ok := x.finish(ctx, gateway.PhaseRequest, &x.request); ok && v.Action == gateway.Block {
				return x.blocked(v.Reason), nil
			}
		}
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestBody{
			RequestBody: &extprocv3.BodyResponse{},
		}}, nil
	case *extprocv3.ProcessingRequest_ResponseHeaders:
		n := x.responseHeaders(r.ResponseHeaders)
		if v, ok := x.admit(ctx, &x.response, n); !ok && v.Action == gateway.Block {
			return x.blocked(v.Reason), nil
		}
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{
			ResponseHeaders: &extprocv3.HeadersResponse{},
		}}, nil
	case *extprocv3.ProcessingRequest_ResponseBody:
		if x.response.add(r.ResponseBody.GetBody(), r.ResponseBody.GetEndOfStream(), x.a.cfg.MaxBody) {
			if v, ok := x.finish(ctx, gateway.PhaseResponse, &x.response); ok && v.Action == gateway.Block && !x.response.streamed {
				return x.blocked(v.Reason), nil
			}
		}
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseBody{
			ResponseBody: &extprocv3.BodyResponse{},
		}}, nil
	case *extprocv3.ProcessingRequest_RequestTrailers:
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestTrailers{
			RequestTrailers: &extprocv3.TrailersResponse{},
		}}, nil
	case *extprocv3.ProcessingRequest_ResponseTrailers:
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseTrailers{
			ResponseTrailers: &extprocv3.TrailersResponse{},
		}}, nil
	}
	// A message kind this adapter does not know has no answer it could give in kind.
	return nil, status.Errorf(codes.InvalidArgument, "unsupported ext_proc request %T", req.Request)
}

// requestHeaders reads the request's identity and protocol and returns its declared body length,
// -1 when it declares none.
func (x *exchange) requestHeaders(h *extprocv3.HttpHeaders) int64 {
	pseudo, headers := splitHeaders(h.GetHeaders())
	x.id = headers["x-request-id"]
	if x.id == "" {
		x.id = rand.Text()
	}
	ids := x.a.cfg.IdentityHeaders
	x.client = gateway.Client{
		UserAgent: headers["user-agent"],
		Headers:   gateway.ClientHeaders(headers, ""),
		Identity: gateway.Identity{
			User:  headers[ids.User],
			Email: headers[ids.Email],
			Team:  headers[ids.Team],
			Org:   headers[ids.Org],
			Key:   headers[ids.Key],
		},
	}
	if xff := headers["x-forwarded-for"]; xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		x.client.Address = strings.TrimSpace(first)
	}
	// Only a POST with a body to a model endpoint is a model call: GET on the same paths lists
	// stored completions, and everything else (models, token counting, embeddings) passes.
	if pseudo[":method"] == http.MethodPost && !h.GetEndOfStream() {
		x.protocol = codec.Detect(pseudo[":path"])
	}
	if x.protocol == "" {
		x.request.skip, x.response.skip = true, true
	}
	return contentLength(headers)
}

// responseHeaders reads the reply's status and encoding and returns its declared body length, -1
// when it declares none (a streamed reply usually does not).
func (x *exchange) responseHeaders(h *extprocv3.HttpHeaders) int64 {
	pseudo, headers := splitHeaders(h.GetHeaders())
	// Error replies are the provider's, not model output.
	if code, err := strconv.Atoi(pseudo[":status"]); err != nil || code < 200 || code > 299 {
		x.response.skip = true
	}
	x.encoding = strings.ToLower(strings.TrimSpace(headers["content-encoding"]))
	return contentLength(headers)
}

func contentLength(headers map[string]string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(headers["content-length"]), 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// unknownBody is what a body that declares no length reserves at its headers; the rest is
// reserved when it is complete.
const unknownBody = 1 << 20

// admit reserves memory for a body before its headers are answered. Envoy sends a BUFFERED body
// only after the headers' answer, so an admitted body is within the pipeline's budget before it
// reaches this process, and one that waits for admission waits in Envoy. A refused body is
// skipped: Envoy still sends it, and it is dropped as it arrives. ok is false when refused, with
// the overload verdict.
//
// (A mode override could stop Envoy sending a refused body at all, but it replaces the whole
// processing mode, the reply's included, and the adapter does not know how Envoy is configured.)
func (x *exchange) admit(ctx context.Context, b *body, declared int64) (gateway.Verdict, bool) {
	if b.skip {
		return gateway.Verdict{}, true
	}
	n := declared
	switch {
	case declared < 0:
		n = min(unknownBody, int64(x.a.cfg.MaxBody))
	case declared > int64(x.a.cfg.MaxBody):
		b.overflow = true // not analyzed; reported when it ends
		return gateway.Verdict{}, true
	}
	return x.reserve(ctx, b, n)
}

// reserve takes n more bytes of the pipeline's body budget for b; a refusal skips the body.
func (x *exchange) reserve(ctx context.Context, b *body, n int64) (gateway.Verdict, bool) {
	release, refused, ok := x.h.Admit(ctx, n)
	if !ok {
		b.skip = true
		return refused, false
	}
	b.releases = append(b.releases, release)
	b.held += n
	return gateway.Verdict{}, true
}

// finish hands a completed body to the pipeline; ok is false when the body was not analyzed. The
// body's reservation, taken at its headers, grows to its actual size here (a body that declared
// no length, or more than it sent), and is released when analysis ends. A call refused here gets
// the overload verdict.
func (x *exchange) finish(ctx context.Context, phase gateway.Phase, b *body) (v gateway.Verdict, ok bool) {
	defer b.free()
	if b.overflow {
		x.a.log.Warn("body exceeds max_body, not analyzed", "gateway", x.a.cfg.Name, "call", x.id, "phase", phase.String(), "max_body", x.a.cfg.MaxBody)
		return v, false
	}
	if need := int64(len(b.buf)) - b.held; need > 0 {
		if refused, ok := x.reserve(ctx, b, need); !ok {
			return refused, true
		}
	}
	data := b.buf
	var requestBody []byte
	if phase == gateway.PhaseResponse {
		if data, ok = x.decode(data); !ok {
			return v, false
		}
		requestBody = x.requestBody
	} else {
		// The model the client asked for names both phases' events; the reply names the
		// provider's own model separately. The body goes to the reply too, which the pipeline
		// names a session from as it named the request's. Neither is modified or reused.
		x.model = jsonscan.FirstString(data, "model")
		x.requestBody = data
	}
	// A request is held until its last chunk is answered, so a block still stops it; a streamed
	// reply has already reached the client.
	enforce := phase == gateway.PhaseRequest || !b.streamed
	v = x.h.Handle(ctx, &gateway.Call{
		Gateway:     x.a.cfg.Name,
		ID:          x.id,
		Phase:       phase,
		Enforce:     enforce,
		Time:        time.Now(),
		Client:      x.client,
		Model:       x.model,
		Protocol:    x.protocol,
		Body:        data,
		RequestBody: requestBody,
	})
	if v.Action == gateway.Block {
		x.a.log.Warn("call blocked", "gateway", x.a.cfg.Name, "call", x.id, "phase", phase.String(), "rules", v.Rules, "enforced", enforce)
	}
	return v, true
}

// decode undoes the reply's content encoding, so the pipeline sees the JSON or SSE text.
func (x *exchange) decode(data []byte) ([]byte, bool) {
	switch x.encoding {
	case "", "identity":
		return data, true
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err == nil {
			var out []byte
			// The cap applies to the inflated size too: a small gzip body can inflate enormously.
			if out, err = io.ReadAll(io.LimitReader(zr, int64(x.a.cfg.MaxBody)+1)); err == nil {
				if len(out) > x.a.cfg.MaxBody {
					x.a.log.Warn("decompressed body exceeds max_body, not analyzed", "gateway", x.a.cfg.Name, "call", x.id, "max_body", x.a.cfg.MaxBody)
					return nil, false
				}
				return out, true
			}
		}
		// The raw bytes go on so the pipeline, not the adapter, decides what an undecodable reply means.
		x.a.log.Warn("gzip reply body does not decompress, passing it raw", "gateway", x.a.cfg.Name, "call", x.id, "err", err)
		return data, true
	}
	if _, seen := x.a.encodings.LoadOrStore(x.encoding, struct{}{}); !seen {
		x.a.log.Debug("reply content encoding not decoded, passing raw bytes", "gateway", x.a.cfg.Name, "encoding", x.encoding)
	}
	return data, true
}

// blocked replaces the exchange's reply with an error in the provider's own shape, so the client
// handles it like any provider refusal.
func (x *exchange) blocked(reason string) *extprocv3.ProcessingResponse {
	if reason == "" {
		reason = defaultReason
	}
	// protocol is set: only model-call bodies reach a verdict.
	c, _ := codec.For(x.protocol)
	code := x.a.cfg.BlockStatus
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ImmediateResponse{
		ImmediateResponse: &extprocv3.ImmediateResponse{
			Status: &typev3.HttpStatus{Code: typev3.StatusCode(code)},
			Headers: &extprocv3.HeaderMutation{SetHeaders: []*corev3.HeaderValueOption{{
				Header:       &corev3.HeaderValue{Key: "content-type", RawValue: []byte("application/json")},
				AppendAction: corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
			}}},
			Body:    c.ErrorBody(code, reason),
			Details: blockedDetails,
		},
	}}
}

// splitHeaders separates Envoy's pseudo-headers (":path", ":status") from the HTTP headers.
// Names are lowercased and repeated headers joined, as HTTP folds them.
func splitHeaders(m *corev3.HeaderMap) (pseudo, headers map[string]string) {
	pseudo, headers = map[string]string{}, map[string]string{}
	for _, h := range m.GetHeaders() {
		// Envoy fills raw_value; value is only set by older releases.
		v := string(h.GetRawValue())
		if v == "" {
			v = h.GetValue()
		}
		k := strings.ToLower(h.GetKey())
		dst := headers
		if strings.HasPrefix(k, ":") {
			dst = pseudo
		}
		if prev, ok := dst[k]; ok {
			v = prev + ", " + v
		}
		dst[k] = v
	}
	return pseudo, headers
}
