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

// DesktopPathsForOS lists where Claude Desktop is installed on a given platform.
//
// Claude Cowork is configured in organization settings rather than locally, so all Beacon does here is
// report whether the app is present -- which it could only ever answer for macOS, because the only
// path it knew was /Applications/Claude.app. On Windows that meant a machine with Claude Desktop
// installed reported it as absent, and the operator had no way to tell that from a machine without it.
//
// Parameterized on goos rather than reading runtime.GOOS, following vscodeUserDataDirsForOS: it makes
// every platform's answer testable from a maintainer's Mac, so only genuinely OS-bound behavior needs
// a runner.
//
// Windows has no per-user Applications directory to check as a fallback, and Claude Desktop installs
// per-user under %LOCALAPPDATA%, so that is the whole list there. Read from the environment with a
// fallback rather than hardcoded, since a redirected profile must not be missed.
func DesktopPathsForOS(goos string) []string {
	switch goos {
	case "darwin":
		return []string{
			"/Applications/Claude.app",
			filepath.Join(os.Getenv("HOME"), "Applications", "Claude.app"),
		}
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil
			}
			local = filepath.Join(home, "AppData", "Local")
		}
		return []string{filepath.Join(local, "AnthropicClaude")}
	default:
		// Claude Desktop does not ship for Linux. Returning nothing is the honest answer, and it is
		// distinct from "looked and did not find it".
		return nil
	}
}

func GetStatus(logPath string) Status {
	status := Status{
		Name:           Name,
		DisplayName:    DisplayName,
		MinimumVersion: MinVersion,
		Configuration:  "admin_configured",
		Message:        "Configure Claude Cowork in Claude Desktop organization settings",
	}
	for _, path := range DesktopPathsForOS(runtime.GOOS) {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			status.Detected = true
			status.DesktopPath = path
			break
		}
	}
	if last, ok := LastCoworkEvent(logPath); ok {
		status.LastEventObserved = true
		if !last.IsZero() {
			status.LastEventObservedAt = last.UTC().Format(time.RFC3339)
		}
	}
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
