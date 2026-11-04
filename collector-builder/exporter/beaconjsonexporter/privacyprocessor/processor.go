// Package privacyprocessor redacts secrets before telemetry fans out to exporters.
// Exporter-local sanitization cannot protect a sibling exporter such as splunk_hec.
package privacyprocessor

import (
	"context"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processorhelper"
)

var componentType = component.MustNewType("beacon_privacy")

type Config struct{}

func NewFactory() processor.Factory {
	return processor.NewFactory(componentType, func() component.Config { return &Config{} },
		processor.WithLogs(createLogs, component.StabilityLevelBeta),
		processor.WithTraces(createTraces, component.StabilityLevelBeta),
		processor.WithMetrics(createMetrics, component.StabilityLevelBeta))
}

// Keep encoded message lists intact until the converter parses them. The JSONL
// writer still applies its normal per-field limits after conversion. This bound
// is above the generated receiver's largest accepted message (32 MiB).
const preConversionStringLimit = 64 << 20

var privacy = asymptoteobserve.PrivacyOptions{RedactSecrets: true, StringLimit: preConversionStringLimit}

func redactMap(attrs pcommon.Map) error {
	return attrs.FromRaw(asymptoteobserve.SanitizeMap(attrs.AsRaw(), privacy))
}

func redactValue(value pcommon.Value) error {
	switch value.Type() {
	case pcommon.ValueTypeStr:
		value.SetStr(asymptoteobserve.RedactString(value.Str()))
	case pcommon.ValueTypeMap:
		return redactMap(value.Map())
	case pcommon.ValueTypeSlice:
		return value.FromRaw(asymptoteobserve.SanitizeSlice(value.Slice().AsRaw(), privacy))
	}
	return nil
}

func createLogs(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Logs) (processor.Logs, error) {
	return processorhelper.NewLogs(ctx, set, cfg, next, processLogs,
		processorhelper.WithCapabilities(consumer.Capabilities{MutatesData: true}))
}

func processLogs(_ context.Context, logs plog.Logs) (plog.Logs, error) {
	for resourceIndex := 0; resourceIndex < logs.ResourceLogs().Len(); resourceIndex++ {
		resource := logs.ResourceLogs().At(resourceIndex)
		if err := redactMap(resource.Resource().Attributes()); err != nil {
			return logs, err
		}
		for scopeIndex := 0; scopeIndex < resource.ScopeLogs().Len(); scopeIndex++ {
			scope := resource.ScopeLogs().At(scopeIndex)
			if err := redactMap(scope.Scope().Attributes()); err != nil {
				return logs, err
			}
			for recordIndex := 0; recordIndex < scope.LogRecords().Len(); recordIndex++ {
				record := scope.LogRecords().At(recordIndex)
				if err := redactMap(record.Attributes()); err != nil {
					return logs, err
				}
				if err := redactValue(record.Body()); err != nil {
					return logs, err
				}
			}
		}
	}
	return logs, nil
}

func createTraces(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Traces) (processor.Traces, error) {
	return processorhelper.NewTraces(ctx, set, cfg, next, processTraces,
		processorhelper.WithCapabilities(consumer.Capabilities{MutatesData: true}))
}

func processTraces(_ context.Context, traces ptrace.Traces) (ptrace.Traces, error) {
	for resourceIndex := 0; resourceIndex < traces.ResourceSpans().Len(); resourceIndex++ {
		resource := traces.ResourceSpans().At(resourceIndex)
		if err := redactMap(resource.Resource().Attributes()); err != nil {
			return traces, err
		}
		for scopeIndex := 0; scopeIndex < resource.ScopeSpans().Len(); scopeIndex++ {
			scope := resource.ScopeSpans().At(scopeIndex)
			if err := redactMap(scope.Scope().Attributes()); err != nil {
				return traces, err
			}
			for spanIndex := 0; spanIndex < scope.Spans().Len(); spanIndex++ {
				span := scope.Spans().At(spanIndex)
				span.SetName(asymptoteobserve.RedactString(span.Name()))
				span.Status().SetMessage(asymptoteobserve.RedactString(span.Status().Message()))
				if err := redactMap(span.Attributes()); err != nil {
					return traces, err
				}
				for eventIndex := 0; eventIndex < span.Events().Len(); eventIndex++ {
					event := span.Events().At(eventIndex)
					event.SetName(asymptoteobserve.RedactString(event.Name()))
					if err := redactMap(event.Attributes()); err != nil {
						return traces, err
					}
				}
				for linkIndex := 0; linkIndex < span.Links().Len(); linkIndex++ {
					link := span.Links().At(linkIndex)
					if err := redactMap(link.Attributes()); err != nil {
						return traces, err
					}
				}
			}
		}
	}
	return traces, nil
}

