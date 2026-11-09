// Package otlphttpreceiver is the collector's OTLP/HTTP receiver, made to keep the sender's port.
//
// It serves exactly what the upstream otlp receiver's HTTP protocol serves (POST /v1/logs,
// /v1/traces and /v1/metrics, protobuf or JSON, the same status codes and rpc.Status error bodies,
// built on the same confighttp server with its decompression and size limits) with one difference.
// The upstream receiver records the client's address in client.Info with the port dropped
// (confighttp's parseIP), and the port is what tells one loopback connection from another. This one
// records the full address, so the beacon_peer processor can ask the kernel which account owns the
// sending socket, as it already can for gRPC. Beacon renders it in place of the upstream HTTP
// protocol only where that lookup exists (Linux, macOS and Windows), so everywhere else nothing
// changes.
package otlphttpreceiver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"sync"

	"go.opentelemetry.io/collector/client"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var componentType = component.MustNewType("beacon_otlp_http")

const (
	pbContentType   = "application/x-protobuf"
	jsonContentType = "application/json"
	defaultEndpoint = "127.0.0.1:4318"
)

// Config is confighttp's server config, as under the upstream receiver's `protocols.http`.
type Config struct {
	confighttp.ServerConfig `mapstructure:",squash"`
}

func createDefaultConfig() component.Config {
	// The upstream receiver's HTTP defaults exactly, so swapping it in changes nothing but the
	// address recorded: no TLS, and no server timeouts.
	cfg := confighttp.NewDefaultServerConfig()
	cfg.Endpoint = defaultEndpoint
	cfg.TLSSetting = nil
	cfg.WriteTimeout = 0
	cfg.ReadHeaderTimeout = 0
	cfg.IdleTimeout = 0
	return &Config{ServerConfig: cfg}
}

func NewFactory() receiver.Factory {
	return receiver.NewFactory(componentType, createDefaultConfig,
		receiver.WithLogs(createLogs, component.StabilityLevelBeta),
		receiver.WithTraces(createTraces, component.StabilityLevelBeta),
		receiver.WithMetrics(createMetrics, component.StabilityLevelBeta))
}

// One server per configured receiver serves every signal, as the upstream receiver shares one, so
// the logs, traces and metrics pipelines that name it all get the same instance.
var (
	sharedMu sync.Mutex
	shared   = map[*Config]*otlpHTTP{}
)

func instance(cfg component.Config, set receiver.Settings) *otlpHTTP {
	c := cfg.(*Config)
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if r, ok := shared[c]; ok {
		return r
	}
	r := &otlpHTTP{cfg: c, settings: set}
	shared[c] = r
	return r
}

func createLogs(_ context.Context, set receiver.Settings, cfg component.Config, next consumer.Logs) (receiver.Logs, error) {
	r := instance(cfg, set)
	r.logs = next
	return r, nil
}

func createTraces(_ context.Context, set receiver.Settings, cfg component.Config, next consumer.Traces) (receiver.Traces, error) {
	r := instance(cfg, set)
	r.traces = next
	return r, nil
}

func createMetrics(_ context.Context, set receiver.Settings, cfg component.Config, next consumer.Metrics) (receiver.Metrics, error) {
	r := instance(cfg, set)
	r.metrics = next
	return r, nil
}

type otlpHTTP struct {
	cfg      *Config
	settings receiver.Settings
	logs     consumer.Logs
	traces   consumer.Traces
	metrics  consumer.Metrics

	mu       sync.Mutex
	addr     net.Addr // where the server listens, once started
	started  bool
	stopped  bool
	server   *http.Server
	serveErr chan struct{}
}

