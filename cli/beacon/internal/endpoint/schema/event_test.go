package schema

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewEventSetsRequiredInvariants(t *testing.T) {
	event := NewEvent(NewEventOptions{
		Action:       "telemetry.enabled",
		Category:     "telemetry",
		AgentVersion: "test-version",
		Harness:      HarnessInfo{Name: "endpoint"},
		Message:      "configured",
	})

	if err := event.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if event.Vendor != Vendor || event.Product != Product || event.SchemaVersion != SchemaVersion {
		t.Fatalf("unexpected schema identity: %#v", event)
	}
	if event.Event.Kind != "agent_runtime" || event.Event.Action != "telemetry.enabled" || event.Event.Category != "telemetry" {
		t.Fatalf("unexpected event info: %#v", event.Event)
	}
	if event.Severity != SeverityInfo {
		t.Fatalf("default severity = %q, want %q", event.Severity, SeverityInfo)
	}
	if event.Endpoint.OS != runtime.GOOS || event.Endpoint.AgentVersion != "test-version" {
		t.Fatalf("unexpected endpoint info: %#v", event.Endpoint)
	}
	if _, err := time.Parse(time.RFC3339, event.Timestamp); err != nil {
		t.Fatalf("timestamp is not RFC3339: %q", event.Timestamp)
	}
	event.File = &FileInfo{Path: "main.go", Operation: "modify"}
	event.Command = &CommandInfo{Command: "go test ./..."}
	event.MCP = &MCPInfo{Server: "github", Tool: "get_issue"}
	event.Prompt = &PromptInfo{Text: "Summarize this file"}
	event.Content = &ContentInfo{Retention: ContentRetentionFull, Included: true}
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate rejected optional telemetry fields: %v", err)
	}
}

func TestValidateContentRetentionValues(t *testing.T) {
	for _, retention := range []string{ContentRetentionMetadata, ContentRetentionRedacted, ContentRetentionFull} {
		t.Run(retention, func(t *testing.T) {
			event := NewEvent(NewEventOptions{
				Action:  "tool.invoked",
				Harness: HarnessInfo{Name: "cursor"},
			})
			event.Content = &ContentInfo{Retention: retention, Included: retention != ContentRetentionMetadata}

			if err := event.Validate(); err != nil {
				t.Fatalf("Validate rejected retention %q: %v", retention, err)
			}
		})
	}
}

func TestValidateRejectsMissingOrInvalidRequiredFields(t *testing.T) {
	valid := NewEvent(NewEventOptions{
		Action:   "tool.invoked",
		Harness:  HarnessInfo{Name: "cursor"},
		Severity: SeverityHigh,
	})

	tests := []struct {
		name string
		edit func(*Event)
		want string
	}{
		{
			name: "vendor",
			edit: func(e *Event) { e.Vendor = "other" },
			want: "vendor must be beacon",
		},
		{
			name: "product",
			edit: func(e *Event) { e.Product = "other" },
			want: "product must be endpoint-agent",
		},
		{
			name: "schema version",
			edit: func(e *Event) { e.SchemaVersion = "" },
			want: "schema_version is required",
		},
		{
			name: "action",
			edit: func(e *Event) { e.Event.Action = "" },
			want: "event.kind and event.action are required",
		},
		{
			name: "severity",
			edit: func(e *Event) { e.Severity = "" },
			want: "severity is required",
		},
		{
			name: "os",
			edit: func(e *Event) { e.Endpoint.OS = "" },
			want: "endpoint.os is required",
		},
		{
			name: "harness",
			edit: func(e *Event) { e.Harness.Name = "" },
			want: "harness.name is required",
		},
		{
			name: "content retention",
			edit: func(e *Event) { e.Content = &ContentInfo{Retention: "raw", Included: true} },
			want: "content.retention must be metadata, redacted, or full",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := valid
			tt.edit(&event)
			err := event.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate error = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestTokenUsageJSONRoundTrip guards against silent schema drift between the
// beaconevent.TokenUsage (written by the collector) and schema.TokenUsage (read
// by the CLI dashboard). Both types are defined independently across separate Go
// modules, so field additions or JSON tag changes won't produce a compile error.
// This test encodes a JSONL line that mimics what the collector writes and
// verifies that schema.Event deserialises the tokens field correctly.
func TestTokenUsageJSONRoundTrip(t *testing.T) {
	// Construct the same JSON the collector exporter would emit.
	raw := `{
		"timestamp":"2026-01-01T00:00:00Z",
		"vendor":"beacon",
		"product":"endpoint-agent",
		"schema_version":"1.0",
		"event":{"kind":"agent_runtime","action":"metric.observed","category":"metric"},
		"severity":"info",
		"endpoint":{"os":"darwin"},
		"harness":{"name":"claude_code"},
		"tokens":{"input":500,"output":150,"cache_read":300,"cache_write":80},
		"message":"gen_ai.client.token.usage"
	}`

	var event Event
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if event.Tokens == nil {
		t.Fatal("Tokens is nil after unmarshal; 'tokens' field may be missing or misnamed in schema.Event")
	}
	if event.Tokens.Input != 500 {
		t.Errorf("Input = %d, want 500", event.Tokens.Input)
	}
	if event.Tokens.Output != 150 {
		t.Errorf("Output = %d, want 150", event.Tokens.Output)
	}
	if event.Tokens.CacheRead != 300 {
		t.Errorf("CacheRead = %d, want 300", event.Tokens.CacheRead)
	}
	if event.Tokens.CacheWrite != 80 {
		t.Errorf("CacheWrite = %d, want 80", event.Tokens.CacheWrite)
	}
	if got := event.Tokens.Total(); got != 1030 {
		t.Errorf("Total() = %d, want 1030", got)
	}

	// Re-encode and verify omitempty: zero fields must not appear in output.
	partial := &TokenUsage{Output: 200}
	encoded, err := json.Marshal(partial)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	out := string(encoded)
	if strings.Contains(out, `"input"`) {
		t.Errorf("zero Input field should be omitted, got: %s", out)
	}
	if !strings.Contains(out, `"output":200`) {
		t.Errorf("non-zero Output field should be present, got: %s", out)
	}
}
