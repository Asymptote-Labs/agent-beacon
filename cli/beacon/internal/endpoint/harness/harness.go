package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
		DiscoverGemini(),
		DiscoverAntigravity(),
		DiscoverCopilotCLI(),
		DiscoverOpenCode(),
		DiscoverHermes(),
		DiscoverFactory(),
		DiscoverVSCode(),
		DiscoverCursor(),
		DiscoverDevin(),
		DiscoverDevinDesktop(),
		DiscoverClaudeCowork(),
	}
}

func DiscoverAntigravity() Harness {
	h := Harness{Name: "antigravity_cli", DisplayName: "Antigravity CLI", Capability: "hooks"}
	detectExecutable(&h, "antigravity", "antigravity-cli")
	home, _ := os.UserHomeDir()
	userConfig := filepath.Join(home, ".gemini", "config", "hooks.json")
	projectConfig := filepath.Join(".agents", "hooks.json")
	h.ConfigPath = userConfig
	if fileExists(projectConfig) {
		h.ConfigPath = projectConfig
	}
	if !h.Detected && (dirExists(filepath.Join(home, ".gemini", "config")) || dirExists(".agents")) {
		h.Detected = true
	}
	if fileExists(h.ConfigPath) {
		status, msg := antigravityStatus(h.ConfigPath)
		h.TelemetryStatus = status
		h.Message = msg
	} else {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Antigravity hooks.json was not found"
	}
	return h
}

func DiscoverClaude() Harness {
	h := Harness{Name: "claude_code", DisplayName: "Claude Code", Capability: "otel_env"}
	detectExecutable(&h, "claude")
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
	detectExecutable(&h, "codex")
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
	return h
}

func DiscoverFactory() Harness {
	h := Harness{Name: "factory", DisplayName: "Factory Droid", Capability: "otel_env"}
	detectExecutable(&h, "droid")
	home, _ := os.UserHomeDir()
	h.ConfigPath = factoryProfilePath(home)
	if fileExists(h.ConfigPath) {
		status, msg := factoryStatus(h.ConfigPath)
		h.TelemetryStatus = status
		h.Message = msg
	} else {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Factory Droid telemetry is configured by the launch environment; set OTEL_TELEMETRY_ENDPOINT to the local OTLP HTTP receiver"
	}
	return h
}

func DiscoverOpenCode() Harness {
	h := Harness{Name: "opencode", DisplayName: "opencode", Capability: "plugin"}
	detectExecutable(&h, "opencode")
	home, _ := os.UserHomeDir()
	pluginPath := filepath.Join(home, ".config", "opencode", "plugins", "beacon.ts")
	h.ConfigPath = pluginPath
	if !h.Detected && dirExists(filepath.Join(home, ".config", "opencode")) {
		h.Detected = true
	}
	if fileExists(pluginPath) {
		data, _ := os.ReadFile(pluginPath)
		if strings.Contains(string(data), "beacon-managed-opencode-plugin:v1") {
			h.TelemetryStatus = TelemetryEnabled
			h.Message = "Beacon opencode plugin is configured"
		} else {
			h.TelemetryStatus = TelemetryDisabled
			h.Message = "opencode plugin file exists but Beacon endpoint plugin was not found"
		}
	} else {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Beacon opencode plugin was not found"
	}
	return h
}

func DiscoverHermes() Harness {
	h := Harness{Name: "hermes", DisplayName: "Hermes Agent", Capability: "hooks"}
	detectExecutable(&h, "hermes")
	home, _ := os.UserHomeDir()
	h.ConfigPath = filepath.Join(home, ".hermes", "config.yaml")
	if !h.Detected && dirExists(filepath.Join(home, ".hermes")) {
		h.Detected = true
	}
	if fileExists(h.ConfigPath) {
		status, msg := hermesStatus(h.ConfigPath)
		h.TelemetryStatus = status
		h.Message = msg
	} else {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Hermes config.yaml was not found"
	}
	return h
}

