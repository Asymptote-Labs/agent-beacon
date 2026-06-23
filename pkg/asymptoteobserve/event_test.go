package asymptoteobserve

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
	event.MCP = &MCPInfo{Server: "github", Tool: "get_issue", Method: &MCPMethodInfo{Name: "tools/call"}}
	event.JSONRPC = &JSONRPCInfo{Request: &JSONRPCRequestInfo{ID: "request-7"}, Protocol: &JSONRPCProtocolInfo{Version: "2.0"}}
	event.Network = &NetworkInfo{Protocol: &NetworkProtocolInfo{Name: "http", Version: "2"}, Transport: "tcp"}
	event.RPC = &RPCInfo{Response: &RPCResponseInfo{StatusCode: "OK"}}
	event.Server = &ServerInfo{Address: "example.com", Port: intPtr(443)}
	event.Error = &ErrorInfo{Type: "tool_error"}
	event.Prompt = &PromptInfo{Text: "Summarize this file"}
	event.Content = &ContentInfo{Retention: ContentRetentionMetadata, Included: false}
	event.GenAI = &GenAIInfo{
		Provider: &GenAIProviderInfo{Name: "openai"},
		Request:  &GenAIRequestInfo{Model: "gpt-4o"},
		Usage:    &GenAIUsageInfo{InputTokens: int64Ptr(10), OutputTokens: int64Ptr(20)},
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate rejected optional telemetry fields: %v", err)
	}
}

func TestValidateContentEmptyRetentionAccepted(t *testing.T) {
	event := NewEvent(NewEventOptions{
		Action:  "tool.invoked",
		Harness: HarnessInfo{Name: "cursor"},
	})
	event.Content = &ContentInfo{Retention: "", Included: false}
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate rejected empty content.retention: %v", err)
	}
}

func TestValidateContentRetentionValuesForCompatibility(t *testing.T) {
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

func TestValidateOriginValues(t *testing.T) {
	for _, origin := range []Origin{"", OriginLocal, OriginCloud, OriginCI} {
		t.Run(string(origin), func(t *testing.T) {
			event := NewEvent(NewEventOptions{
				Action:  "tool.invoked",
				Harness: HarnessInfo{Name: "cursor"},
				Origin:  origin,
			})
			if err := event.Validate(); err != nil {
				t.Fatalf("Validate rejected origin %q: %v", origin, err)
			}
		})
	}

	event := NewEvent(NewEventOptions{Action: "tool.invoked", Harness: HarnessInfo{Name: "cursor"}, Origin: "serverless"})
	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "origin must be local, cloud, or ci") {
		t.Fatalf("Validate error = %v, want origin error", err)
	}
}

func int64Ptr(value int64) *int64 {
	return &value
}

func intPtr(value int) *int {
	return &value
}

func float64Ptr(value float64) *float64 {
	return &value
}

func TestTraceAndUsageCostSerializeWithSemconvNames(t *testing.T) {
	event := NewEvent(NewEventOptions{Action: "token.usage", Harness: HarnessInfo{Name: "claude_code"}})
	event.Trace = &TraceInfo{ID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef", ParentSpanID: "fedcba9876543210"}
	event.GenAI = &GenAIInfo{
		Usage: &GenAIUsageInfo{
			InputTokens:   int64Ptr(120),
			OutputTokens:  int64Ptr(45),
			CacheCreation: &GenAIUsageCacheCreationInfo{InputTokens: int64Ptr(30)},
			CacheRead:     &GenAIUsageCacheReadInfo{InputTokens: int64Ptr(90)},
			Reasoning:     &GenAIUsageReasoningInfo{OutputTokens: int64Ptr(15)},
			CostUSD:       float64Ptr(0.0123),
		},
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate rejected trace and usage fields: %v", err)
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`"trace":{"id":"0123456789abcdef0123456789abcdef","span_id":"0123456789abcdef","parent_span_id":"fedcba9876543210"}`,
		`"input_tokens":120`,
		`"output_tokens":45`,
		`"cache_creation":{"input_tokens":30}`,
		`"cache_read":{"input_tokens":90}`,
		`"reasoning":{"output_tokens":15}`,
		`"cost_usd":0.0123`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("JSON missing %s: %s", want, text)
		}
	}
}

