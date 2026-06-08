package falconhecexporter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/config/configretry"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/beaconevent"
)

func TestConfigValidateRequiresEndpointAndToken(t *testing.T) {
	cfg := createDefaultConfig()
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing endpoint error")
	}
	cfg.Endpoint = "https://logscale.example/api/v1/ingest/hec"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing token error")
	}
	cfg.Token = "ingest-token"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func TestConsumeLogsSendsBearerHECPayload(t *testing.T) {
	var gotAuth string
	var gotContentType string
	var payload hecPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exp, err := newExporter(&Config{
		Endpoint:      server.URL,
		Token:         "ingest-token",
		Source:        "beacon-endpoint-agent",
		Sourcetype:    "json",
		Index:         "beacon-repo",
		Timeout:       time.Second,
		QueueSettings: createDefaultConfig().QueueSettings,
		RetrySettings: createDefaultConfig().RetrySettings,
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	logs := plog.NewLogs()
	rec := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	ts := time.Date(2024, 11, 18, 12, 31, 40, 251000000, time.UTC)
	rec.SetTimestamp(pcommon.Timestamp(ts.UnixNano()))
	rec.Body().SetStr("prompt submitted")
	rec.Attributes().PutStr("beacon.event.action", "prompt.submitted")
	rec.Attributes().PutStr("gen_ai.prompt", "summarize this")
	rec.Attributes().PutStr("service.name", "claude-code")

	if err := exp.consumeLogs(context.Background(), logs); err != nil {
		t.Fatalf("consumeLogs returned error: %v", err)
	}

	if gotAuth != "Bearer ingest-token" {
		t.Fatalf("Authorization = %q, want Bearer token", gotAuth)
	}
	if !strings.HasPrefix(gotContentType, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", gotContentType)
	}
	if payload.Source != "beacon-endpoint-agent" || payload.Sourcetype != "json" || payload.Index != "beacon-repo" {
		t.Fatalf("wrapper metadata missing: %#v", payload)
	}
	if payload.Event == nil {
		t.Fatal("payload event is nil")
	}
	if payload.Event["@timestamp"] != "2024-11-18T12:31:40.251Z" {
		t.Fatalf("@timestamp = %#v", payload.Event["@timestamp"])
	}
	if payload.Event["vendor"] != "beacon" || payload.Event["product"] != "endpoint-agent" {
		t.Fatalf("Beacon fields were not nested in event object: %#v", payload.Event)
	}
	if _, ok := payload.Event["@timestamp"].(string); !ok {
		t.Fatalf("@timestamp should be a string: %#v", payload.Event["@timestamp"])
	}
	if payload.Time != float64(ts.UnixNano())/float64(time.Second) {
		t.Fatalf("time = %.9f, want %.9f", payload.Time, float64(ts.UnixNano())/float64(time.Second))
	}
}

func TestHECPayloadUsesObservedAtForSubsecondTimestamp(t *testing.T) {
	exp := &falconExporter{cfg: createDefaultConfig()}
	ts := time.Date(2024, 11, 18, 12, 31, 40, 251000000, time.UTC)
	event := testEvent(ts.Format(time.RFC3339))
	event.ObservedAt = ts
	payload, err := exp.hecPayload(event)
	if err != nil {
		t.Fatalf("hecPayload returned error: %v", err)
	}
	if payload.Event["@timestamp"] != "2024-11-18T12:31:40.251Z" {
		t.Fatalf("@timestamp = %#v", payload.Event["@timestamp"])
	}
	if payload.Time != float64(ts.UnixNano())/float64(time.Second) {
		t.Fatalf("time = %.9f, want %.9f", payload.Time, float64(ts.UnixNano())/float64(time.Second))
	}
}

func TestConsumeLogsReturnsErrorForNonSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer server.Close()
	exp, err := newExporter(&Config{
		Endpoint:      server.URL,
		Token:         "bad-token",
		Timeout:       time.Second,
		QueueSettings: createDefaultConfig().QueueSettings,
		RetrySettings: configNoRetry(),
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}
	logs := plog.NewLogs()
	logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty().Body().SetStr("hello")
	err = exp.consumeLogs(context.Background(), logs)
	if err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("consumeLogs error = %v, want status 401", err)
	}
}

func TestHECPayloadDoesNotMutateCanonicalEvent(t *testing.T) {
	exp := &falconExporter{cfg: createDefaultConfig()}
	event := testEvent("2024-11-18T12:31:40.251Z")
	payload, err := exp.hecPayload(event)
	if err != nil {
		t.Fatalf("hecPayload returned error: %v", err)
	}
	if _, ok := payload.Event["@timestamp"]; !ok {
		t.Fatal("payload missing @timestamp")
	}
	if data, err := json.Marshal(event); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(data), "@timestamp") {
		t.Fatalf("canonical event mutated with @timestamp: %s", string(data))
	}
}

func testEvent(timestamp string) beaconevent.Event {
	return beaconevent.Event{
		ObservedAt:    mustParseTime(timestamp),
		Timestamp:     timestamp,
		Vendor:        "beacon",
		Product:       "endpoint-agent",
		SchemaVersion: "1.0",
		Event:         beaconevent.EventInfo{Kind: "agent_runtime", Action: "tool.invoked"},
		Severity:      "info",
		Harness:       beaconevent.HarnessInfo{Name: "test"},
	}
}

func mustParseTime(value string) time.Time {
	ts, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return ts
}

func configNoRetry() configretry.BackOffConfig {
	cfg := createDefaultConfig().RetrySettings
	cfg.Enabled = false
	return cfg
}