func DiscoverCursor() Harness {
	h := Harness{Name: "cursor", DisplayName: "Cursor", Capability: "hooks"}
	detectExecutable(&h, "cursor")
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

func DiscoverDevin() Harness {
	h := Harness{Name: "devin-cli", DisplayName: "Devin CLI", Capability: "hooks"}
	detectExecutable(&h, "devin")
	home, _ := os.UserHomeDir()
	userConfig := filepath.Join(home, ".config", "devin", "config.json")
	projectConfig := filepath.Join(".devin", "hooks.v1.json")
	h.ConfigPath = userConfig
	if fileExists(projectConfig) {
		h.ConfigPath = projectConfig
	}
	if !h.Detected && (dirExists(filepath.Join(home, ".config", "devin")) || dirExists(".devin")) {
		h.Detected = true
	}
	if fileExists(h.ConfigPath) {
		status, msg := devinStatus(h.ConfigPath, "devin", "devin-cli")
		h.TelemetryStatus = status
		h.Message = msg
	} else {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Devin CLI endpoint hooks were not found"
	}
	return h
}

func DiscoverDevinDesktop() Harness {
	h := Harness{Name: "devin-desktop", DisplayName: "Devin Desktop", Capability: "hooks"}
	home, _ := os.UserHomeDir()
	appSupport := filepath.Join(home, "Library", "Application Support", "Devin")
	appPath := "/Applications/Devin.app"
	if dirExists(appSupport) {
		h.Detected = true
		h.ExecutablePath = appSupport
	}
	if fileExists(filepath.Join(appPath, "Contents", "Info.plist")) {
		h.Detected = true
		h.ExecutablePath = appPath
	}
	userConfig := filepath.Join(home, ".codeium", "windsurf", "hooks.json")
	projectConfig := filepath.Join(".windsurf", "hooks.json")
	h.ConfigPath = userConfig
	if fileExists(projectConfig) {
		h.ConfigPath = projectConfig
	}
	if !h.Detected && (dirExists(filepath.Join(home, ".codeium", "windsurf")) || dirExists(".windsurf")) {
		h.Detected = true
	}
	if fileExists(h.ConfigPath) {
		status, msg := windsurfCascadeStatus(h.ConfigPath, "devin-desktop")
		h.TelemetryStatus = status
		if status == TelemetryEnabled {
			h.Message = msg + "; generate a Devin Desktop event and check the Beacon runtime log to validate hook execution"
		} else {
			h.Message = msg
		}
	} else {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Devin Desktop Cascade/Windsurf hooks.json was not found"
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
	// Delta temporality keeps token usage counters per-interval so downstream
	// aggregation can sum datapoints without deduping cumulative series.
	env["OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE"] = "delta"
	env["OTEL_EXPORTER_OTLP_PROTOCOL"] = "grpc"
	env["OTEL_EXPORTER_OTLP_ENDPOINT"] = opts.Endpoint
	// Tool details expose the command, file path, URL, search pattern, and MCP
	// identity/arguments needed to reconstruct agent activity from OTel events.
	env["OTEL_LOG_TOOL_DETAILS"] = "1"
	env["OTEL_LOG_USER_PROMPTS"] = "1"
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
	updated := mergeCodexOTELWithPrompt(existing, opts.Endpoint, true)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(updated), 0600)
}

func ValidateConfigured(endpoint string) []ValidationResult {
	claude := DiscoverClaude()
	codex := DiscoverCodex()
	gemini := DiscoverGemini()
	copilot := discoverCopilotCLI(endpoint)
	factory := DiscoverFactory()
	vscode := discoverVSCode(endpoint)
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
		{
			Harness: gemini.Name,
			Status:  gemini.TelemetryStatus,
			Message: validateEndpointMessage(gemini.TelemetryStatus, gemini.Message, endpoint),
		},
		{
			Harness: copilot.Name,
			Status:  copilot.TelemetryStatus,
			Message: validateEndpointMessage(copilot.TelemetryStatus, copilot.Message, endpoint),
		},
		{
			Harness: factory.Name,
			Status:  factory.TelemetryStatus,
			Message: validateEndpointMessage(factory.TelemetryStatus, factory.Message, endpoint),
		},
		{
			Harness: vscode.Name,
			Status:  vscode.TelemetryStatus,
			Message: validateEndpointMessage(vscode.TelemetryStatus, vscode.Message, endpoint),
		},
	}
}

