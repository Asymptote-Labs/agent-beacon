package beaconjsonexporter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestConfigValidateRequiresPath(t *testing.T) {
	cfg := createDefaultConfig()
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing path error")
	}
	cfg.Path = filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func TestConsumeLogsWritesBeaconJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:             path,
		MaxEventBytes:    defaultMaxEventBytes,
		RotateBytes:      defaultRotateBytes,
		RedactSecrets:    true,
		ContentRetention: "redacted",
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "claude-cowork")
	sl := rl.ScopeLogs().AppendEmpty()
	rec := sl.LogRecords().AppendEmpty()
	rec.Body().SetStr("tool call token=super-secret")
	rec.Attributes().PutStr("beacon.event.action", "tool.invoked")
	rec.Attributes().PutStr("session.id", "session-1")
	rec.Attributes().PutStr("tool.name", "Shell")
	rec.Attributes().PutStr("command", "kubectl get pods")

	if err := exp.consumeLogs(context.Background(), logs); err != nil {
		t.Fatalf("consumeLogs returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	if strings.Contains(string(data), "super-secret") {
		t.Fatalf("expected secret to be redacted: %s", string(data))
	}
	var event beaconEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if event.Vendor != vendor || event.Event.Action != "tool.invoked" || event.Harness.Name != "claude_cowork" {
		t.Fatalf("unexpected event: %#v", event)
	}
	if event.Event.Category != "tool" {
		t.Fatalf("event category = %q, want tool", event.Event.Category)
	}
	if event.Session == nil || event.Session.ID != "session-1" {
		t.Fatalf("session missing from event: %#v", event.Session)
	}
	if event.Command == nil || event.Command.Command != "kubectl get pods" {
		t.Fatalf("command missing from event: %#v", event.Command)
	}
}

func TestEventCategoryInfersFromAction(t *testing.T) {
	tests := map[string]string{
		"tool.invoked":       "tool",
		"file.modified":      "file",
		"command.executed":   "command",
		"mcp.tool_invoked":   "mcp",
		"approval.requested": "approval",
		"policy.blocked":     "approval",
		"prompt.submitted":   "prompt",
		"metric.observed":    "metric",
	}
	for action, want := range tests {
		if got := eventCategory(action, ""); got != want {
			t.Fatalf("eventCategory(%q) = %q, want %q", action, got, want)
		}
	}
	if got := eventCategory("tool.invoked", "custom"); got != "custom" {
		t.Fatalf("explicit category = %q, want custom", got)
	}
}

func TestMetadataRetentionDropsRawAttributes(t *testing.T) {
	exp, err := newExporter(&Config{
		Path:             filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes:    defaultMaxEventBytes,
		RotateBytes:      defaultRotateBytes,
		RedactSecrets:    true,
		ContentRetention: "metadata",
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	raw := exp.rawPayload(map[string]interface{}{"prompt": "do a thing"}, map[string]interface{}{"otel_signal": "logs"})
	if _, ok := raw["attributes"]; ok {
		t.Fatalf("metadata retention should not include raw attributes: %#v", raw)
	}
	if raw["attribute_count"] != 1 {
		t.Fatalf("attribute count missing: %#v", raw)
	}
}

func TestCodexInternalSpanFilter(t *testing.T) {
	codexAttrs := map[string]interface{}{"service.name": "codex-cli"}

	if !shouldDropSpan(codexAttrs, testSpan("FramedRead::poll_next")) {
		t.Fatal("expected Codex transport span to be dropped")
	}
	if shouldDropSpan(codexAttrs, testSpan("session_task.turn")) {
		t.Fatal("expected meaningful Codex session span to be kept")
	}
	if shouldDropSpan(map[string]interface{}{"service.name": "other-agent"}, testSpan("FramedRead::poll_next")) {
		t.Fatal("expected non-Codex span to be kept")
	}
}

func TestConsumeTracesDropsCodexInternalSpans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:             path,
		MaxEventBytes:    defaultMaxEventBytes,
		RotateBytes:      defaultRotateBytes,
		RedactSecrets:    true,
		ContentRetention: "metadata",
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "codex-cli")
	spans := rs.ScopeSpans().AppendEmpty().Spans()
	spans.AppendEmpty().SetName("FramedRead::poll_next")
	spans.AppendEmpty().SetName("session_task.turn")

	if err := exp.consumeTraces(context.Background(), traces); err != nil {
		t.Fatalf("consumeTraces returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "FramedRead::poll_next") {
		t.Fatalf("transport span was written: %s", text)
	}
	if !strings.Contains(text, "session_task.turn") {
		t.Fatalf("meaningful span was not written: %s", text)
	}
}

func testSpan(name string) ptrace.Span {
	traces := ptrace.NewTraces()
	span := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName(name)
	return span
}

func TestHarnessNameSeparatesClaudeCodeAndCowork(t *testing.T) {
	tests := []struct {
		name  string
		attrs map[string]interface{}
		hints []string
		want  string
	}{
		{
			name:  "claude code log body",
			attrs: map[string]interface{}{"service.name": "claude"},
			hints: []string{"claude_code.api_request"},
			want:  "claude_code",
		},
		{
			name:  "claude code metric",
			attrs: map[string]interface{}{"service.name": "claude-code"},
			hints: []string{"claude_code.token.usage"},
			want:  "claude_code",
		},
		{
			name:  "claude cowork service",
			attrs: map[string]interface{}{"service.name": "claude-cowork"},
			want:  "claude_cowork",
		},
		{
			name:  "codex service",
			attrs: map[string]interface{}{"service.name": "codex-cli"},
			want:  "codex_cli",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := harnessName(tt.attrs, tt.hints...); got != tt.want {
				t.Fatalf("harnessName() = %q, want %q", got, tt.want)
			}
		})
	}
}
