package falconhecexporter

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
)

var componentType = component.MustNewType("falcon_hec")

func NewFactory() exporter.Factory {
	return exporter.NewFactory(
		componentType,
		func() component.Config { return createDefaultConfig() },
		exporter.WithLogs(createLogsExporter, component.StabilityLevelBeta),
		exporter.WithTraces(createTracesExporter, component.StabilityLevelBeta),
		exporter.WithMetrics(createMetricsExporter, component.StabilityLevelBeta),
	)
}

func exporterOptions(cfg *Config) []exporterhelper.Option {
	return []exporterhelper.Option{
		exporterhelper.WithQueue(cfg.QueueSettings),
		exporterhelper.WithRetry(cfg.RetrySettings),
		exporterhelper.WithTimeout(exporterhelper.TimeoutConfig{Timeout: cfg.Timeout}),
	}
}

func exporterhelperNewLogs(ctx context.Context, set exporter.Settings, cfg component.Config, pusher consumer.ConsumeLogsFunc, fcfg *Config) (exporter.Logs, error) {
	return exporterhelper.NewLogs(ctx, set, cfg, pusher, exporterOptions(fcfg)...)
}

func exporterhelperNewTraces(ctx context.Context, set exporter.Settings, cfg component.Config, pusher consumer.ConsumeTracesFunc, fcfg *Config) (exporter.Traces, error) {
	return exporterhelper.NewTraces(ctx, set, cfg, pusher, exporterOptions(fcfg)...)
}

func exporterhelperNewMetrics(ctx context.Context, set exporter.Settings, cfg component.Config, pusher consumer.ConsumeMetricsFunc, fcfg *Config) (exporter.Metrics, error) {
	return exporterhelper.NewMetrics(ctx, set, cfg, pusher, exporterOptions(fcfg)...)
}
