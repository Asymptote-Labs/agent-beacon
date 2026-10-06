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
)

// Claude Code's events of one turn reach the runtime log under one prompt.id, written by the real
// exporter. The prompt whose text is too large for the event ceiling loses its text to compaction
// and keeps its id, because the id is what places it in the turn.
func TestConsumeLogsWritesClaudePromptIDThroughCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:          path,
		MaxEventBytes: 4096, // below the 4 KiB the prompt keeps after truncation, so compaction runs
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "claude-code")
	records := rl.ScopeLogs().AppendEmpty().LogRecords()
	add := func(name string, attrs map[string]interface{}) {
		rec := records.AppendEmpty()
		rec.Body().SetStr("claude_code." + name)
		attrs["event.name"] = name
		attrs["session.id"] = "claude-session"
		attrs["prompt.id"] = "6f1c2c1e-prompt"
		if err := rec.Attributes().FromRaw(attrs); err != nil {
			t.Fatal(err)
		}
	}
	add("user_prompt", map[string]interface{}{"prompt": strings.Repeat("summarize this log ", 4000), "prompt_length": "76000"})
	add("tool_result", map[string]interface{}{"tool_name": "Bash", "success": "true", "tool_use_id": "toolu_1"})
	add("api_request", map[string]interface{}{"model": "claude-sonnet-4-5", "input_tokens": "10", "output_tokens": "4"})

	if err := exp.consumeLogs(context.Background(), logs); err != nil {
		t.Fatalf("consumeLogs returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("wrote %d events, want 3:\n%s", len(lines), data)
	}
	for _, line := range lines {
		var event struct {
			Event struct {
				Action string `json:"action"`
			} `json:"event"`
			Prompt *struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"prompt"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Prompt == nil || event.Prompt.ID != "6f1c2c1e-prompt" {
			t.Fatalf("%s prompt = %+v, want the turn's id", event.Event.Action, event.Prompt)
		}
		if event.Prompt.Text != "" {
			t.Fatalf("%s kept %d bytes of prompt text past the ceiling", event.Event.Action, len(event.Prompt.Text))
		}
	}
}
