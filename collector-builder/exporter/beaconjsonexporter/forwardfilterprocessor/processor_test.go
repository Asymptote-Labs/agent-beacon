package forwardfilterprocessor

import (
	"context"
	"testing"

	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor/processortest"
)

// resources are one OTLP source each: what each sets on its resource, and whether a forward of
// Claude Code and Codex carries it.
var resources = []struct {
	attrs   map[string]any
	forward bool
}{
	{map[string]any{"service.name": "claude-code"}, true},
	{map[string]any{"service.name": "codex_cli_rs"}, true},
	{map[string]any{"service.name": "codex-app-server"}, true},
	{map[string]any{"service.name": "agent-beacon-browser-collector", "beacon.harness.name": "claude_web"}, false},
	{map[string]any{"service.name": "agent-beacon-browser-collector", "beacon.harness.name": "chatgpt_web"}, false},
	{map[string]any{"service.name": "gemini-cli"}, false},
	{map[string]any{}, false},
}

func config() *Config {
	return &Config{Harnesses: []string{"claude_code", "codex_cli", "codex_desktop"}}
}

func TestLogsKeepOnlyListedHarnesses(t *testing.T) {
	sink := new(consumertest.LogsSink)
	proc, err := NewFactory().CreateLogs(context.Background(), processortest.NewNopSettings(componentType), config(), sink)
	if err != nil {
		t.Fatal(err)
	}
	logs := plog.NewLogs()
	want := 0
	for _, r := range resources {
		resource := logs.ResourceLogs().AppendEmpty()
		if err := resource.Resource().Attributes().FromRaw(r.attrs); err != nil {
			t.Fatal(err)
		}
		resource.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty().Body().SetStr("x")
		if r.forward {
			want++
		}
	}
	if err := proc.ConsumeLogs(context.Background(), logs); err != nil {
		t.Fatal(err)
	}
	got := sink.AllLogs()
	if len(got) != 1 || got[0].ResourceLogs().Len() != want {
		t.Fatalf("forwarded %d batches, want one with %d resources", len(got), want)
	}
	for i := 0; i < got[0].ResourceLogs().Len(); i++ {
		if name, ok := got[0].ResourceLogs().At(i).Resource().Attributes().Get("beacon.harness.name"); ok {
			t.Fatalf("forwarded a browser resource: %s", name.Str())
		}
	}
}

func TestNothingListedForwardsNothing(t *testing.T) {
	sink := new(consumertest.TracesSink)
	proc, err := NewFactory().CreateTraces(context.Background(), processortest.NewNopSettings(componentType), &Config{Harnesses: []string{"codex_cli"}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	traces := ptrace.NewTraces()
	_ = traces.ResourceSpans().AppendEmpty().Resource().Attributes().FromRaw(map[string]any{"service.name": "claude-code"})
	if err := proc.ConsumeTraces(context.Background(), traces); err != nil {
		t.Fatal(err)
	}
	if len(sink.AllTraces()) != 0 {
		t.Fatalf("forwarded Claude Code spans with only Codex listed")
	}
}

func TestMetricsKeepListedHarness(t *testing.T) {
	sink := new(consumertest.MetricsSink)
	proc, err := NewFactory().CreateMetrics(context.Background(), processortest.NewNopSettings(componentType), config(), sink)
	if err != nil {
		t.Fatal(err)
	}
	metrics := pmetric.NewMetrics()
	_ = metrics.ResourceMetrics().AppendEmpty().Resource().Attributes().FromRaw(map[string]any{"service.name": "claude-code"})
	_ = metrics.ResourceMetrics().AppendEmpty().Resource().Attributes().FromRaw(map[string]any{"service.name": "gemini-cli"})
	if err := proc.ConsumeMetrics(context.Background(), metrics); err != nil {
		t.Fatal(err)
	}
	if got := sink.AllMetrics(); len(got) != 1 || got[0].ResourceMetrics().Len() != 1 {
		t.Fatalf("want only the Claude Code resource forwarded, got %+v", got)
	}
}