// Start is called once per pipeline that names the receiver; the server starts on the first.
func (r *otlpHTTP) Start(ctx context.Context, host component.Host) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return nil
	}
	mux := http.NewServeMux()
	if r.logs != nil {
		mux.HandleFunc("/v1/logs", r.handle(func(ctx context.Context, enc encoder, body []byte) ([]byte, error) {
			req := plogotlp.NewExportRequest()
			if err := enc.unmarshal(body, req.UnmarshalProto, req.UnmarshalJSON); err != nil {
				return nil, status.Error(codes.InvalidArgument, err.Error())
			}
			if req.Logs().LogRecordCount() > 0 {
				if err := r.logs.ConsumeLogs(ctx, req.Logs()); err != nil {
					return nil, consumeStatus(err)
				}
			}
			resp := plogotlp.NewExportResponse()
			return enc.marshal(resp.MarshalProto, resp.MarshalJSON)
		}))
	}
	if r.traces != nil {
		mux.HandleFunc("/v1/traces", r.handle(func(ctx context.Context, enc encoder, body []byte) ([]byte, error) {
			req := ptraceotlp.NewExportRequest()
			if err := enc.unmarshal(body, req.UnmarshalProto, req.UnmarshalJSON); err != nil {
				return nil, status.Error(codes.InvalidArgument, err.Error())
			}
			if req.Traces().SpanCount() > 0 {
				if err := r.traces.ConsumeTraces(ctx, req.Traces()); err != nil {
					return nil, consumeStatus(err)
				}
			}
			resp := ptraceotlp.NewExportResponse()
			return enc.marshal(resp.MarshalProto, resp.MarshalJSON)
		}))
	}
	if r.metrics != nil {
		mux.HandleFunc("/v1/metrics", r.handle(func(ctx context.Context, enc encoder, body []byte) ([]byte, error) {
			req := pmetricotlp.NewExportRequest()
			if err := enc.unmarshal(body, req.UnmarshalProto, req.UnmarshalJSON); err != nil {
				return nil, status.Error(codes.InvalidArgument, err.Error())
			}
			if req.Metrics().DataPointCount() > 0 {
				if err := r.metrics.ConsumeMetrics(ctx, req.Metrics()); err != nil {
					return nil, consumeStatus(err)
				}
			}
			resp := pmetricotlp.NewExportResponse()
			return enc.marshal(resp.MarshalProto, resp.MarshalJSON)
		}))
	}
	var err error
	r.server, err = r.cfg.ServerConfig.ToServer(ctx, host, r.settings.TelemetrySettings, mux,
		confighttp.WithErrorHandler(errorHandler))
	if err != nil {
		return err
	}
	ln, err := r.cfg.ServerConfig.ToListener(ctx)
	if err != nil {
		return fmt.Errorf("failed to bind to address %s: %w", r.cfg.Endpoint, err)
	}
	r.addr = ln.Addr()
	r.settings.Logger.Info("Starting HTTP server", zap.String("endpoint", ln.Addr().String()))
	r.started = true
	r.serveErr = make(chan struct{})
	go func() {
		defer close(r.serveErr)
		if err := r.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			componentstatus.ReportStatus(host, componentstatus.NewFatalErrorEvent(err))
		}
	}()
	return nil
}

// Shutdown is called once per pipeline too; the server stops on the first.
func (r *otlpHTTP) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started || r.stopped {
		return nil
	}
	r.stopped = true
	err := r.server.Shutdown(ctx)
	<-r.serveErr
	sharedMu.Lock()
	delete(shared, r.cfg)
	sharedMu.Unlock()
	return err
}

// handle wraps one signal's export with the protocol around it: method, content type, body, and
// the client's full address in client.Info.
func (r *otlpHTTP) handle(export func(context.Context, encoder, []byte) ([]byte, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			writeResponse(w, "text/plain", http.StatusMethodNotAllowed,
				[]byte(fmt.Sprintf("%v method not allowed, supported: [POST]", http.StatusMethodNotAllowed)))
			return
		}
		enc, ok := encoderFor(req.Header.Get("Content-Type"))
		if !ok {
			writeResponse(w, "text/plain", http.StatusUnsupportedMediaType,
				[]byte(fmt.Sprintf("%v unsupported media type, supported: [%s, %s]", http.StatusUnsupportedMediaType, jsonContentType, pbContentType)))
			return
		}
		body, err := io.ReadAll(req.Body)
		if err == nil {
			err = req.Body.Close()
		}
		if err != nil {
			writeStatus(w, enc, status.New(codes.InvalidArgument, err.Error()))
			return
		}
		msg, err := export(withClientAddr(req), enc, body)
		if err != nil {
			s, _ := status.FromError(err)
			writeStatus(w, enc, s)
			return
		}
		writeResponse(w, enc.contentType, http.StatusOK, msg)
	}
}

