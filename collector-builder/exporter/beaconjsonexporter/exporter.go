package beaconjsonexporter

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/beaconevent"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

type beaconExporter struct {
	cfg       *Config
	writer    jsonlWriter
	logger    *zap.Logger
	converter beaconevent.Converter
	// sequence numbers the events this exporter writes, in the order it walks them off
	// the wire. Nanosecond timestamps order almost everything on their own, but one
	// metric export flushes a batch of datapoints that all carry the same collection
	// instant; the counter is what recovers their emission order without falling back
	// to where the lines landed in the file.
	sequence *asymptoteobserve.Sequencer
}

const (
	codexConversationStarts = beaconevent.CodexConversationStarts
	codexUserPrompt         = beaconevent.CodexUserPrompt
	codexToolDecision       = beaconevent.CodexToolDecision
	codexToolResult         = beaconevent.CodexToolResult
)

func newExporter(raw component.Config, set exporter.Settings) (*beaconExporter, error) {
	cfg, ok := raw.(*Config)
	if !ok {
		return nil, fmt.Errorf("unexpected config type %T", raw)
	}
	if cfg.MaxEventBytes == 0 {
		cfg.MaxEventBytes = defaultMaxEventBytes
	}
	if cfg.RotateBytes <= 0 {
		cfg.RotateBytes = defaultRotateBytes
	}
	if cfg.RotateArchives <= 0 {
		cfg.RotateArchives = defaultRotateArchives
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &beaconExporter{
		cfg: cfg,
		writer: jsonlWriter{
			path:           cfg.Path,
			maxEventBytes:  cfg.MaxEventBytes,
			rotateBytes:    cfg.RotateBytes,
			rotateArchives: cfg.RotateArchives,
			redactSecrets:  cfg.RedactSecrets,
		},
		logger:   set.Logger,
		sequence: &asymptoteobserve.Sequencer{},
		converter: beaconevent.NewConverter(beaconevent.Options{
			IncludeRuntimeMetrics: cfg.IncludeRuntimeMetrics,
			IncludeCodexSpans:     cfg.IncludeCodexSpans,
		}),
	}, nil
}

func (e *beaconExporter) consumeLogs(ctx context.Context, logs plog.Logs) error {
	_ = ctx
	var firstErr error
	for _, event := range e.eventConverter().EventsFromLogs(logs) {
		e.stampSequence(&event)
		if err := e.writer.append(event); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (e *beaconExporter) consumeTraces(ctx context.Context, traces ptrace.Traces) error {
	_ = ctx
	var firstErr error
	for _, event := range e.eventConverter().EventsFromTraces(traces) {
		e.stampSequence(&event)
		if err := e.writer.append(event); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (e *beaconExporter) consumeMetrics(ctx context.Context, metrics pmetric.Metrics) error {
	_ = ctx
	var firstErr error
	for _, event := range e.eventConverter().EventsFromMetrics(metrics) {
		e.stampSequence(&event)
		if err := e.writer.append(event); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// stampSequence gives the event this exporter's next emission number, unless the runtime
// already supplied one of its own on the wire and the converter promoted it.
func (e *beaconExporter) stampSequence(event *beaconEvent) {
	if event.Sequence != 0 {
		return
	}
	event.Sequence = e.sequence.Next()
}

func shouldDropLog(resourceAttrs map[string]interface{}, record plog.LogRecord) bool {
	return beaconevent.ShouldDropLog(resourceAttrs, record)
}

func shouldDropMetric(resourceAttrs map[string]interface{}, name string, includeRuntimeMetrics bool) bool {
	return beaconevent.ShouldDropMetric(resourceAttrs, name, includeRuntimeMetrics)
}

func (e *beaconExporter) eventFromLog(resourceAttrs map[string]interface{}, record plog.LogRecord) beaconEvent {
	return e.eventConverter().EventFromLog(resourceAttrs, record)
}

func (e *beaconExporter) eventFromSpan(resourceAttrs map[string]interface{}, span ptrace.Span) beaconEvent {
	return e.eventConverter().EventFromSpan(resourceAttrs, span)
}

func (e *beaconExporter) shouldDropSpan(resourceAttrs map[string]interface{}, span ptrace.Span) bool {
	return e.eventConverter().ShouldDropSpan(resourceAttrs, span)
}

func (e *beaconExporter) normalizeCodexLogEvent(event *beaconEvent, attrs map[string]interface{}) {
	e.eventConverter().NormalizeCodexLogEvent(event, attrs)
}

func codexLogEventName(attrs map[string]interface{}) string {
	return beaconevent.CodexLogEventName(attrs)
}

func normalizeCodexToolResult(event *beaconEvent, attrs map[string]interface{}) {
	beaconevent.NormalizeCodexToolResult(event, attrs)
}

func (e *beaconExporter) eventFromMetric(resourceAttrs map[string]interface{}, metric pmetric.Metric) beaconEvent {
	return e.eventConverter().EventFromMetric(resourceAttrs, metric)
}

func (e *beaconExporter) populateCommon(event *beaconEvent, attrs map[string]interface{}) {
	e.eventConverter().PopulateCommon(event, attrs)
}

func (e *beaconExporter) rawPayload(attrs map[string]interface{}, extra map[string]interface{}) map[string]interface{} {
	return e.eventConverter().RawPayload(attrs, extra)
}

func (e *beaconExporter) eventConverter() beaconevent.Converter {
	if e == nil || e.cfg == nil {
		return beaconevent.NewConverter(beaconevent.Options{})
	}
	return beaconevent.NewConverter(beaconevent.Options{
		IncludeRuntimeMetrics: e.cfg.IncludeRuntimeMetrics,
		IncludeCodexSpans:     e.cfg.IncludeCodexSpans,
	})
}

func attrsToMap(attrs pcommon.Map) map[string]interface{} {
	return beaconevent.AttrsToMap(attrs)
}

func mergeMaps(a, b map[string]interface{}) map[string]interface{} {
	return beaconevent.MergeMaps(a, b)
}

func firstString(attrs map[string]interface{}, keys ...string) string {
	return beaconevent.FirstString(attrs, keys...)
}

func firstNonEmpty(values ...string) string {
	return beaconevent.FirstNonEmpty(values...)
}

func intAttr(attrs map[string]interface{}, keys ...string) (int, bool) {
	return beaconevent.IntAttr(attrs, keys...)
}

func int64Attr(attrs map[string]interface{}, keys ...string) (int64, bool) {
	return beaconevent.Int64Attr(attrs, keys...)
}

func timestamp(ts time.Time) time.Time {
	return beaconevent.Timestamp(ts)
}

func severity(text, number string) string {
	return beaconevent.Severity(text, number)
}

func spanSeverity(status string) string {
	return beaconevent.SpanSeverity(status)
}

func harnessName(attrs map[string]interface{}, hints ...string) string {
	return beaconevent.HarnessName(attrs, hints...)
}

func normalizeHarnessName(name string) string {
	return beaconevent.NormalizeHarnessName(name)
}

func inferAction(attrs map[string]interface{}, fallback string) string {
	return beaconevent.InferAction(attrs, fallback)
}

func copilotAction(attrs map[string]interface{}, operation, text string) string {
	return beaconevent.CopilotAction(attrs, operation, text)
}

func geminiToolAction(attrs map[string]interface{}) string {
	return beaconevent.GeminiToolAction(attrs)
}

func geminiFileAction(attrs map[string]interface{}) string {
	return beaconevent.GeminiFileAction(attrs)
}

func eventCategory(action, explicit string) string {
	return beaconevent.EventCategory(action, explicit)
}
