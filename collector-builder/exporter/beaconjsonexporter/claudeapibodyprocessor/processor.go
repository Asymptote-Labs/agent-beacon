// Package claudeapibodyprocessor applies Beacon's policy for Claude Code API body events to every
// log record before any exporter sees it.
//
// The policy has to run here rather than in an exporter. The logs pipeline fans out to splunk_hec,
// the upstream exporter that forwards OTLP attributes as they arrive, so a body stripped only by
// beaconjson would still reach a configured SIEM with the system prompt, conversation history and
// tool results in it.
package claudeapibodyprocessor

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processorhelper"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/beaconevent"
)

var componentType = component.MustNewType("claude_api_body")

type Config struct{}

func NewFactory() processor.Factory {
	return processor.NewFactory(
		componentType,
		func() component.Config { return &Config{} },
		processor.WithLogs(createLogs, component.StabilityLevelBeta),
	)
}

func createLogs(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Logs) (processor.Logs, error) {
	return processorhelper.NewLogs(ctx, set, cfg, next, processLogs,
		processorhelper.WithCapabilities(consumer.Capabilities{MutatesData: true}))
}

func processLogs(_ context.Context, logs plog.Logs) (plog.Logs, error) {
	logs.ResourceLogs().RemoveIf(func(resource plog.ResourceLogs) bool {
		resource.ScopeLogs().RemoveIf(func(scope plog.ScopeLogs) bool {
			scope.LogRecords().RemoveIf(beaconevent.SanitizeClaudeAPIBodyRecord)
			return scope.LogRecords().Len() == 0
		})
		return resource.ScopeLogs().Len() == 0
	})
	if logs.LogRecordCount() == 0 {
		return logs, processorhelper.ErrSkipProcessingData
	}
	return logs, nil
}