func mergeCodexOTEL(existing, endpoint string) string {
	return mergeCodexOTELWithPrompt(existing, endpoint, true)
}

func mergeCodexOTELWithPrompt(existing, endpoint string, logUserPrompt bool) string {
	lines := strings.Split(existing, "\n")
	var out []string
	inOTEL := false
	wroteOTEL := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			isOTELHeader := codexOTELHeader(trim)
			if inOTEL && !isOTELHeader && !wroteOTEL {
				out = append(out, codexOTELBlock(endpoint, logUserPrompt)...)
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
		out = append(out, codexOTELBlock(endpoint, logUserPrompt)...)
		wroteOTEL = true
	}
	if !wroteOTEL {
		if strings.TrimSpace(existing) != "" {
			out = append(out, "")
		}
		out = append(out, codexOTELBlock(endpoint, logUserPrompt)...)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}

func codexOTELHeader(header string) bool {
	return header == "[otel]" || strings.HasPrefix(header, "[otel.")
}

func codexOTELBlock(endpoint string, logUserPrompt bool) []string {
	return []string{
		"[otel]",
		"environment = \"dev\"",
		fmt.Sprintf("log_user_prompt = %t", logUserPrompt),
		"",
		"[otel.exporter.\"otlp-grpc\"]",
		fmt.Sprintf("endpoint = %q", endpoint),
		"",
		// metrics_exporter is a separate Codex setting from the log/event
		// exporter above; without it Codex exports logs but no metrics, so the
		// codex.turn.token_usage histogram never reaches the collector.
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

func factoryStatus(path string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TelemetryMissing, err.Error()
	}
	text := string(data)
	endpoint := shellExportValue(text, "OTEL_TELEMETRY_ENDPOINT")
	if endpoint == "" {
		return TelemetryDisabled, "Factory Droid OTEL_TELEMETRY_ENDPOINT is not configured"
	}
	if !strings.Contains(endpoint, "127.0.0.1") && !strings.Contains(endpoint, "localhost") {
		return TelemetryMisconfigured, "Factory Droid OTEL endpoint does not point to localhost"
	}
	return TelemetryEnabled, "Factory Droid telemetry is configured for local OTLP HTTP"
}

func devinStatus(path string, platforms ...string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TelemetryMissing, err.Error()
	}
	if hasBeaconDevinHooks, err := hasBeaconDevinHooks(data, platforms...); err != nil {
		return TelemetryMisconfigured, "Devin hooks JSON is invalid"
	} else if hasBeaconDevinHooks {
		return TelemetryEnabled, "Devin endpoint hooks are configured"
	}
	return TelemetryDisabled, "Devin hooks exist but endpoint hooks were not found"
}

func hasBeaconDevinHooks(data []byte, platforms ...string) (bool, error) {
	if len(platforms) == 0 {
		platforms = []string{"devin"}
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return false, err
	}
	rawHooks := data
	if raw, ok := root["hooks"]; ok {
		rawHooks = raw
	}
	var hooks map[string][]struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(rawHooks, &hooks); err != nil {
		return false, nil
	}
	for _, groups := range hooks {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if strings.Contains(hook.Command, "BEACON_ENDPOINT_MODE=1") {
					for _, platform := range platforms {
						if commandHasPlatform(hook.Command, platform) {
							return true, nil
						}
					}
				}
			}
		}
	}
	return false, nil
}

