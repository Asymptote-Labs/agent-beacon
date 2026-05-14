package beaconjsonexporter

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

type beaconExporter struct {
	cfg    *Config
	writer jsonlWriter
	logger *zap.Logger
}

func newExporter(raw component.Config, set exporter.Settings) (*beaconExporter, error) {
	cfg, ok := raw.(*Config)
	if !ok {
		return nil, fmt.Errorf("unexpected config type %T", raw)
	}
	if cfg.MaxEventBytes == 0 {
		cfg.MaxEventBytes = defaultMaxEventBytes
	}
	if cfg.RotateBytes == 0 {
		cfg.RotateBytes = defaultRotateBytes
	}
	if cfg.ContentRetention == "" {
		cfg.ContentRetention = "full"
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &beaconExporter{
		cfg: cfg,
		writer: jsonlWriter{
			path:          cfg.Path,
			maxEventBytes: cfg.MaxEventBytes,
			rotateBytes:   cfg.RotateBytes,
			redactSecrets: cfg.RedactSecrets,
		},
		logger: set.Logger,
	}, nil
}

func (e *beaconExporter) consumeLogs(ctx context.Context, logs plog.Logs) error {
	_ = ctx
	var firstErr error
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		resourceLogs := logs.ResourceLogs().At(i)
		resourceAttrs := attrsToMap(resourceLogs.Resource().Attributes())
		for j := 0; j < resourceLogs.ScopeLogs().Len(); j++ {
			scopeLogs := resourceLogs.ScopeLogs().At(j)
			for k := 0; k < scopeLogs.LogRecords().Len(); k++ {
				record := scopeLogs.LogRecords().At(k)
				event := e.eventFromLog(resourceAttrs, record)
				if err := e.writer.append(event); err != nil && firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func (e *beaconExporter) consumeTraces(ctx context.Context, traces ptrace.Traces) error {
	_ = ctx
	var firstErr error
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		resourceSpans := traces.ResourceSpans().At(i)
		resourceAttrs := attrsToMap(resourceSpans.Resource().Attributes())
		for j := 0; j < resourceSpans.ScopeSpans().Len(); j++ {
			scopeSpans := resourceSpans.ScopeSpans().At(j)
			for k := 0; k < scopeSpans.Spans().Len(); k++ {
				span := scopeSpans.Spans().At(k)
				if shouldDropSpan(resourceAttrs, span) {
					continue
				}
				event := e.eventFromSpan(resourceAttrs, span)
				if err := e.writer.append(event); err != nil && firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func (e *beaconExporter) consumeMetrics(ctx context.Context, metrics pmetric.Metrics) error {
	_ = ctx
	var firstErr error
	for i := 0; i < metrics.ResourceMetrics().Len(); i++ {
		resourceMetrics := metrics.ResourceMetrics().At(i)
		resourceAttrs := attrsToMap(resourceMetrics.Resource().Attributes())
		for j := 0; j < resourceMetrics.ScopeMetrics().Len(); j++ {
			scopeMetrics := resourceMetrics.ScopeMetrics().At(j)
			for k := 0; k < scopeMetrics.Metrics().Len(); k++ {
				metric := scopeMetrics.Metrics().At(k)
				event := e.eventFromMetric(resourceAttrs, metric)
				if err := e.writer.append(event); err != nil && firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func (e *beaconExporter) eventFromLog(resourceAttrs map[string]interface{}, record plog.LogRecord) beaconEvent {
	attrs := mergeMaps(resourceAttrs, attrsToMap(record.Attributes()))
	ts := timestamp(record.Timestamp().AsTime())
	action := firstString(attrs, "beacon.event.action", "event.action", "gen_ai.agent.action", "ai.agent.action")
	if action == "" {
		action = inferAction(attrs, record.Body().AsString())
	}
	message := firstNonEmpty(record.Body().AsString(), firstString(attrs, "message", "log.message"))
	event := newBeaconEvent(action, eventCategory(action, firstString(attrs, "beacon.event.category", "event.category", "category")), severity(record.SeverityText(), record.SeverityNumber().String()), harnessName(attrs, message), ts)
	event.Message = message
	e.populateCommon(&event, attrs)
	event.Raw = e.rawPayload(attrs, map[string]interface{}{
		"otel_signal": "logs",
		"severity":    record.SeverityText(),
	})
	return event
}

func (e *beaconExporter) eventFromSpan(resourceAttrs map[string]interface{}, span ptrace.Span) beaconEvent {
	attrs := mergeMaps(resourceAttrs, attrsToMap(span.Attributes()))
	action := firstString(attrs, "beacon.event.action", "event.action", "gen_ai.agent.action", "ai.agent.action")
	if action == "" {
		action = inferAction(attrs, span.Name())
	}
	message := firstNonEmpty(firstString(attrs, "message", "gen_ai.prompt", "gen_ai.response"), span.Name())
	event := newBeaconEvent(action, eventCategory(action, firstString(attrs, "beacon.event.category", "event.category", "tool")), spanSeverity(span.Status().Code().String()), harnessName(attrs, message, span.Name()), timestamp(span.StartTimestamp().AsTime()))
	event.Message = message
	e.populateCommon(&event, attrs)
	event.Raw = e.rawPayload(attrs, map[string]interface{}{
		"otel_signal": "traces",
		"span_name":   span.Name(),
		"span_kind":   span.Kind().String(),
		"status":      span.Status().Code().String(),
	})
	return event
}

func shouldDropSpan(resourceAttrs map[string]interface{}, span ptrace.Span) bool {
	attrs := mergeMaps(resourceAttrs, attrsToMap(span.Attributes()))
	if harnessName(attrs, span.Name()) != "codex_cli" {
		return false
	}
	return isCodexInternalSpan(span.Name())
}

func isCodexInternalSpan(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return true
	}
	keepPrefixes := []string{
		"codex.",
		"op.dispatch",
		"session_task.",
		"model_client.",
		"run_turn",
		"run_sampling_request",
		"try_run_sampling_request",
		"stream_request",
		"handle_responses",
	}
	for _, prefix := range keepPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return false
		}
	}
	dropExact := map[string]struct{}{
		"receiving":                     {},
		"receiving_stream":              {},
		"poll":                          {},
		"poll_ready":                    {},
		"popped":                        {},
		"pop_frame":                     {},
		"reserve_capacity":              {},
		"try_assign_capacity":           {},
		"try_reclaim_frame":             {},
		"assign_connection_capacity":    {},
		"updating stream flow":          {},
		"updating connection flow":      {},
		"recv_stream_window_update":     {},
		"recv_connection_window_update": {},
		"send_data":                     {},
	}
	if _, ok := dropExact[normalized]; ok {
		return true
	}
	dropPrefixes := []string{
		"framedread::",
		"framedwrite::",
		"hpack::",
		"prioritize::",
	}
	for _, prefix := range dropPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func (e *beaconExporter) eventFromMetric(resourceAttrs map[string]interface{}, metric pmetric.Metric) beaconEvent {
	attrs := mergeMaps(resourceAttrs, map[string]interface{}{})
	action := firstString(attrs, "beacon.event.action", "event.action")
	if action == "" {
		action = "metric.observed"
	}
	event := newBeaconEvent(action, "metric", "info", harnessName(attrs, metric.Name()), time.Now().UTC())
	event.Message = metric.Name()
	e.populateCommon(&event, attrs)
	event.Raw = e.rawPayload(attrs, map[string]interface{}{
		"otel_signal":        "metrics",
		"metric_name":        metric.Name(),
		"metric_description": metric.Description(),
		"metric_unit":        metric.Unit(),
	})
	return event
}

func (e *beaconExporter) populateCommon(event *beaconEvent, attrs map[string]interface{}) {
	event.Model = firstString(attrs, "gen_ai.request.model", "gen_ai.response.model", "model", "ai.model")
	event.Repository = firstString(attrs, "vcs.repository.url", "repository", "repo.path", "workspace.repository")
	event.Branch = firstString(attrs, "vcs.branch.name", "git.branch", "branch")
	if id := firstString(attrs, "session.id", "conversation.id", "conversation_id", "gen_ai.conversation.id"); id != "" || firstString(attrs, "cwd", "working_directory") != "" {
		event.Session = &sessionInfo{
			ID:               id,
			WorkingDirectory: firstString(attrs, "cwd", "working_directory", "process.command_args.cwd"),
		}
	}
	if name := firstString(attrs, "tool.name", "gen_ai.tool.name", "mcp.tool.name"); name != "" || firstString(attrs, "tool.command", "command") != "" {
		event.Tool = &toolInfo{
			Name:    name,
			Command: firstString(attrs, "tool.command", "command", "process.command_line"),
			Path:    firstString(attrs, "tool.path", "file.path"),
		}
	}
	if path := firstString(attrs, "file.path", "file_path", "code.filepath"); path != "" {
		event.File = &fileInfo{
			Path:      path,
			Operation: firstString(attrs, "file.operation", "operation"),
			Language:  firstString(attrs, "code.language", "language"),
		}
	}
	if command := firstString(attrs, "command", "process.command_line", "shell.command"); command != "" {
		event.Command = &commandInfo{Command: command}
		if exitCode, ok := intAttr(attrs, "exit_code", "process.exit_code", "command.exit_code"); ok {
			event.Command.ExitCode = &exitCode
		}
		if duration, ok := int64Attr(attrs, "duration_ms", "command.duration_ms"); ok {
			event.Command.DurationMS = duration
		}
	}
	if server := firstString(attrs, "mcp.server.name", "mcp.server", "gen_ai.mcp.server"); server != "" || firstString(attrs, "mcp.tool.name") != "" {
		event.MCP = &mcpInfo{
			Server: server,
			Tool:   firstString(attrs, "mcp.tool.name", "tool.name"),
		}
	}
	if decision := firstString(attrs, "approval.decision", "policy.decision"); decision != "" {
		event.Approval = &approvalInfo{
			Required: true,
			Decision: decision,
			Reason:   firstString(attrs, "approval.reason", "policy.reason"),
		}
	}
	if e.cfg.ContentRetention != "metadata" && event.Event.Category == "prompt" {
		if text := firstString(attrs, "gen_ai.prompt", "prompt", "user_prompt", "input.prompt"); text != "" {
			event.Prompt = &promptInfo{Text: text}
		}
	}
	event.Content = &contentInfo{Retention: e.cfg.ContentRetention, Included: e.cfg.ContentRetention != "metadata", Redacted: e.cfg.ContentRetention == "redacted"}
}

func (e *beaconExporter) rawPayload(attrs map[string]interface{}, extra map[string]interface{}) map[string]interface{} {
	raw := map[string]interface{}{}
	for k, v := range extra {
		raw[k] = v
	}
	if e.cfg.ContentRetention == "metadata" {
		raw["attribute_count"] = len(attrs)
		return raw
	}
	raw["attributes"] = attrs
	return raw
}

func attrsToMap(attrs pcommon.Map) map[string]interface{} {
	out := make(map[string]interface{}, attrs.Len())
	attrs.Range(func(k string, v pcommon.Value) bool {
		out[k] = v.AsRaw()
		return true
	})
	return out
}

func mergeMaps(a, b map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func firstString(attrs map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := attrs[key]; ok {
			if str := strings.TrimSpace(fmt.Sprint(value)); str != "" && str != "<nil>" {
				return str
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func intAttr(attrs map[string]interface{}, keys ...string) (int, bool) {
	value, ok := int64Attr(attrs, keys...)
	return int(value), ok
}

func int64Attr(attrs map[string]interface{}, keys ...string) (int64, bool) {
	for _, key := range keys {
		switch typed := attrs[key].(type) {
		case int:
			return int64(typed), true
		case int64:
			return typed, true
		case float64:
			return int64(typed), true
		}
	}
	return 0, false
}

func timestamp(ts time.Time) time.Time {
	if ts.IsZero() || ts.UnixNano() == 0 {
		return time.Now().UTC()
	}
	return ts
}

func severity(text, number string) string {
	lower := strings.ToLower(text + " " + number)
	switch {
	case strings.Contains(lower, "fatal") || strings.Contains(lower, "critical"):
		return "critical"
	case strings.Contains(lower, "error"):
		return "high"
	case strings.Contains(lower, "warn"):
		return "medium"
	default:
		return "info"
	}
}

func spanSeverity(status string) string {
	if strings.Contains(strings.ToLower(status), "error") {
		return "high"
	}
	return "info"
}

func harnessName(attrs map[string]interface{}, hints ...string) string {
	name := firstString(attrs, "beacon.harness.name", "harness.name", "service.name", "telemetry.sdk.name")
	if explicit := firstString(attrs, "beacon.harness.name", "harness.name"); explicit != "" {
		return normalizeHarnessName(explicit)
	}
	candidates := append([]string{name}, hints...)
	for _, candidate := range candidates {
		if normalized := normalizeHarnessName(candidate); normalized != "" {
			return normalized
		}
	}
	if name != "" {
		return name
	}
	return "otel"
}

func normalizeHarnessName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case lower == "":
		return ""
	case strings.Contains(lower, "cowork") || strings.Contains(lower, "co-work"):
		return "claude_cowork"
	case strings.Contains(lower, "claude_code") || strings.Contains(lower, "claude-code") || strings.Contains(lower, "claude code") || strings.HasPrefix(lower, "claude_code."):
		return "claude_code"
	case lower == "claude" || strings.Contains(lower, "claude"):
		return "claude_code"
	case strings.Contains(lower, "codex"):
		return "codex_cli"
	case name != "":
		return name
	default:
		return ""
	}
}

func inferAction(attrs map[string]interface{}, fallback string) string {
	tool := strings.ToLower(firstString(attrs, "tool.name", "gen_ai.tool.name", "mcp.tool.name"))
	text := strings.ToLower(strings.Join([]string{
		fallback,
		tool,
		firstString(attrs, "event.name", "codex.op", "rpc.method"),
	}, " "))
	switch {
	case strings.Contains(text, "prompt") || strings.Contains(text, "user_input"):
		return "prompt.submitted"
	case strings.Contains(text, "mcp"):
		return "mcp.tool_invoked"
	case strings.Contains(text, "command") || strings.Contains(text, "shell") || strings.Contains(text, "exec"):
		return "command.executed"
	case strings.Contains(text, "file") || strings.Contains(text, "write") || strings.Contains(text, "edit"):
		return "file.modified"
	case strings.Contains(text, "approval"):
		return "approval.requested"
	default:
		return "tool.invoked"
	}
}

func eventCategory(action, explicit string) string {
	if explicit != "" {
		return explicit
	}
	switch {
	case strings.HasPrefix(action, "prompt."):
		return "prompt"
	case strings.HasPrefix(action, "command."):
		return "command"
	case strings.HasPrefix(action, "file."):
		return "file"
	case strings.HasPrefix(action, "mcp."):
		return "mcp"
	case strings.HasPrefix(action, "approval.") || strings.HasPrefix(action, "policy."):
		return "approval"
	case strings.HasPrefix(action, "metric."):
		return "metric"
	case strings.HasPrefix(action, "tool."):
		return "tool"
	default:
		return ""
	}
}
