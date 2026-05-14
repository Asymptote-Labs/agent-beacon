package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type TelemetryStatus string

const (
	TelemetryEnabled       TelemetryStatus = "enabled"
	TelemetryDisabled      TelemetryStatus = "disabled"
	TelemetryMissing       TelemetryStatus = "missing"
	TelemetryMisconfigured TelemetryStatus = "misconfigured"
)

type Harness struct {
	Name            string          `json:"name"`
	DisplayName     string          `json:"display_name"`
	Detected        bool            `json:"detected"`
	Version         string          `json:"version,omitempty"`
	ExecutablePath  string          `json:"executable_path,omitempty"`
	ConfigPath      string          `json:"config_path,omitempty"`
	TelemetryStatus TelemetryStatus `json:"telemetry_status"`
	Capability      string          `json:"capability,omitempty"`
	Message         string          `json:"message,omitempty"`
}

type ConfigureOptions struct {
	Endpoint string
	UserMode bool
}

type ValidationResult struct {
	Harness string          `json:"harness"`
	Status  TelemetryStatus `json:"status"`
	Message string          `json:"message"`
}

func DiscoverAll() []Harness {
	return []Harness{
		DiscoverClaude(),
		DiscoverCodex(),
		DiscoverCursor(),
		DiscoverClaudeCowork(),
	}
}

func DiscoverClaude() Harness {
	h := Harness{Name: "claude_code", DisplayName: "Claude Code", Capability: "otel_env"}
	path, err := exec.LookPath("claude")
	if err == nil {
		h.Detected = true
		h.ExecutablePath = path
		h.Version = commandVersion(path)
	}
	home, _ := os.UserHomeDir()
	userConfig := filepath.Join(home, ".claude", "settings.json")
	managedConfig := "/Library/Application Support/ClaudeCode/managed-settings.json"
	if fileExists(managedConfig) {
		h.ConfigPath = managedConfig
		status, msg := claudeStatus(managedConfig)
		h.TelemetryStatus = status
		h.Message = msg
	} else if fileExists(userConfig) {
		h.ConfigPath = userConfig
		status, msg := claudeStatus(userConfig)
		h.TelemetryStatus = status
		h.Message = msg
	} else {
		h.ConfigPath = userConfig
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Claude settings file was not found"
	}
	if !h.Detected && dirExists(filepath.Join(home, ".claude")) {
		h.Detected = true
	}
	return h
}

func DiscoverCodex() Harness {
	h := Harness{Name: "codex_cli", DisplayName: "Codex CLI", Capability: "otel_config"}
	path, err := exec.LookPath("codex")
	if err == nil {
		h.Detected = true
		h.ExecutablePath = path
		h.Version = commandVersion(path)
	}
	home, _ := os.UserHomeDir()
	h.ConfigPath = filepath.Join(home, ".codex", "config.toml")
	if fileExists(h.ConfigPath) {
		status, msg := codexStatus(h.ConfigPath)
		h.TelemetryStatus = status
		h.Message = msg
	} else {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Codex config file was not found"
	}
	if !h.Detected && dirExists(filepath.Join(home, ".codex")) {
		h.Detected = true
	}
	return h
}

func DiscoverCursor() Harness {
	h := Harness{Name: "cursor", DisplayName: "Cursor", Capability: "hooks"}
	path, err := exec.LookPath("cursor")
	if err == nil {
		h.Detected = true
		h.ExecutablePath = path
		h.Version = commandVersion(path)
	}
	home, _ := os.UserHomeDir()
	h.ConfigPath = filepath.Join(home, ".cursor", "hooks.json")
	if !h.Detected && dirExists(filepath.Join(home, ".cursor")) {
		h.Detected = true
	}
	if fileExists(h.ConfigPath) {
		data, _ := os.ReadFile(h.ConfigPath)
		if strings.Contains(string(data), "BEACON_ENDPOINT_MODE=1") {
			h.TelemetryStatus = TelemetryEnabled
			h.Message = "Cursor endpoint hooks are configured"
		} else {
			h.TelemetryStatus = TelemetryDisabled
			h.Message = "Cursor hooks exist but endpoint hooks were not found"
		}
	} else {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Cursor hooks.json was not found"
	}
	return h
}

func DiscoverClaudeCowork() Harness {
	h := Harness{Name: "claude_cowork", DisplayName: "Claude Cowork", Capability: "admin_otel"}
	if fileExists("/Applications/Claude.app/Contents/Info.plist") {
		h.Detected = true
		h.ExecutablePath = "/Applications/Claude.app"
	}
	h.TelemetryStatus = TelemetryMissing
	h.Message = "Claude Cowork telemetry is configured in Claude organization settings"
	return h
}

func ConfigureClaude(opts ConfigureOptions) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".claude", "settings.json")
	settings := map[string]interface{}{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &settings)
		if err := backup(path, data); err != nil {
			return "", err
		}
	}
	env, _ := settings["env"].(map[string]interface{})
	if env == nil {
		env = map[string]interface{}{}
	}
	env["CLAUDE_CODE_ENABLE_TELEMETRY"] = "1"
	env["OTEL_LOGS_EXPORTER"] = "otlp"
	env["OTEL_METRICS_EXPORTER"] = "otlp"
	env["OTEL_EXPORTER_OTLP_PROTOCOL"] = "grpc"
	env["OTEL_EXPORTER_OTLP_ENDPOINT"] = opts.Endpoint
	settings["env"] = env
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0600)
}

