package cowork

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	Name        = "claude_cowork"
	DisplayName = "Claude Cowork"
	MinVersion  = "1.1.4173"
)

type Config struct {
	Endpoint string `json:"endpoint"`
	Protocol string `json:"protocol"`
	Headers  string `json:"headers,omitempty"`
}

type Status struct {
	Name              string `json:"name"`
	DisplayName       string `json:"display_name"`
	Detected          bool   `json:"detected"`
	DesktopPath       string `json:"desktop_path,omitempty"`
	MinimumVersion    string `json:"minimum_version"`
	Configuration     string `json:"configuration"`
	LastEventObserved bool   `json:"last_event_observed"`
	Message           string `json:"message"`
}

func DefaultConfig(grpcPort, httpPort int) Config {
	return Config{
		Endpoint: fmt.Sprintf("http://127.0.0.1:%d", httpPort),
		Protocol: "HTTP/protobuf",
	}
}

func PrintConfig(cfg Config) string {
	if cfg.Endpoint == "" {
		cfg = DefaultConfig(4317, 4318)
	}
	return fmt.Sprintf(`Claude Cowork OpenTelemetry setup

Configure this in Claude Desktop:

  Organization settings > Cowork > OpenTelemetry

OTLP endpoint:
  %s

OTLP protocol:
  %s

Headers:
  %s

Notes:
- Claude Cowork export is configured by a Team/Enterprise admin.
- Claude Desktop must be version %s or newer.
- Cowork may include prompt text and tool parameters. Beacon's collector should redact/drop content by default before writing Wazuh JSONL.
`, cfg.Endpoint, cfg.Protocol, headerText(cfg.Headers), MinVersion)
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
	status.LastEventObserved = HasRecentCoworkEvent(logPath)
	if status.LastEventObserved {
		status.Message = "Claude Cowork events have been observed in the endpoint runtime log"
	}
	return status
}

func HasRecentCoworkEvent(logPath string) bool {
	if logPath == "" {
		return false
	}
	file, err := os.Open(logPath)
	if err != nil {
		return false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if harness, ok := event["harness"].(map[string]interface{}); ok {
			if name, _ := harness["name"].(string); strings.EqualFold(name, Name) {
				return true
			}
		}
	}
	return false
}

func headerText(headers string) string {
	if strings.TrimSpace(headers) == "" {
		return "(none)"
	}
	return headers
}
