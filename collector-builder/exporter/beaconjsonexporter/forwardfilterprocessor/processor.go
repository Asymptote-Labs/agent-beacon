// Package forwardfilterprocessor keeps only the runtimes an OTLP forward is meant to carry.
//
// The OTLP forward replaces the export a runtime had to another backend before Beacon pointed it at
// the local collector. The collector's receiver is shared by every OTLP source Beacon sets up
// (Gemini CLI, VS Code, SDKs, the browser extension's claude.ai and ChatGPT chats), so without this
// filter the forward would hand that backend telemetry it never received before. A resource whose
// harness is not listed, or cannot be named, is dropped from the forward only.
package forwardfilterprocessor

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processorhelper"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/beaconevent"
)

var componentType = component.MustNewType("beacon_forward_filter")

// Config lists the canonical harness names (asymptoteobserve.NormalizeHarnessName) to forward.
type Config struct {
	Harnesses []string `mapstructure:"harnesses"`
}

func NewFactory() processor.Factory {
	return processor.NewFactory(componentType, func() component.Config { return &Config{} },
		processor.WithLogs(createLogs, component.StabilityLevelBeta),
		processor.WithTraces(createTraces, component.StabilityLevelBeta),
		processor.WithMetrics(createMetrics, component.StabilityLevelBeta))
}

type filter map[string]bool

func newFilter(cfg component.Config) filter {
	allowed := filter{}
	for _, name := range cfg.(*Config).Harnesses {
		if normalized := beaconevent.NormalizeHarnessName(name); normalized != "" {
			allowed[normalized] = true
		}
	}
	return allowed
}

// drops reports whether a resource's telemetry stays out of the forward. The harness is resolved
// from the resource attributes the way the beaconjson exporter resolves it, so an explicit
// beacon.harness.name (the browser extension's claude_web/chatgpt_web) wins over service.name.
func (f filter) drops(resource pcommon.Resource) bool {
	attrs := resource.Attributes().AsRaw()
	if beaconevent.FirstString(attrs, "beacon.harness.name", "harness.name", "service.name", "telemetry.sdk.name") == "" {
		return true
	}
	return !f[beaconevent.HarnessName(attrs)]
}

var mutates = processorhelper.WithCapabilities(consumer.Capabilities{MutatesData: true})

func createLogs(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Logs) (processor.Logs, error) {
	f := newFilter(cfg)
	return processorhelper.NewLogs(ctx, set, cfg, next, func(_ context.Context, logs plog.Logs) (plog.Logs, error) {
		logs.ResourceLogs().RemoveIf(func(r plog.ResourceLogs) bool { return f.drops(r.Resource()) })
		if logs.ResourceLogs().Len() == 0 {
			return logs, processorhelper.ErrSkipProcessingData
		}
		return logs, nil
	}, mutates)
}

func createTraces(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Traces) (processor.Traces, error) {
	f := newFilter(cfg)
	return processorhelper.NewTraces(ctx, set, cfg, next, func(_ context.Context, traces ptrace.Traces) (ptrace.Traces, error) {
		traces.ResourceSpans().RemoveIf(func(r ptrace.ResourceSpans) bool { return f.drops(r.Resource()) })
		if traces.ResourceSpans().Len() == 0 {
			return traces, processorhelper.ErrSkipProcessingData
		}
		return traces, nil
	}, mutates)
}

func createMetrics(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Metrics) (processor.Metrics, error) {
	f := newFilter(cfg)
	return processorhelper.NewMetrics(ctx, set, cfg, next, func(_ context.Context, metrics pmetric.Metrics) (pmetric.Metrics, error) {
		metrics.ResourceMetrics().RemoveIf(func(r pmetric.ResourceMetrics) bool { return f.drops(r.Resource()) })
		if metrics.ResourceMetrics().Len() == 0 {
			return metrics, processorhelper.ErrSkipProcessingData
		}
		return metrics, nil
	}, mutates)
}
