package cmd

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestExtractGenAIUsage(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]interface{}
		want  map[string]interface{}
	}{
		{
			name:  "no usage returns nil",
			input: map[string]interface{}{"conversation_id": "conv-1", "model": "claude-4.5-sonnet"},
			want:  nil,
		},
		{
			name: "tokens container snake case",
			input: map[string]interface{}{
				"tokens": map[string]interface{}{
					"input_tokens":      float64(1200),
					"output_tokens":     float64(300),
					"cache_read_tokens": float64(5000),
				},
			},
			want: map[string]interface{}{
				"input_tokens":  int64(1200),
				"output_tokens": int64(300),
				"cache_read":    map[string]interface{}{"input_tokens": int64(5000)},
			},
		},
		{
			name: "token_usage container camel case",
			input: map[string]interface{}{
				"token_usage": map[string]interface{}{
					"inputTokens":           float64(10),
					"outputTokens":          float64(20),
					"cacheWriteTokens":      float64(30),
					"reasoningOutputTokens": float64(40),
				},
			},
			want: map[string]interface{}{
				"input_tokens":   int64(10),
				"output_tokens":  int64(20),
				"cache_creation": map[string]interface{}{"input_tokens": int64(30)},
				"reasoning":      map[string]interface{}{"output_tokens": int64(40)},
			},
		},
		{
			name: "usage container with prompt and completion aliases",
			input: map[string]interface{}{
				"usage": map[string]interface{}{
					"prompt_tokens":     float64(7),
					"completion_tokens": float64(11),
					"cost":              0.42,
				},
			},
			want: map[string]interface{}{
				"input_tokens":  int64(7),
				"output_tokens": int64(11),
				"cost_usd":      0.42,
			},
		},
		{
			name: "bare top-level fields",
			input: map[string]interface{}{
				"input_tokens":  float64(3),
				"output_tokens": float64(4),
				"cost_usd":      0.01,
			},
			want: map[string]interface{}{
				"input_tokens":  int64(3),
				"output_tokens": int64(4),
				"cost_usd":      0.01,
			},
		},
		{
			name: "bare top-level cost is ignored",
			input: map[string]interface{}{
				"input_tokens": float64(3),
				"cost":         0.5,
			},
			want: map[string]interface{}{
				"input_tokens": int64(3),
			},
		},
		{
			name: "total tokens alone is ignored",
			input: map[string]interface{}{
				"tokens": map[string]interface{}{"total_tokens": float64(999)},
			},
			want: nil,
		},
		{
			name: "negative counts are dropped",
			input: map[string]interface{}{
				"tokens": map[string]interface{}{
					"input_tokens":  float64(-5),
					"output_tokens": float64(8),
				},
			},
			want: map[string]interface{}{"output_tokens": int64(8)},
		},
		{
			name: "numeric strings are parsed",
			input: map[string]interface{}{
				"tokenCount": map[string]interface{}{
					"inputTokens":  "150",
					"outputTokens": "25",
				},
			},
			want: map[string]interface{}{
				"input_tokens":  int64(150),
				"output_tokens": int64(25),
			},
		},
		{
			name: "non-numeric strings are ignored",
			input: map[string]interface{}{
				"usage": map[string]interface{}{"input_tokens": "lots"},
			},
			want: nil,
		},
		{
			name: "nested container wins over bare fields",
			input: map[string]interface{}{
				"tokens":       map[string]interface{}{"input_tokens": float64(100)},
				"input_tokens": float64(999),
			},
			want: map[string]interface{}{"input_tokens": int64(100)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractGenAIUsage(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("extractGenAIUsage() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCursorAgentThoughtRecordsUsageWhenPayloadCarriesTokens(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runAgentThought, map[string]interface{}{
		"conversation_id": "conv-usage",
		"hook_event_name": "afterAgentThought",
		"text":            "considering the diff",
		"model":           "claude-4.5-sonnet",
		"tokens": map[string]interface{}{
			"input_tokens":      float64(1200),
			"output_tokens":     float64(300),
			"cache_read_tokens": float64(5000),
		},
	})

	event := lastEndpointEvent(t, logPath)
	if model := event["model"]; model != "claude-4.5-sonnet" {
		t.Fatalf("model = %q, want claude-4.5-sonnet", model)
	}
	genAI := event["gen_ai"].(map[string]interface{})
	if _, ok := genAI["output"]; !ok {
		t.Fatalf("gen_ai.output should survive the usage merge: %#v", genAI)
	}
	usage := genAI["usage"].(map[string]interface{})
	if got := usage["input_tokens"]; got != float64(1200) {
		t.Fatalf("gen_ai.usage.input_tokens = %v, want 1200", got)
	}
	if got := usage["output_tokens"]; got != float64(300) {
		t.Fatalf("gen_ai.usage.output_tokens = %v, want 300", got)
	}
	cacheRead := usage["cache_read"].(map[string]interface{})
	if got := cacheRead["input_tokens"]; got != float64(5000) {
		t.Fatalf("gen_ai.usage.cache_read.input_tokens = %v, want 5000", got)
	}
}

func TestCursorPromptSubmitOmitsUsageWhenAbsent(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"conversation_id": "conv-plain",
		"hook_event_name": "beforeSubmitPrompt",
		"prompt":          "list files",
	})

	event := lastEndpointEvent(t, logPath)
	if genAI, ok := event["gen_ai"].(map[string]interface{}); ok {
		if _, ok := genAI["usage"]; ok {
			t.Fatalf("gen_ai.usage should be absent for a usage-free payload: %#v", genAI)
		}
	}
}

func TestCursorFileEditRecordsUsageThroughRecordLocalEdit(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"conversation_id": "conv-edit-usage",
		"hook_event_name": "afterFileEdit",
		"file_path":       "/repo/main.go",
		"edits": []interface{}{
			map[string]interface{}{"old_string": "a\n", "new_string": "b\n"},
		},
		"token_usage": map[string]interface{}{
			"inputTokens":  float64(64),
			"outputTokens": float64(16),
		},
	})

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified", action)
	}
	usage := event["gen_ai"].(map[string]interface{})["usage"].(map[string]interface{})
	if got := usage["input_tokens"]; got != float64(64) {
		t.Fatalf("gen_ai.usage.input_tokens = %v, want 64", got)
	}
	if got := usage["output_tokens"]; got != float64(16) {
		t.Fatalf("gen_ai.usage.output_tokens = %v, want 16", got)
	}
}
