// Package claudeotel builds the environment that sends Claude Code's OpenTelemetry export to a
// Beacon collector. `beacon ci` lays it over a CI job's environment and `beacon endpoint` writes it
// into Claude Code's settings.json, so both send every signal to the same place.
package claudeotel

import "strings"

// protocol is the one Beacon's collector endpoint speaks.
const protocol = "grpc"

var signals = []string{"LOGS", "METRICS", "TRACES"}

// OTLPEnv returns the variables that send Claude Code's logs, metrics and traces to endpoint.
//
// caller is the environment Claude Code would otherwise run with. Claude Code lets a per-signal
// endpoint or protocol replace the generic one, merges per-signal headers into the generic ones, and
// sends detailed beta tracing to BETA_TRACING_ENDPOINT instead of through its exporters. Any of these
// set there would send a signal past the collector, or hand the collector credentials meant for
// another one, so each that caller sets is cancelled here: Claude Code and the OpenTelemetry SDK treat
// an empty value as unset. Variables caller leaves unset are not written, so nothing new reaches
// other programs that inherit the result.
func OTLPEnv(endpoint string, caller map[string]string) map[string]string {
	env := map[string]string{
		"CLAUDE_CODE_ENABLE_TELEMETRY": "1",
		"OTEL_LOGS_EXPORTER":           "otlp",
		"OTEL_METRICS_EXPORTER":        "otlp",
		// Delta temporality keeps token usage counters per-interval so downstream
		// aggregation can sum datapoints without deduping cumulative series.
		"OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE": "delta",
		"OTEL_EXPORTER_OTLP_PROTOCOL":                       protocol,
		"OTEL_EXPORTER_OTLP_ENDPOINT":                       endpoint,
		// Tool details expose the command, file path, URL, search pattern, and MCP
		// identity/arguments needed to reconstruct agent activity from OTel events.
		"OTEL_LOG_TOOL_DETAILS": "1",
		"OTEL_LOG_USER_PROMPTS": "1",
	}
	override := func(key, value string) {
		if strings.TrimSpace(caller[key]) != "" {
			env[key] = value
		}
	}
	override("OTEL_EXPORTER_OTLP_HEADERS", "")
	for _, signal := range signals {
		override("OTEL_EXPORTER_OTLP_"+signal+"_PROTOCOL", protocol)
		override("OTEL_EXPORTER_OTLP_"+signal+"_HEADERS", "")
	}
	for _, key := range RedirectKeys() {
		override(key, "")
	}
	return env
}

// RedirectKeys are the variables that send a Claude Code signal somewhere other than
// OTEL_EXPORTER_OTLP_ENDPOINT.
func RedirectKeys() []string {
	keys := make([]string, 0, len(signals)+1)
	for _, signal := range signals {
		keys = append(keys, "OTEL_EXPORTER_OTLP_"+signal+"_ENDPOINT")
	}
	return append(keys, "BETA_TRACING_ENDPOINT")
}

// InlineRawBodies reports whether an OTEL_LOG_RAW_API_BODIES value has Claude Code put API bodies
// inline in its log events, rather than leave them off or write them to files Beacon never reads.
func InlineRawBodies(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true":
		return true
	}
	return false
}
