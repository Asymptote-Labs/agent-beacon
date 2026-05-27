package harness

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	VSCodeName        = "vscode"
	VSCodeCopilotName = "vscode_copilot"
)

type VSCodeConfigOptions struct {
	Endpoint       string
	CaptureContent bool
	WorkspacePath  string
}

func DiscoverVSCode() Harness {
	return discoverVSCode("")
}

func discoverVSCode(expectedEndpoint string) Harness {
	h := Harness{Name: VSCodeName, DisplayName: "VS Code", Capability: "otel_hooks"}
	if path, err := exec.LookPath("code"); err == nil {
		h.Detected = true
		h.ExecutablePath = path
		h.Version = commandVersion(path)
	}
	settingsPath, err := VSCodeUserSettingsPath()
	if err == nil {
		h.ConfigPath = settingsPath
	}
	if !h.Detected {
		for _, candidate := range vscodeUserDataDirs() {
			if dirExists(candidate) {
				h.Detected = true
				break
			}
		}
	}
	status, msg := VSCodeOTelStatus(h.ConfigPath, expectedEndpoint)
	h.TelemetryStatus = status
	h.Message = msg
	return h
}

func VSCodeUserSettingsPath() (string, error) {
	return vscodeUserSettingsPath(runtime.GOOS)
}

func vscodeUserSettingsPath(goos string) (string, error) {
	dirs := vscodeUserDataDirsForOS(goos)
	if len(dirs) == 0 {
		return "", fmt.Errorf("unsupported operating system %q", goos)
	}
	return filepath.Join(dirs[0], "User", "settings.json"), nil
}

func vscodeUserDataDirs() []string {
	return vscodeUserDataDirsForOS(runtime.GOOS)
}

func vscodeUserDataDirsForOS(goos string) []string {
	switch goos {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		return []string{filepath.Join(home, "Library", "Application Support", "Code")}
	case "linux":
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil
			}
			base = filepath.Join(home, ".config")
		}
		return []string{filepath.Join(base, "Code")}
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			return nil
		}
		return []string{filepath.Join(base, "Code")}
	default:
		return nil
	}
}

func VSCodeWorkspaceSettingsPath(workspacePath string) (string, error) {
	if workspacePath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		workspacePath = cwd
	}
	return filepath.Join(workspacePath, ".vscode", "settings.json"), nil
}

func ConfigureVSCode(opts VSCodeConfigOptions) (string, error) {
	if opts.Endpoint == "" {
		opts.Endpoint = "http://127.0.0.1:4318"
	}
	path, err := VSCodeUserSettingsPath()
	if opts.WorkspacePath != "" {
		path, err = VSCodeWorkspaceSettingsPath(opts.WorkspacePath)
	}
	if err != nil {
		return "", err
	}
	settings := map[string]interface{}{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return "", fmt.Errorf("VS Code settings JSON is invalid: %w", err)
		}
		if err := backup(path, data); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	settings["github.copilot.chat.otel.enabled"] = true
	settings["github.copilot.chat.otel.exporterType"] = "otlp-http"
	settings["github.copilot.chat.otel.otlpEndpoint"] = opts.Endpoint
	settings["github.copilot.chat.otel.captureContent"] = opts.CaptureContent
	settings["github.copilot.chat.otel.maxAttributeSizeChars"] = 8192
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0600)
}

func VSCodeOTelStatus(path, expectedEndpoint string) (TelemetryStatus, string) {
	if path == "" {
		return TelemetryMissing, "VS Code settings path could not be resolved"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return TelemetryMissing, "VS Code settings.json was not found"
		}
		return TelemetryMissing, err.Error()
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return TelemetryMisconfigured, "VS Code settings JSON is invalid"
	}
	if vscodeSettingString(settings, "github.copilot.chat.otel.exporterType") == "file" ||
		vscodeSettingString(settings, "github.copilot.chat.otel.outfile") != "" {
		return TelemetryMisconfigured, "VS Code Copilot OTel file exporter is configured and will bypass local OTLP"
	}
	enabled := truthySetting(vscodeSettingString(settings, "github.copilot.chat.otel.enabled"))
	endpoint := vscodeSettingString(settings, "github.copilot.chat.otel.otlpEndpoint")
	if !enabled && endpoint == "" {
		return TelemetryDisabled, "VS Code Copilot OTel is not enabled"
	}
	if endpoint == "" {
		endpoint = "http://localhost:4318"
	}
	if !localVSCodeOTLPEndpointMatches(endpoint, expectedEndpoint) {
		return TelemetryMisconfigured, "VS Code Copilot OTLP endpoint does not point to the local Beacon HTTP receiver"
	}
	return TelemetryEnabled, "VS Code Copilot OTel is configured for local OTLP HTTP"
}

func vscodeSettingString(settings map[string]interface{}, key string) string {
	value, ok := settings[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func localVSCodeOTLPEndpointMatches(endpoint, expectedEndpoint string) bool {
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
