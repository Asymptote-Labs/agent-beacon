package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func DiscoverGemini() Harness {
	h := Harness{Name: "gemini_cli", DisplayName: "Gemini CLI", Capability: "otel_config"}
	path, err := exec.LookPath("gemini")
	if err == nil {
		h.Detected = true
		h.ExecutablePath = path
		h.Version = commandVersion(path)
	}
	home, _ := os.UserHomeDir()
	h.ConfigPath = filepath.Join(home, ".gemini", "settings.json")
	if fileExists(h.ConfigPath) {
		status, msg := geminiStatus(h.ConfigPath)
		h.TelemetryStatus = status
		h.Message = msg
	} else {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Gemini settings file was not found"
	}
	return h
}

func ConfigureGemini(opts ConfigureOptions) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".gemini", "settings.json")
	settings := map[string]interface{}{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return "", fmt.Errorf("Gemini settings JSON is invalid: %w", err)
		}
		if err := backup(path, data); err != nil {
			return "", err
		}
	}

	telemetry, _ := settings["telemetry"].(map[string]interface{})
	if telemetry == nil {
		telemetry = map[string]interface{}{}
	}
	telemetry["enabled"] = true
	telemetry["target"] = "local"
	telemetry["otlpEndpoint"] = opts.Endpoint
	telemetry["otlpProtocol"] = "grpc"
	telemetry["useCollector"] = true
	telemetry["logPrompts"] = opts.ContentRetention != "metadata"
	telemetry["traces"] = opts.ContentRetention != "metadata"
	delete(telemetry, "outfile")
	settings["telemetry"] = telemetry

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0600)
}

func geminiStatus(path string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TelemetryMissing, err.Error()
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return TelemetryMisconfigured, "Gemini settings JSON is invalid"
	}
	telemetry, _ := settings["telemetry"].(map[string]interface{})
	if telemetry == nil {
		return TelemetryDisabled, "Gemini telemetry config is missing"
	}
	if !truthySetting(telemetry["enabled"]) {
		return TelemetryDisabled, "Gemini telemetry is not enabled"
	}
	if fmt.Sprint(telemetry["target"]) != "local" {
		return TelemetryMisconfigured, "Gemini telemetry target is not local"
	}
	if outfile := strings.TrimSpace(fmt.Sprint(telemetry["outfile"])); outfile != "" && outfile != "<nil>" {
		return TelemetryMisconfigured, "Gemini telemetry outfile is set and will bypass local OTLP"
	}
	endpoint := fmt.Sprint(telemetry["otlpEndpoint"])
	if !strings.Contains(endpoint, "127.0.0.1") && !strings.Contains(endpoint, "localhost") {
		return TelemetryMisconfigured, "Gemini OTLP endpoint does not point to localhost"
	}
	if fmt.Sprint(telemetry["otlpProtocol"]) != "grpc" {
		return TelemetryMisconfigured, "Gemini OTLP protocol is not grpc"
	}
	if !truthySetting(telemetry["useCollector"]) {
		return TelemetryDisabled, "Gemini telemetry is not configured to use an external collector"
	}
	return TelemetryEnabled, "Gemini telemetry is configured for local OTLP; project settings and environment variables may override this user config"
}

func truthySetting(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		normalized := strings.ToLower(strings.TrimSpace(typed))
		return normalized == "true" || normalized == "1"
	case float64:
		return typed == 1
	default:
		return false
	}
}
