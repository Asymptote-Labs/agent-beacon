package openclaw

import (
	"fmt"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations"
)

const (
	Name        = "openclaw_gateway"
	DisplayName = "OpenClaw Gateway"
	DocsURL     = "https://docs.openclaw.ai/gateway/opentelemetry"
)

type Config struct {
	Endpoint       string `json:"endpoint"`
	Protocol       string `json:"protocol"`
	ServiceName    string `json:"service_name"`
	SampleRate     string `json:"sample_rate,omitempty"`
	FlushInterval  string `json:"flush_interval,omitempty"`
	CaptureContent bool   `json:"capture_content,omitempty"`
}

type Status struct {
	Name                string `json:"name"`
	DisplayName         string `json:"display_name"`
	Configuration       string `json:"configuration"`
	LastEventObserved   bool   `json:"last_event_observed"`
	LastEventObservedAt string `json:"last_event_observed_at,omitempty"`
	Message             string `json:"message"`
}

func DefaultConfig(httpPort int) Config {
	return Config{
		Endpoint:    fmt.Sprintf("http://127.0.0.1:%d", httpPort),
		Protocol:    "http/protobuf",
		ServiceName: "openclaw-gateway",
	}
}

func PrintConfig(cfg Config) string {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultConfig(4318).Endpoint
	}
	if cfg.Protocol == "" {
		cfg.Protocol = "http/protobuf"
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = "openclaw-gateway"
	}
	sampleRate := strings.TrimSpace(cfg.SampleRate)
	if sampleRate == "" {
		sampleRate = "1.0"
	}
	flushInterval := strings.TrimSpace(cfg.FlushInterval)
	if flushInterval == "" {
		flushInterval = "60000"
	}

	captureContent := "false"
	if cfg.CaptureContent {
		captureContent = "true"
	}

	return fmt.Sprintf(`OpenClaw Gateway OpenTelemetry setup

Install and enable the diagnostics plugin:

  openclaw plugins install clawhub:@openclaw/diagnostics-otel
  openclaw plugins enable diagnostics-otel

Add this to your OpenClaw Gateway config:

  {
    plugins: {
      allow: ["diagnostics-otel"],
      entries: {
        "diagnostics-otel": { enabled: true },
      },
    },
    diagnostics: {
      enabled: true,
      otel: {
        enabled: true,
        endpoint: %q,
        protocol: %q,
        serviceName: %q,
        traces: true,
        metrics: true,
        logs: true,
        sampleRate: %s,
        flushIntervalMs: %s,
        captureContent: {
          enabled: %s,
          inputMessages: false,
          outputMessages: false,
          toolInputs: false,
          toolOutputs: false,
          systemPrompt: false,
        },
      },
    },
  }

Notes:
- Beacon listens for OTLP/HTTP on the local endpoint collector and writes normalized events to the runtime JSONL log.
- OpenClaw does not export raw prompt, response, tool, or system-prompt content unless captureContent is explicitly enabled.
- Validation only confirms that Beacon observed at least one OpenClaw OTLP-derived event; it does not prove every signal type is flowing.
- OpenClaw OTEL reference: %s
`, cfg.Endpoint, cfg.Protocol, cfg.ServiceName, sampleRate, flushInterval, captureContent, DocsURL)
}

func GetStatus(logPath string) Status {
	status := Status{
		Name:          Name,
		DisplayName:   DisplayName,
		Configuration: "gateway_configured",
		Message:       "Configure OpenClaw Gateway diagnostics-otel to export OTLP/HTTP to Beacon's local collector",
	}
	if last, ok := LastOpenClawEvent(logPath); ok {
		status.LastEventObserved = true
		if !last.IsZero() {
			status.LastEventObservedAt = last.UTC().Format(time.RFC3339)
		}
	}
	if status.LastEventObserved {
		status.Message = "OpenClaw OTLP-derived events have been observed in the endpoint runtime log"
	}
	return status
}

func HasRecentOpenClawEvent(logPath string) bool {
	return integrations.HasRecentHarnessEvent(logPath, Name)
}

func HasOpenClawEventSince(logPath string, since time.Time) bool {
	return integrations.HasHarnessEventSince(logPath, Name, since)
}

func LastOpenClawEvent(logPath string) (time.Time, bool) {
	return integrations.LastHarnessEvent(logPath, Name)
}
