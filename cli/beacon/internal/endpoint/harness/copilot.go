package harness

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func DiscoverCopilotCLI() Harness {
	return discoverCopilotCLI("")
}

func discoverCopilotCLI(expectedEndpoint string) Harness {
	h := Harness{Name: "copilot_cli", DisplayName: "GitHub Copilot CLI", Capability: "otel_env"}
	detectExecutable(&h, "copilot")
	home, _ := os.UserHomeDir()
	if !h.Detected && fileExists(filepath.Join(home, ".copilot", "config.json")) {
		h.Detected = true
	}
	h.ConfigPath = factoryProfilePath(home)
	if fileExists(h.ConfigPath) {
		status, msg := copilotStatus(h.ConfigPath, expectedEndpoint)
		h.TelemetryStatus = status
		h.Message = msg
	} else {
		status, msg := copilotStatusFromEnv(copilotEnvFromProcess(), expectedEndpoint)
		h.TelemetryStatus = status
		h.Message = msg
		if status == TelemetryDisabled {
			h.TelemetryStatus = TelemetryMissing
			h.Message = "GitHub Copilot CLI telemetry is MDM-managed; set COPILOT_OTEL_ENABLED=true and OTEL_EXPORTER_OTLP_ENDPOINT to the local OTLP HTTP receiver"
		}
	}
	return h
}

func copilotStatus(path string, expectedEndpoint ...string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		status, msg := copilotStatusFromEnv(copilotEnvFromProcess(), firstNonEmptyString(expectedEndpoint...))
		if status != TelemetryDisabled {
			return status, msg
		}
		return TelemetryMissing, err.Error()
	}
	env := copilotEnvFromProfile(string(data))
	procEnv := copilotEnvFromProcess()
	if procEnv.enabled != "" {
		env.enabled = procEnv.enabled
	}
	if procEnv.endpoint != "" {
		env.endpoint = procEnv.endpoint
	}
	if procEnv.fileExporter != "" {
		env.fileExporter = procEnv.fileExporter
	}
	return copilotStatusFromEnv(env, firstNonEmptyString(expectedEndpoint...))
}

type copilotEnv struct {
	enabled      string
	endpoint     string
	fileExporter string
}

func (env copilotEnv) configured() bool {
	return env.enabled != "" || env.endpoint != "" || env.fileExporter != ""
}

func copilotEnvFromProcess() copilotEnv {
	return copilotEnv{
		enabled:      os.Getenv("COPILOT_OTEL_ENABLED"),
		endpoint:     firstNonEmptyString(os.Getenv("COPILOT_OTEL_ENDPOINT"), os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
		fileExporter: os.Getenv("COPILOT_OTEL_FILE_EXPORTER_PATH"),
	}
}

func copilotEnvFromProfile(text string) copilotEnv {
	return copilotEnv{
		enabled:      shellExportValue(text, "COPILOT_OTEL_ENABLED"),
		endpoint:     firstNonEmptyString(shellExportValue(text, "COPILOT_OTEL_ENDPOINT"), shellExportValue(text, "OTEL_EXPORTER_OTLP_ENDPOINT")),
		fileExporter: shellExportValue(text, "COPILOT_OTEL_FILE_EXPORTER_PATH"),
	}
}

func copilotStatusFromEnv(env copilotEnv, expectedEndpoint string) (TelemetryStatus, string) {
	if env.fileExporter != "" {
		return TelemetryMisconfigured, "GitHub Copilot CLI file exporter is configured and will bypass local OTLP"
	}
	if !truthySetting(env.enabled) {
		return TelemetryDisabled, "GitHub Copilot CLI OTel env is not configured"
	}
	endpoint := env.endpoint
	if endpoint == "" {
		endpoint = "http://localhost:4318"
	}
	if !localOTLPEndpointMatches(endpoint, expectedEndpoint) {
		return TelemetryMisconfigured, "GitHub Copilot CLI OTLP endpoint does not point to localhost"
	}
	return TelemetryEnabled, "GitHub Copilot CLI telemetry is configured for local OTLP HTTP"
}

func localOTLPEndpointMatches(endpoint, expectedEndpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "127.0.0.1" && host != "localhost" {
		return false
	}
	if expectedEndpoint == "" {
		return true
	}
	expected, err := url.Parse(expectedEndpoint)
	if err != nil || expected.Port() == "" {
		return true
	}
	return parsed.Port() == expected.Port()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
