package otlphttpreceiver

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"

	"go.opentelemetry.io/collector/client"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"go.opentelemetry.io/collector/receiver/receivertest"
)

// recordingLogs keeps every export and the client.Info it arrived with.
type recordingLogs struct {
	mu    sync.Mutex
	infos []client.Info
	logs  []plog.Logs
	err   error
}

func (r *recordingLogs) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }
func (r *recordingLogs) ConsumeLogs(ctx context.Context, ld plog.Logs) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.infos = append(r.infos, client.FromContext(ctx))
	r.logs = append(r.logs, ld)
	return r.err
}

type fixture struct {
	url     string
	logs    *recordingLogs
	traces  *consumertest.TracesSink
	metrics *consumertest.MetricsSink
}

func start(t *testing.T) fixture {
	t.Helper()
	f := NewFactory()
	cfg := f.CreateDefaultConfig().(*Config)
	cfg.Endpoint = "127.0.0.1:0"
	set := receivertest.NewNopSettings(componentType)
	fx := fixture{logs: &recordingLogs{}, traces: new(consumertest.TracesSink), metrics: new(consumertest.MetricsSink)}
	lr, err := f.CreateLogs(context.Background(), set, cfg, fx.logs)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := f.CreateTraces(context.Background(), set, cfg, fx.traces)
	if err != nil {
		t.Fatal(err)
	}
	mr, err := f.CreateMetrics(context.Background(), set, cfg, fx.metrics)
	if err != nil {
		t.Fatal(err)
	}
	if lr != tr || tr != mr {
		t.Fatal("each signal got its own receiver; they must share one server")
	}
	host := componenttest.NewNopHost()
	// Started once per pipeline, as the collector does.
	if err := lr.Start(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(context.Background(), host); err != nil {
		t.Fatalf("a second Start failed: %v", err)
	}
	t.Cleanup(func() {
		if err := lr.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
		if err := mr.Shutdown(context.Background()); err != nil {
			t.Errorf("a second Shutdown failed: %v", err)
		}
	})
	fx.url = "http://" + lr.(*otlpHTTP).addr.String()
	return fx
}

const jsonLog = `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"chatgpt_web"}}]},"scopeLogs":[{"logRecords":[{"body":{"stringValue":"hello"}}]}]}]}`

// post sends body from a connection whose local address the caller learns, so a test can compare
// the port the receiver recorded with the one the client really used.
func post(t *testing.T, url, contentType string, body []byte, header map[string]string) (*http.Response, []byte, *net.TCPAddr) {
	t.Helper()
	var local *net.TCPAddr
	transport := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err == nil {
			local = c.LocalAddr().(*net.TCPAddr)
		}
		return c, err
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out, local
}

// The point of the receiver: the pipeline sees the client's address with its port.
func TestTheClientPortReachesThePipeline(t *testing.T) {
	fx := start(t)
	resp, _, local := post(t, fx.url+"/v1/logs", "application/json", []byte(jsonLog), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(fx.logs.infos) != 1 {
		t.Fatalf("got %d exports, want 1", len(fx.logs.infos))
	}
	addr, ok := fx.logs.infos[0].Addr.(*net.TCPAddr)
	if !ok {
		t.Fatalf("client.Info.Addr = %T %v, want a *net.TCPAddr", fx.logs.infos[0].Addr, fx.logs.infos[0].Addr)
	}
	if addr.Port != local.Port || !addr.IP.Equal(local.IP) {
		t.Fatalf("recorded %s, want the client's own %s", addr, local)
	}
	if got := fx.logs.logs[0].LogRecordCount(); got != 1 {
		t.Fatalf("log records = %d", got)
	}
}

func TestProtobufAndGzipAreServedLikeUpstream(t *testing.T) {
	fx := start(t)
	traces := ptrace.NewTraces()
	traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("tool")
	body, err := ptraceotlp.NewExportRequestFromTraces(traces).MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	_, _ = zw.Write(body)
	_ = zw.Close()
	resp, out, _ := post(t, fx.url+"/v1/traces", "application/x-protobuf", zipped.Bytes(), map[string]string{"Content-Encoding": "gzip"})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-protobuf" {
		t.Fatalf("status = %d, content type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if err := ptraceotlp.NewExportResponse().UnmarshalProto(out); err != nil {
		t.Fatalf("response is not an ExportTraceServiceResponse: %v", err)
	}
	if fx.traces.SpanCount() != 1 {
		t.Fatalf("spans = %d", fx.traces.SpanCount())
	}

	metrics := `{"resourceMetrics":[{"scopeMetrics":[{"metrics":[{"name":"m","gauge":{"dataPoints":[{"asInt":"1"}]}}]}]}]}`
	resp, _, _ = post(t, fx.url+"/v1/metrics", "application/json; charset=utf-8", []byte(metrics), nil)
	if resp.StatusCode != http.StatusOK || fx.metrics.DataPointCount() != 1 {
		t.Fatalf("metrics status = %d, points = %d", resp.StatusCode, fx.metrics.DataPointCount())
	}
}

func TestRefusalsMatchUpstream(t *testing.T) {
	fx := start(t)

	resp, err := http.Get(fx.url + "/v1/logs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", resp.StatusCode)
	}
	if resp, _, _ := post(t, fx.url+"/v1/logs", "text/plain", []byte(jsonLog), nil); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain status = %d, want 415", resp.StatusCode)
	}
	resp2, out, _ := post(t, fx.url+"/v1/logs", "application/json", []byte(`{"resourceLogs":`), nil)
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed status = %d, want 400", resp2.StatusCode)
	}
	var st map[string]any
	if err := json.Unmarshal(out, &st); err != nil || st["code"] != float64(3) {
		t.Errorf("malformed body = %s, want an rpc.Status with InvalidArgument", out)
	}
	if resp, _, _ := post(t, fx.url+"/v1/profiles", "application/json", []byte(`{}`), nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path status = %d, want 404", resp.StatusCode)
	}

	// A pipeline that refuses (memory_limiter) is retryable; a permanent error is not.
	fx.logs.err = errors.New("memory limit")
	if resp, _, _ := post(t, fx.url+"/v1/logs", "application/json", []byte(jsonLog), nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("retryable status = %d, want 503", resp.StatusCode)
	}
	fx.logs.err = consumererror.NewPermanent(errors.New("bad data"))
	if resp, _, _ := post(t, fx.url+"/v1/logs", "application/json", []byte(jsonLog), nil); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("permanent status = %d, want 500", resp.StatusCode)
	}

	// An empty export is answered without reaching the pipeline.
	fx.logs.err = nil
	before := len(fx.logs.logs)
	if resp, _, _ := post(t, fx.url+"/v1/logs", "application/json", []byte(`{}`), nil); resp.StatusCode != http.StatusOK {
		t.Errorf("empty status = %d", resp.StatusCode)
	}
	if len(fx.logs.logs) != before {
		t.Error("an empty export reached the pipeline")
	}
}

func TestOnlyConfiguredSignalsAreServed(t *testing.T) {
	f := NewFactory()
	cfg := f.CreateDefaultConfig().(*Config)
	cfg.Endpoint = "127.0.0.1:0"
	r, err := f.CreateLogs(context.Background(), receivertest.NewNopSettings(componentType), cfg, new(consumertest.LogsSink))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background(), componenttest.NewNopHost()); err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown(context.Background())
	url := "http://" + r.(*otlpHTTP).addr.String()
	if resp, _, _ := post(t, url+"/v1/traces", "application/json", []byte(`{}`), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("traces without a traces pipeline = %d, want 404", resp.StatusCode)
	}
}

func TestDefaultsToTheLoopbackOTLPPort(t *testing.T) {
	if got := createDefaultConfig().(*Config).Endpoint; got != "127.0.0.1:4318" {
		t.Fatalf("default endpoint = %q", got)
	}
}
