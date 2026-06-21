package cowork

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations"
)

const (
	Name     = "claude_cowork"
	AdminURL = "https://claude.ai/admin-settings/cowork"

	DisplayName = "Claude Cowork"
	MinVersion  = "1.1.4173"
)

type Config struct {
	Endpoint           string `json:"endpoint"`
	Protocol           string `json:"protocol"`
	Headers            string `json:"headers,omitempty"`
	ResourceAttributes string `json:"resource_attributes,omitempty"`
}

type Status struct {
	Name                string `json:"name"`
	DisplayName         string `json:"display_name"`
	Detected            bool   `json:"detected"`
	DesktopPath         string `json:"desktop_path,omitempty"`
	MinimumVersion      string `json:"minimum_version"`
	Configuration       string `json:"configuration"`
	LastEventObserved   bool   `json:"last_event_observed"`
	LastEventObservedAt string `json:"last_event_observed_at,omitempty"`
	Message             string `json:"message"`
}

func DefaultConfig(grpcPort, httpPort int) Config {
	return Config{
		Endpoint: fmt.Sprintf("http://127.0.0.1:%d", httpPort),
		Protocol: "HTTP/protobuf",
	}
}

func PrintConfig(cfg Config) string {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultConfig(4317, 4318).Endpoint
	}
	if cfg.Protocol == "" {
		cfg.Protocol = "HTTP/protobuf"
	}
	return fmt.Sprintf(`Claude Cowork OpenTelemetry setup

Configure this in Claude Desktop:

  %s

OTLP endpoint:
  %s

OTLP protocol:
  %s

Headers:
  %s

Resource attributes:
  %s

Notes:
- Claude Cowork export is configured by a Team/Enterprise admin.
- Claude Desktop must be version %s or newer.
- The OTLP endpoint must be reachable by Claude Cowork. Use a public HTTPS collector or an authenticated tunnel for local testing.
- Cowork may include prompt text and tool parameters. Beacon applies local redaction and size limits before writing endpoint events.
`, AdminURL, cfg.Endpoint, cfg.Protocol, headerText(cfg.Headers), resourceAttributesText(cfg.ResourceAttributes), MinVersion)
}

func GetStatus(logPath string) Status {
	status := Status{
		Name:           Name,
		DisplayName:    DisplayName,
		MinimumVersion: MinVersion,
		Configuration:  "admin_configured",
		Message:        "Configure Claude Cowork in Claude Desktop organization settings",
	}
	if runtime.GOOS == "darwin" {
		for _, path := range []string{
			"/Applications/Claude.app",
			filepath.Join(os.Getenv("HOME"), "Applications", "Claude.app"),
		} {
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				status.Detected = true
				status.DesktopPath = path
				break
			}
		}
	}
	status.LastEventObserved, status.LastEventObservedAt = integrations.LastEventStatus(LastCoworkEvent(logPath))
	if status.LastEventObserved {
		status.Message = "Claude Cowork events have been observed in the endpoint runtime log"
	}
	return status
}

func HasRecentCoworkEvent(logPath string) bool {
	return integrations.HasRecentHarnessEvent(logPath, Name)
}

func HasCoworkEventSince(logPath string, since time.Time) bool {
	return integrations.HasHarnessEventSince(logPath, Name, since)
}

func LastCoworkEvent(logPath string) (time.Time, bool) {
	return integrations.LastHarnessEvent(logPath, Name)
}

func headerText(headers string) string {
	if strings.TrimSpace(headers) == "" {
		return "(none)"
	}
	return headers
}

func resourceAttributesText(attrs string) string {
	if strings.TrimSpace(attrs) == "" {
		return "(none)"
	}
	return attrs
}
