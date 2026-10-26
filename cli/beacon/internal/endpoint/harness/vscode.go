package harness

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/jsonc"
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
	detectExecutable(&h, "code")
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
	// settings.json is JSONC that people edit by hand. Each key is set in place so the rest of the
	// file, comments included, stays exactly as it was.
	text := ""
	if data, err := os.ReadFile(path); err == nil {
		text = string(data)
		if _, err := jsonc.Decode(text); err != nil {
			return "", fmt.Errorf("VS Code settings JSON is invalid: %w", err)
		}
		if err := backup(path, data); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	beaconSettings := jsonc.Ordered{
		{Key: "github.copilot.chat.otel.enabled", Value: true},
		{Key: "github.copilot.chat.otel.exporterType", Value: "otlp-http"},
		{Key: "github.copilot.chat.otel.otlpEndpoint", Value: opts.Endpoint},
		{Key: "github.copilot.chat.otel.captureContent", Value: opts.CaptureContent},
		{Key: "github.copilot.chat.otel.maxAttributeSizeChars", Value: 8192},
	}
	for _, f := range beaconSettings {
		if text, err = jsonc.SetMember(text, nil, f.Key, f.Value); err != nil {
			return "", fmt.Errorf("cannot update VS Code settings %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(text), 0600)
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
	settings, err := jsonc.Decode(string(data))
	if err != nil {
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
