package schema

import (
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
	event.Content = &ContentInfo{Retention: "metadata", Included: false}
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate rejected optional telemetry fields: %v", err)
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