func windsurfCascadeStatus(path string, platform string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TelemetryMissing, err.Error()
	}
	if hasHooks, err := hasBeaconWindsurfHooks(data, platform); err != nil {
		return TelemetryMisconfigured, "Cascade/Windsurf hooks JSON is invalid"
	} else if hasHooks {
		return TelemetryEnabled, "Devin Desktop Cascade/Windsurf endpoint hooks are configured"
	}
	return TelemetryDisabled, "Cascade/Windsurf hooks exist but Devin Desktop endpoint hooks were not found"
}

func hasBeaconWindsurfHooks(data []byte, platform string) (bool, error) {
	var root struct {
		Hooks map[string][]struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return false, err
	}
	for _, hooks := range root.Hooks {
		for _, hook := range hooks {
			if strings.Contains(hook.Command, "BEACON_ENDPOINT_MODE=1") && commandHasPlatform(hook.Command, platform) {
				return true, nil
			}
		}
	}
	return false, nil
}

func hermesStatus(path string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TelemetryMissing, err.Error()
	}
	hasHooks, err := hasBeaconHermesHooks(data)
	if err != nil {
		return TelemetryMisconfigured, "Hermes config.yaml is invalid"
	}
	if hasHooks {
		return TelemetryEnabled, "Hermes Agent endpoint hooks are configured"
	}
	return TelemetryDisabled, "Hermes config exists but endpoint hooks were not found"
}

func hasBeaconHermesHooks(data []byte) (bool, error) {
	var root struct {
		Hooks map[string][]struct {
			Command string `yaml:"command"`
		} `yaml:"hooks"`
	}
	if len(data) == 0 {
		return false, nil
	}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return false, err
	}
	for _, refs := range root.Hooks {
		for _, ref := range refs {
			if strings.Contains(ref.Command, "BEACON_ENDPOINT_MODE=1") && commandHasPlatform(ref.Command, "hermes") {
				return true, nil
			}
		}
	}
	return false, nil
}

func commandHasPlatform(command, platform string) bool {
	fields := strings.Fields(command)
	for i, field := range fields {
		if field == "--platform" && i+1 < len(fields) {
			return strings.Trim(fields[i+1], `"'`) == platform
		}
		if strings.HasPrefix(field, "--platform=") {
			return strings.Trim(strings.TrimPrefix(field, "--platform="), `"'`) == platform
		}
	}
	return false
}

func antigravityStatus(path string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TelemetryMissing, err.Error()
	}
	hasHooks, err := hasBeaconAntigravityHooks(data)
	if err != nil {
		return TelemetryMisconfigured, "Antigravity hooks JSON is invalid"
	}
	if hasHooks {
		return TelemetryEnabled, "Antigravity endpoint hooks are configured"
	}
	return TelemetryDisabled, "Antigravity hooks exist but endpoint hooks were not found"
}

func hasBeaconAntigravityHooks(data []byte) (bool, error) {
	var blocks map[string]json.RawMessage
	if err := json.Unmarshal(data, &blocks); err != nil {
		return false, err
	}
	for _, raw := range blocks {
		if strings.Contains(string(raw), "BEACON_ENDPOINT_MODE=1") && strings.Contains(string(raw), "--platform antigravity") {
			return true, nil
		}
	}
	return false, nil
}

func shellExportValue(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "export ")
		if !strings.HasPrefix(line, key+"=") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, key+"="))
		return strings.Trim(value, `"'`)
	}
	return ""
}

func factoryProfilePath(home string) string {
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		return filepath.Join(home, ".zshrc")
	case "bash":
		return filepath.Join(home, ".bash_profile")
	default:
		return filepath.Join(home, ".profile")
	}
}

// detectExecutable looks up each candidate command name on PATH and, on the
// first match, marks the harness detected and records its executable path and
// version. Names are tried in order.
func detectExecutable(h *Harness, names ...string) {
	for _, name := range names {
		path, err := exec.LookPath(name)
		if err == nil {
			h.Detected = true
			h.ExecutablePath = path
			h.Version = commandVersion(path)
			return
		}
	}
}

func commandVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	out, err := cmd.CombinedOutput()
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