func TestMCPStandardContextSerializesWithSemconvNames(t *testing.T) {
	event := NewEvent(NewEventOptions{Action: "mcp.tool_invoked", Category: "mcp", Harness: HarnessInfo{Name: "cursor"}})
	event.MCP = &MCPInfo{
		Method:   &MCPMethodInfo{Name: "tools/call"},
		Protocol: &MCPProtocolInfo{Version: "2025-06-18"},
		Resource: &MCPResourceInfo{URI: "file:///tmp/report.md"},
		Session:  &MCPSessionInfo{ID: "mcp-session"},
	}
	event.GenAI = &GenAIInfo{
		Operation: &GenAIOperationInfo{Name: "execute_tool"},
		Tool:      &GenAIToolInfo{Name: "get_organizations", Call: &GenAIToolCallInfo{ID: "call-1"}},
	}
	event.JSONRPC = &JSONRPCInfo{Request: &JSONRPCRequestInfo{ID: "request-7"}, Protocol: &JSONRPCProtocolInfo{Version: "2.0"}}
	event.Network = &NetworkInfo{Protocol: &NetworkProtocolInfo{Name: "http", Version: "2"}, Transport: "tcp"}
	event.RPC = &RPCInfo{Response: &RPCResponseInfo{StatusCode: "OK"}}
	event.Server = &ServerInfo{Address: "example.com", Port: intPtr(443)}
	event.Error = &ErrorInfo{Type: "tool_error"}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`"gen_ai":{"operation":{"name":"execute_tool"}`,
		`"tool":{"call":{"id":"call-1"},"name":"get_organizations"}`,
		`"mcp":{"method":{"name":"tools/call"},"protocol":{"version":"2025-06-18"},"resource":{"uri":"file:///tmp/report.md"},"session":{"id":"mcp-session"}}`,
		`"jsonrpc":{"request":{"id":"request-7"},"protocol":{"version":"2.0"}}`,
		`"network":{"protocol":{"name":"http","version":"2"},"transport":"tcp"}`,
		`"rpc":{"response":{"status_code":"OK"}}`,
		`"server":{"address":"example.com","port":443}`,
		`"error":{"type":"tool_error"}`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("JSON missing %s: %s", want, text)
		}
	}
}

func TestTraceAndUsageCostOmittedWhenUnset(t *testing.T) {
	event := NewEvent(NewEventOptions{Action: "tool.invoked", Harness: HarnessInfo{Name: "cursor"}})
	event.GenAI = &GenAIInfo{Usage: &GenAIUsageInfo{InputTokens: int64Ptr(1)}}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	text := string(data)
	for _, field := range []string{`"trace"`, `"cost_usd"`} {
		if strings.Contains(text, field) {
			t.Fatalf("unset additive field %s leaked into JSON: %s", field, text)
		}
	}
}

func TestRunAndOriginAreOmittedWhenUnset(t *testing.T) {
	event := NewEvent(NewEventOptions{Action: "tool.invoked", Harness: HarnessInfo{Name: "cursor"}})
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	text := string(data)
	for _, field := range []string{`"origin"`, `"run"`} {
		if strings.Contains(text, field) {
			t.Fatalf("unset additive field %s leaked into JSON: %s", field, text)
		}
	}
}

func TestRunAndOriginSerializeWhenSet(t *testing.T) {
	event := NewEvent(NewEventOptions{
		Action:  "tool.invoked",
		Harness: HarnessInfo{Name: "claude"},
		Origin:  OriginCI,
		Run:     &RunInfo{Provider: "github_actions", RunID: "123", PR: "42", Ephemeral: true},
	})
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"origin":"ci"`, `"provider":"github_actions"`, `"run_id":"123"`, `"ephemeral":true`} {
		if !strings.Contains(text, want) {
			t.Fatalf("JSON missing %s: %s", want, text)
		}
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