// withClientAddr is the one thing this receiver does differently: the request's context keeps
// whatever client.Info confighttp put there (metadata, auth) with Addr replaced by the full
// address, port included.
func withClientAddr(req *http.Request) context.Context {
	ctx := req.Context()
	host, port, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return ctx
	}
	ip := net.ParseIP(host)
	p, err := strconv.Atoi(port)
	if ip == nil || err != nil {
		return ctx
	}
	info := client.FromContext(ctx)
	info.Addr = &net.TCPAddr{IP: ip, Port: p}
	return client.NewContext(ctx, info)
}

// consumeStatus maps a pipeline's error to the status the upstream receiver returns for it:
// retryable unless the error says it is permanent.
func consumeStatus(err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}
	code := codes.Unavailable
	if consumererror.IsPermanent(err) {
		code = codes.Internal
	}
	return status.Error(code, err.Error())
}

type encoder struct {
	contentType string
	json        bool
}

func encoderFor(contentType string) (encoder, bool) {
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return encoder{}, false
	}
	switch media {
	case pbContentType:
		return encoder{contentType: pbContentType}, true
	case jsonContentType:
		return encoder{contentType: jsonContentType, json: true}, true
	}
	return encoder{}, false
}

func (e encoder) unmarshal(body []byte, pb, js func([]byte) error) error {
	if e.json {
		return js(body)
	}
	return pb(body)
}

func (e encoder) marshal(pb, js func() ([]byte, error)) ([]byte, error) {
	if e.json {
		return js()
	}
	return pb()
}

func (e encoder) marshalStatus(s *status.Status) ([]byte, error) {
	if e.json {
		return protojson.Marshal(s.Proto())
	}
	return proto.Marshal(s.Proto())
}

// httpStatus is the upstream receiver's mapping from a gRPC code to an HTTP status.
func httpStatus(s *status.Status) int {
	switch s.Code() {
	case codes.Canceled, codes.DeadlineExceeded, codes.Aborted, codes.OutOfRange, codes.Unavailable, codes.DataLoss:
		return http.StatusServiceUnavailable
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.Unimplemented:
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

var fallbackMsg = []byte(`{"code": 13, "message": "failed to marshal error message"}`)

func writeStatus(w http.ResponseWriter, enc encoder, s *status.Status) {
	msg, err := enc.marshalStatus(s)
	if err != nil {
		writeResponse(w, jsonContentType, http.StatusInternalServerError, fallbackMsg)
		return
	}
	writeResponse(w, enc.contentType, httpStatus(s), msg)
}

// errorHandler answers what confighttp refuses before a handler runs (a body over the size limit,
// a compression it cannot decode), in the request's encoding.
func errorHandler(w http.ResponseWriter, r *http.Request, msg string, code int) {
	enc, ok := encoderFor(r.Header.Get("Content-Type"))
	if !ok {
		writeResponse(w, jsonContentType, http.StatusInternalServerError, fallbackMsg)
		return
	}
	body, err := enc.marshalStatus(status.New(grpcCode(code), msg))
	if err != nil {
		writeResponse(w, jsonContentType, http.StatusInternalServerError, fallbackMsg)
		return
	}
	writeResponse(w, enc.contentType, code, body)
}

func grpcCode(httpCode int) codes.Code {
	switch httpCode {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return codes.InvalidArgument
	case http.StatusTooManyRequests:
		return codes.ResourceExhausted
	case http.StatusServiceUnavailable:
		return codes.Unavailable
	}
	return codes.Unknown
}

func writeResponse(w http.ResponseWriter, contentType string, code int, msg []byte) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(code)
	_, _ = w.Write(msg)
}
