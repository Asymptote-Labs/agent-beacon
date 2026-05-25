package falconhecexporter

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/beaconevent"
)

type falconExporter struct {
	cfg       *Config
	client    *http.Client
	logger    *zap.Logger
	converter beaconevent.Converter
}

type hecPayload struct {
	Time       float64                `json:"time,omitempty"`
	Event      map[string]interface{} `json:"event"`
	Source     string                 `json:"source,omitempty"`
	Sourcetype string                 `json:"sourcetype,omitempty"`
	Index      string                 `json:"index,omitempty"`
}

func newExporter(raw component.Config, set exporter.Settings) (*falconExporter, error) {
	cfg, ok := raw.(*Config)
	if !ok {
		return nil, fmt.Errorf("unexpected config type %T", raw)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.ContentRetention == "" {
		cfg.ContentRetention = "full"
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	client, err := httpClient(cfg)
	if err != nil {
		return nil, err
	}
	return &falconExporter{
		cfg:    cfg,
		client: client,
		logger: set.Logger,
		converter: beaconevent.NewConverter(beaconevent.Options{
			ContentRetention:      cfg.ContentRetention,
			IncludeRuntimeMetrics: cfg.IncludeRuntimeMetrics,
			IncludeCodexSpans:     cfg.IncludeCodexSpans,
		}),
	}, nil
}

func httpClient(cfg *Config) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.InsecureSkipVerify || cfg.CAFile != "" {
		tlsConfig := &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec // User opt-in for private/self-signed LogScale endpoints.
		if cfg.CAFile != "" {
			pem, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, err
			}
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if ok := pool.AppendCertsFromPEM(pem); !ok {
				return nil, fmt.Errorf("failed to parse CA file %q", cfg.CAFile)
			}
			tlsConfig.RootCAs = pool
		}
		transport.TLSClientConfig = tlsConfig
	}
	return &http.Client{Transport: transport, Timeout: cfg.Timeout}, nil
}

func (e *falconExporter) consumeLogs(ctx context.Context, logs plog.Logs) error {
	return e.sendEvents(ctx, e.converter.EventsFromLogs(logs))
}

func (e *falconExporter) consumeTraces(ctx context.Context, traces ptrace.Traces) error {
	return e.sendEvents(ctx, e.converter.EventsFromTraces(traces))
}

func (e *falconExporter) consumeMetrics(ctx context.Context, metrics pmetric.Metrics) error {
	return e.sendEvents(ctx, e.converter.EventsFromMetrics(metrics))
}

func (e *falconExporter) sendEvents(ctx context.Context, events []beaconevent.Event) error {
	if len(events) == 0 {
		return nil
	}
	var body bytes.Buffer
	for _, event := range events {
		payload, err := e.hecPayload(event)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.cfg.Endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+e.cfg.Token)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("falcon HEC returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(text)))
	}
	return nil
}

func (e *falconExporter) hecPayload(event beaconevent.Event) (hecPayload, error) {
	eventObject, err := eventObject(event)
	if err != nil {
		return hecPayload{}, err
	}
	ts, err := eventTime(event)
	if err != nil {
		return hecPayload{}, err
	}
	eventObject["@timestamp"] = ts.UTC().Format(time.RFC3339Nano)
	return hecPayload{
		Time:       float64(ts.UnixNano()) / float64(time.Second),
		Event:      eventObject,
		Source:     e.cfg.Source,
		Sourcetype: e.cfg.Sourcetype,
		Index:      e.cfg.Index,
	}, nil
}

func eventObject(event beaconevent.Event) (map[string]interface{}, error) {
	data, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func eventTime(event beaconevent.Event) (time.Time, error) {
	if !event.ObservedAt.IsZero() {
		return event.ObservedAt.UTC(), nil
	}
	if event.Timestamp == "" {
		return time.Now().UTC(), nil
	}
	ts, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if err != nil {
		return time.Time{}, err
	}
	return ts.UTC(), nil
}

func createLogsExporter(ctx context.Context, set exporter.Settings, cfg component.Config) (exporter.Logs, error) {
	exp, err := newExporter(cfg, set)
	if err != nil {
		return nil, err
	}
	fcfg := cfg.(*Config)
	return exporterhelperNewLogs(ctx, set, cfg, consumer.ConsumeLogsFunc(exp.consumeLogs), fcfg)
}

func createTracesExporter(ctx context.Context, set exporter.Settings, cfg component.Config) (exporter.Traces, error) {
	exp, err := newExporter(cfg, set)
	if err != nil {
		return nil, err
	}
	fcfg := cfg.(*Config)
	return exporterhelperNewTraces(ctx, set, cfg, consumer.ConsumeTracesFunc(exp.consumeTraces), fcfg)
}

func createMetricsExporter(ctx context.Context, set exporter.Settings, cfg component.Config) (exporter.Metrics, error) {
	exp, err := newExporter(cfg, set)
	if err != nil {
		return nil, err
	}
	fcfg := cfg.(*Config)
	return exporterhelperNewMetrics(ctx, set, cfg, consumer.ConsumeMetricsFunc(exp.consumeMetrics), fcfg)
}