func ConfigureCodex(opts ConfigureOptions) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".codex", "config.toml")
	var existing string
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
		if err := backup(path, data); err != nil {
			return "", err
		}
	}
	updated := mergeCodexOTEL(existing, opts.Endpoint)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(updated), 0600)
}

func ValidateConfigured(endpoint string) []ValidationResult {
	claude := DiscoverClaude()
	codex := DiscoverCodex()
	return []ValidationResult{
		{
			Harness: claude.Name,
			Status:  claude.TelemetryStatus,
			Message: validateEndpointMessage(claude.TelemetryStatus, claude.Message, endpoint),
		},
		{
			Harness: codex.Name,
			Status:  codex.TelemetryStatus,
			Message: validateEndpointMessage(codex.TelemetryStatus, codex.Message, endpoint),
		},
	}
}

func mergeCodexOTEL(existing, endpoint string) string {
	lines := strings.Split(existing, "\n")
	var out []string
	inOTEL := false
	wroteOTEL := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			isOTELHeader := codexOTELHeader(trim)
			if inOTEL && !isOTELHeader && !wroteOTEL {
				out = append(out, codexOTELBlock(endpoint)...)
				wroteOTEL = true
			}
			inOTEL = isOTELHeader
			if !inOTEL {
				out = append(out, line)
			}
			continue
		}
		if inOTEL {
			continue
		}
		out = append(out, line)
	}
	if inOTEL && !wroteOTEL {
		out = append(out, codexOTELBlock(endpoint)...)
		wroteOTEL = true
	}
	if !wroteOTEL {
		if strings.TrimSpace(existing) != "" {
			out = append(out, "")
		}
		out = append(out, codexOTELBlock(endpoint)...)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}

func codexOTELHeader(header string) bool {
	return header == "[otel]" || strings.HasPrefix(header, "[otel.")
}

func codexOTELBlock(endpoint string) []string {
	return []string{
		"[otel]",
		"environment = \"dev\"",
		"log_user_prompt = false",
		"",
		"[otel.exporter.\"otlp-grpc\"]",
		fmt.Sprintf("endpoint = %q", endpoint),
		"",
		"[otel.trace_exporter.\"otlp-grpc\"]",
		fmt.Sprintf("endpoint = %q", endpoint),
		"",
		"[otel.metrics_exporter.\"otlp-grpc\"]",
		fmt.Sprintf("endpoint = %q", endpoint),
	}
}

func claudeStatus(path string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TelemetryMissing, err.Error()
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return TelemetryMisconfigured, "Claude settings JSON is invalid"
	}
	env, _ := settings["env"].(map[string]interface{})
	if env == nil {
		return TelemetryDisabled, "Claude telemetry env is missing"
	}
	if fmt.Sprint(env["CLAUDE_CODE_ENABLE_TELEMETRY"]) != "1" {
		return TelemetryDisabled, "CLAUDE_CODE_ENABLE_TELEMETRY is not enabled"
	}
	if !strings.Contains(fmt.Sprint(env["OTEL_EXPORTER_OTLP_ENDPOINT"]), "127.0.0.1") &&
		!strings.Contains(fmt.Sprint(env["OTEL_EXPORTER_OTLP_ENDPOINT"]), "localhost") {
		return TelemetryMisconfigured, "OTLP endpoint does not point to localhost"
	}
	return TelemetryEnabled, "Claude telemetry is configured for local OTLP"
}

func codexStatus(path string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TelemetryMissing, err.Error()
	}
	text := string(data)
	if !strings.Contains(text, "[otel]") {
		return TelemetryDisabled, "Codex [otel] config is missing"
	}
	if !strings.Contains(text, "otlp-grpc") && !strings.Contains(text, "otlp-http") {
		return TelemetryDisabled, "Codex OTEL exporter is not configured"
	}
	if !strings.Contains(text, "127.0.0.1") && !strings.Contains(text, "localhost") {
		return TelemetryMisconfigured, "Codex OTLP endpoint does not point to localhost"
	}
	return TelemetryEnabled, "Codex telemetry is configured for local OTLP"
}

func commandVersion(path string) string {
	cmd := exec.Command(path, "--version")
	timer := time.AfterFunc(2*time.Second, func() { _ = cmd.Process.Kill() })
	out, err := cmd.CombinedOutput()
	timer.Stop()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func backup(path string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	backupPath := fmt.Sprintf("%s.beacon.%s.bak", path, time.Now().UTC().Format("20060102T150405Z"))
	return os.WriteFile(backupPath, data, 0600)
}

func validateEndpointMessage(status TelemetryStatus, msg, endpoint string) string {
	if status != TelemetryEnabled {
		return msg
	}
	if endpoint != "" && !strings.Contains(msg, "local OTLP") {
		return "telemetry enabled but endpoint could not be fully validated"
	}
	return msg
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