func createMetrics(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Metrics) (processor.Metrics, error) {
	return processorhelper.NewMetrics(ctx, set, cfg, next, processMetrics,
		processorhelper.WithCapabilities(consumer.Capabilities{MutatesData: true}))
}

func redactExemplars(exemplars pmetric.ExemplarSlice) error {
	for exemplarIndex := 0; exemplarIndex < exemplars.Len(); exemplarIndex++ {
		exemplar := exemplars.At(exemplarIndex)
		if err := redactMap(exemplar.FilteredAttributes()); err != nil {
			return err
		}
	}
	return nil
}

func redactNumberPoints(points pmetric.NumberDataPointSlice) error {
	for pointIndex := 0; pointIndex < points.Len(); pointIndex++ {
		point := points.At(pointIndex)
		if err := redactMap(point.Attributes()); err != nil {
			return err
		}
		if err := redactExemplars(point.Exemplars()); err != nil {
			return err
		}
	}
	return nil
}

func processMetrics(_ context.Context, metrics pmetric.Metrics) (pmetric.Metrics, error) {
	for resourceIndex := 0; resourceIndex < metrics.ResourceMetrics().Len(); resourceIndex++ {
		resource := metrics.ResourceMetrics().At(resourceIndex)
		if err := redactMap(resource.Resource().Attributes()); err != nil {
			return metrics, err
		}
		for scopeIndex := 0; scopeIndex < resource.ScopeMetrics().Len(); scopeIndex++ {
			scope := resource.ScopeMetrics().At(scopeIndex)
			if err := redactMap(scope.Scope().Attributes()); err != nil {
				return metrics, err
			}
			for metricIndex := 0; metricIndex < scope.Metrics().Len(); metricIndex++ {
				metric := scope.Metrics().At(metricIndex)
				metric.SetDescription(asymptoteobserve.RedactString(metric.Description()))
				switch metric.Type() {
				case pmetric.MetricTypeGauge:
					if err := redactNumberPoints(metric.Gauge().DataPoints()); err != nil {
						return metrics, err
					}
				case pmetric.MetricTypeSum:
					if err := redactNumberPoints(metric.Sum().DataPoints()); err != nil {
						return metrics, err
					}
				case pmetric.MetricTypeHistogram:
					for pointIndex := 0; pointIndex < metric.Histogram().DataPoints().Len(); pointIndex++ {
						point := metric.Histogram().DataPoints().At(pointIndex)
						if err := redactMap(point.Attributes()); err != nil {
							return metrics, err
						}
						if err := redactExemplars(point.Exemplars()); err != nil {
							return metrics, err
						}
					}
				case pmetric.MetricTypeExponentialHistogram:
					for pointIndex := 0; pointIndex < metric.ExponentialHistogram().DataPoints().Len(); pointIndex++ {
						point := metric.ExponentialHistogram().DataPoints().At(pointIndex)
						if err := redactMap(point.Attributes()); err != nil {
							return metrics, err
						}
						if err := redactExemplars(point.Exemplars()); err != nil {
							return metrics, err
						}
					}
				case pmetric.MetricTypeSummary:
					for pointIndex := 0; pointIndex < metric.Summary().DataPoints().Len(); pointIndex++ {
						point := metric.Summary().DataPoints().At(pointIndex)
						if err := redactMap(point.Attributes()); err != nil {
							return metrics, err
						}
					}
				}
			}
		}
	}
	return metrics, nil
}
