package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/fxsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/groksession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestMergeCodexOTELReplacesExistingBlock(t *testing.T) {
	input := "[otel]\nenabled = false\nendpoint = \"https://example.com\"\n\n[otel.exporter.\"otlp-http\"]\nendpoint = \"https://old.example.com/v1/logs\"\n\n[profile]\nname = \"default\"\n"
	got := mergeCodexOTEL(input, "http://127.0.0.1:4317")
	if strings.Contains(got, "https://example.com") || strings.Contains(got, "old.example.com") || strings.Contains(got, "enabled = false") {
		t.Fatalf("old otel config was not replaced: %s", got)
	}
	if !strings.Contains(got, "[profile]\nname = \"default\"") {
		t.Fatalf("following section was not preserved: %s", got)
	}
}

// A settings.json Beacon cannot parse still holds the user's permissions and hooks; rewriting it
// from an empty map would leave Claude Code running with only Beacon's env.
func TestConfigureClaudeRefusesSettingsItCannotParse(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := `{"permissions":{"allow":["Bash(npm test)"]},"hooks":{},}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureClaude(ConfigureOptions{Endpoint: "http://127.0.0.1:4317", UserMode: true}); err == nil {
		t.Fatal("ConfigureClaude rewrote a settings.json it could not parse")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("settings.json was changed:\n%s", data)
	}
	if backups, _ := filepath.Glob(path + ".beacon.*.bak"); len(backups) != 0 {
		t.Fatalf("a refused configure left backups: %v", backups)
	}
}

func TestConfigureClaudeWritesTelemetryEnvAndBackup(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir claude config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"env":{"EXISTING":"1"}}`), 0600); err != nil {
		t.Fatalf("write existing claude config: %v", err)
	}

	written, err := ConfigureClaude(ConfigureOptions{Endpoint: "http://127.0.0.1:4317", UserMode: true})
	if err != nil {
		t.Fatalf("ConfigureClaude returned error: %v", err)
	}
	if written != path {
		t.Fatalf("ConfigureClaude path = %q, want %q", written, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read claude config: %v", err)
	}
	var settings map[string]map[string]string
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal claude config: %v", err)
	}
	env := settings["env"]
	for key, want := range map[string]string{
		"EXISTING":                     "1",
		"CLAUDE_CODE_ENABLE_TELEMETRY": "1",
		"OTEL_LOGS_EXPORTER":           "otlp",
		"OTEL_METRICS_EXPORTER":        "otlp",
		"OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE": "delta",
		"OTEL_EXPORTER_OTLP_PROTOCOL":                       "grpc",
		"OTEL_EXPORTER_OTLP_ENDPOINT":                       "http://127.0.0.1:4317",
		"OTEL_LOG_TOOL_DETAILS":                             "1",
		"OTEL_LOG_USER_PROMPTS":                             "1",
	} {
		if got := env[key]; got != want {
			t.Fatalf("env[%s] = %q, want %q; env=%#v", key, got, want, env)
		}
	}
	if got, set := env["OTEL_LOG_RAW_API_BODIES"]; set {
		t.Fatalf("OTEL_LOG_RAW_API_BODIES = %q without the model-context capture opt-in", got)
	}
	backups, err := filepath.Glob(path + ".beacon.*.bak")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected one backup, got %#v", backups)
	}
}

// Model-context capture is opt-in. The opt-in sets inline API bodies only where nothing is set, and
// install without it removes an inline setting, so a repair without the flag turns capture off.
// Neither overrides 0 or file:<dir>.
func TestConfigureClaudeRawBodiesFollowTheWebFetchOptIn(t *testing.T) {
	const unset = "<unset>"
	tests := []struct {
		capture      bool
		before, want string
	}{
		{capture: true, before: unset, want: "1"},
		{capture: true, before: "1", want: "1"},
		{capture: true, before: "0", want: "0"},
		{capture: true, before: "file:/private/claude-bodies", want: "file:/private/claude-bodies"},
		{capture: false, before: unset, want: unset},
		{capture: false, before: "1", want: unset},
		{capture: false, before: "true", want: unset},
		{capture: false, before: "0", want: "0"},
		{capture: false, before: "file:/private/claude-bodies", want: "file:/private/claude-bodies"},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("capture=%t/%s", tc.capture, tc.before), func(t *testing.T) {
			home := t.TempDir()
			testenv.SetHome(t, home)
			path := filepath.Join(home, ".claude", "settings.json")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{}
			if tc.before != unset {
				env["OTEL_LOG_RAW_API_BODIES"] = tc.before
			}
			before, err := json.Marshal(map[string]interface{}{"env": env})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := ConfigureClaude(ConfigureOptions{Endpoint: "http://127.0.0.1:4317", CaptureModelContext: tc.capture}); err != nil {
					t.Fatal(err)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var settings map[string]map[string]string
			if err := json.Unmarshal(data, &settings); err != nil {
				t.Fatal(err)
			}
			got, set := settings["env"]["OTEL_LOG_RAW_API_BODIES"]
			if !set {
				got = unset
			}
			if got != tc.want {
				t.Fatalf("OTEL_LOG_RAW_API_BODIES after install and reinstall = %q, want %q", got, tc.want)
			}
		})
	}
}

// MCP tools come last in a request body, so the opt-in raises Claude Code's inline content limit
// for inline bodies, never over a limit the user set, and turning it off removes the limit only
// while it is still Beacon's value.
func TestConfigureClaudeContentLimitFollowsTheModelContextOptIn(t *testing.T) {
	const unset = "<unset>"
	tests := []struct {
		steps               []bool
		bodies, limit, want string
	}{
		{steps: []bool{true}, bodies: unset, limit: unset, want: "262144"},
		{steps: []bool{true}, bodies: unset, limit: "81920", want: "81920"},
		{steps: []bool{true}, bodies: "0", limit: unset, want: unset},
		{steps: []bool{true}, bodies: "file:/private/claude-bodies", limit: unset, want: unset},
		{steps: []bool{true}, bodies: "1", limit: unset, want: "262144"},
		{steps: []bool{true, false}, bodies: unset, limit: unset, want: unset},
		{steps: []bool{true, false}, bodies: "1", limit: unset, want: unset},
		{steps: []bool{false}, bodies: "1", limit: "262144", want: unset},
		{steps: []bool{false}, bodies: unset, limit: "81920", want: "81920"},
		{steps: []bool{true, false}, bodies: unset, limit: "81920", want: "81920"},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%v/bodies=%s/limit=%s", tc.steps, tc.bodies, tc.limit), func(t *testing.T) {
			home := t.TempDir()
			testenv.SetHome(t, home)
			path := filepath.Join(home, ".claude", "settings.json")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{}
			for key, value := range map[string]string{"OTEL_LOG_RAW_API_BODIES": tc.bodies, "CLAUDE_CODE_OTEL_CONTENT_MAX_LENGTH": tc.limit} {
				if value != unset {
					env[key] = value
				}
			}
			before, err := json.Marshal(map[string]interface{}{"env": env})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			for _, capture := range tc.steps {
				if _, err := ConfigureClaude(ConfigureOptions{Endpoint: "http://127.0.0.1:4317", CaptureModelContext: capture}); err != nil {
					t.Fatal(err)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var settings map[string]map[string]string
			if err := json.Unmarshal(data, &settings); err != nil {
				t.Fatal(err)
			}
			got, set := settings["env"]["CLAUDE_CODE_OTEL_CONTENT_MAX_LENGTH"]
			if !set {
				got = unset
			}
			if got != tc.want {
				t.Fatalf("CLAUDE_CODE_OTEL_CONTENT_MAX_LENGTH = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClaudeStatusVariants(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name   string
		body   string
		status TelemetryStatus
	}{
		{name: "invalid json", body: `{`, status: TelemetryMisconfigured},
		{name: "missing env", body: `{}`, status: TelemetryDisabled},
		{name: "disabled", body: `{"env":{"CLAUDE_CODE_ENABLE_TELEMETRY":"0"}}`, status: TelemetryDisabled},
		{name: "remote endpoint", body: `{"env":{"CLAUDE_CODE_ENABLE_TELEMETRY":"1","OTEL_EXPORTER_OTLP_ENDPOINT":"https://example.com"}}`, status: TelemetryMisconfigured},
		{name: "enabled", body: `{"env":{"CLAUDE_CODE_ENABLE_TELEMETRY":"1","OTEL_EXPORTER_OTLP_ENDPOINT":"http://127.0.0.1:4317"}}`, status: TelemetryEnabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".json")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			status, _ := claudeStatus(path)
			if status != tt.status {
				t.Fatalf("claudeStatus = %q, want %q", status, tt.status)
			}
		})
	}
}

func TestConfigureCodexWritesTelemetryBlockAndBackup(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir codex config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("model = \"gpt-5\"\n"), 0600); err != nil {
		t.Fatalf("write existing codex config: %v", err)
	}

	written, err := ConfigureCodex(ConfigureOptions{Endpoint: "http://127.0.0.1:4317", UserMode: true})
	if err != nil {
		t.Fatalf("ConfigureCodex returned error: %v", err)
	}
	if written != path {
		t.Fatalf("ConfigureCodex path = %q, want %q", written, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"model = \"gpt-5\"",
		"[otel]",
		"environment = \"dev\"",
		"log_user_prompt = true",
		"[otel.exporter.\"otlp-grpc\"]",
		"endpoint = \"http://127.0.0.1:4317\"",
		// Turn traces carry token counts together with thread, turn, and model
		// identity, unlike the aggregate metric.
		"[otel.trace_exporter.\"otlp-grpc\"]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("codex config missing %q:\n%s", want, text)
		}
	}
	// Metrics stay disabled so one turn is not counted through both the
	// attributable trace and the anonymous histogram.
	for _, duplicate := range []string{
		"[otel.metrics_exporter.\"otlp-grpc\"]",
	} {
		if strings.Contains(text, duplicate) {
			t.Fatalf("codex config should not enable duplicate usage exporter %q:\n%s", duplicate, text)
		}
	}
	backups, err := filepath.Glob(path + ".beacon.*.bak")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected one backup, got %#v", backups)
	}
}

// Codex reads config.toml from CODEX_HOME when it is set, so an install that wrote ~/.codex would
// leave Codex without the exporter and discovery reporting a file Codex never reads.
func TestCodexConfigFollowsCodexHome(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	resolved, err := filepath.EvalSymlinks(codexHome)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(resolved, "config.toml")

	written, err := ConfigureCodex(ConfigureOptions{Endpoint: "http://127.0.0.1:4317", UserMode: true})
	if err != nil {
		t.Fatalf("ConfigureCodex returned error: %v", err)
	}
	if written != want {
		t.Fatalf("ConfigureCodex path = %q, want %q", written, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Fatalf("ConfigureCodex touched ~/.codex with CODEX_HOME set: %v", err)
	}
	h := DiscoverCodex()
	if h.ConfigPath != want || h.TelemetryStatus != TelemetryEnabled {
		t.Fatalf("DiscoverCodex = path %q status %q, want %q enabled", h.ConfigPath, h.TelemetryStatus, want)
	}
}

// Codex refuses to start when CODEX_HOME is not an existing directory, so Beacon must not quietly
// configure ~/.codex instead, or create the directory and hide the mistake.
func TestCodexConfigRefusesMissingCodexHome(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	missing := filepath.Join(home, "missing")
	t.Setenv("CODEX_HOME", missing)

	if _, err := ConfigureCodex(ConfigureOptions{Endpoint: "http://127.0.0.1:4317", UserMode: true}); err == nil {
		t.Fatal("ConfigureCodex accepted a CODEX_HOME that does not exist")
	}
	for _, dir := range []string{missing, filepath.Join(home, ".codex")} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("ConfigureCodex created %s: %v", dir, err)
		}
	}
	h := DiscoverCodex()
	if h.TelemetryStatus != TelemetryMissing || !strings.Contains(h.Message, "CODEX_HOME") {
		t.Fatalf("DiscoverCodex = status %q message %q, want missing naming CODEX_HOME", h.TelemetryStatus, h.Message)
	}
}

func TestDiscoverCodexDoesNotDetectConfigDirectoryOnly(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0755); err != nil {
		t.Fatalf("mkdir codex config dir: %v", err)
	}

	h := DiscoverCodex()
	if h.Detected {
		t.Fatalf("DiscoverCodex detected Codex from config directory only: %#v", h)
	}
	if h.ConfigPath != filepath.Join(home, ".codex", "config.toml") {
		t.Fatalf("ConfigPath = %q, want home config path", h.ConfigPath)
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("TelemetryStatus = %q, want %q", h.TelemetryStatus, TelemetryMissing)
	}
}

func TestDiscoverCodexDetectsExecutableOnPath(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	home := t.TempDir()
	testenv.SetHome(t, home)
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	codexPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(codexPath, []byte("#!/bin/sh\necho codex 1.2.3\n"), 0755); err != nil {
		t.Fatalf("write fake codex executable: %v", err)
	}

	h := DiscoverCodex()
	if !h.Detected {
		t.Fatalf("DiscoverCodex did not detect executable on PATH: %#v", h)
	}
	if h.ExecutablePath != codexPath {
		t.Fatalf("ExecutablePath = %q, want %q", h.ExecutablePath, codexPath)
	}
	if h.Version != "codex 1.2.3" {
		t.Fatalf("Version = %q, want fake executable version", h.Version)
	}
}

func TestHermesStatusReportsInvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("hooks:\n  pre_tool_call: ["), 0600); err != nil {
		t.Fatal(err)
	}

	status, _ := hermesStatus(path)
	if status != TelemetryMisconfigured {
		t.Fatalf("hermesStatus = %q, want misconfigured", status)
	}
}

func TestCodexStatusVariants(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name   string
		body   string
		status TelemetryStatus
	}{
		{name: "missing block", body: `model = "gpt-5"`, status: TelemetryDisabled},
		{name: "old shape", body: "[otel]\nenabled = true\nendpoint = \"http://127.0.0.1:4317\"\n", status: TelemetryDisabled},
		{name: "exporter none", body: "[otel]\nexporter = \"none\"\n", status: TelemetryDisabled},
		{name: "remote endpoint", body: "[otel]\nexporter = { otlp-grpc = { endpoint = \"https://example.com:4317\" } }\n", status: TelemetryMisconfigured},
		{name: "grpc enabled", body: "[otel]\nexporter = { otlp-grpc = { endpoint = \"http://localhost:4317\" } }\n", status: TelemetryEnabled},
		{name: "grpc table enabled", body: "[otel]\nlog_user_prompt = false\n\n[otel.exporter.\"otlp-grpc\"]\nendpoint = \"http://localhost:4317\"\n", status: TelemetryEnabled},
		{name: "http enabled", body: "[otel]\nexporter = { otlp-http = { endpoint = \"http://127.0.0.1:4318/v1/logs\" } }\n", status: TelemetryEnabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".toml")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			status, _ := codexStatus(path)
			if status != tt.status {
				t.Fatalf("codexStatus = %q, want %q", status, tt.status)
			}
		})
	}
}

func TestConfigureGeminiWritesTelemetryAndBackup(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	path := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir gemini config dir: %v", err)
	}
	existing := `{"general":{"vimMode":true},"telemetry":{"enabled":false,"outfile":".gemini/telemetry.log"}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatalf("write existing gemini config: %v", err)
	}

	written, err := ConfigureGemini(ConfigureOptions{Endpoint: "http://127.0.0.1:4317", UserMode: true})
	if err != nil {
		t.Fatalf("ConfigureGemini returned error: %v", err)
	}
	if written != path {
		t.Fatalf("ConfigureGemini path = %q, want %q", written, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read gemini config: %v", err)
	}
	var settings map[string]map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal gemini config: %v", err)
	}
	if settings["general"]["vimMode"] != true {
		t.Fatalf("unrelated settings were not preserved: %#v", settings)
	}
	telemetry := settings["telemetry"]
	for key, want := range map[string]interface{}{
		"enabled":      true,
		"target":       "local",
		"otlpEndpoint": "http://127.0.0.1:4317",
		"otlpProtocol": "grpc",
		"useCollector": true,
		"logPrompts":   true,
		"traces":       true,
	} {
		if got := telemetry[key]; got != want {
			t.Fatalf("telemetry[%s] = %#v, want %#v; telemetry=%#v", key, got, want, telemetry)
		}
	}
	if _, ok := telemetry["outfile"]; ok {
		t.Fatalf("outfile should be removed so OTLP is used: %#v", telemetry)
	}
	backups, err := filepath.Glob(path + ".beacon.*.bak")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected one backup, got %#v", backups)
	}
}

func TestGeminiStatusVariants(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name   string
		body   string
		status TelemetryStatus
	}{
		{name: "invalid json", body: `{`, status: TelemetryMisconfigured},
		{name: "missing telemetry", body: `{}`, status: TelemetryDisabled},
		{name: "disabled", body: `{"telemetry":{"enabled":false}}`, status: TelemetryDisabled},
		{name: "remote target", body: `{"telemetry":{"enabled":true,"target":"gcp","otlpEndpoint":"http://127.0.0.1:4317","otlpProtocol":"grpc","useCollector":true}}`, status: TelemetryMisconfigured},
		{name: "outfile", body: `{"telemetry":{"enabled":true,"target":"local","outfile":".gemini/telemetry.log","otlpEndpoint":"http://127.0.0.1:4317","otlpProtocol":"grpc","useCollector":true}}`, status: TelemetryMisconfigured},
		{name: "remote endpoint", body: `{"telemetry":{"enabled":true,"target":"local","otlpEndpoint":"https://example.com:4317","otlpProtocol":"grpc","useCollector":true}}`, status: TelemetryMisconfigured},
		{name: "http protocol", body: `{"telemetry":{"enabled":true,"target":"local","otlpEndpoint":"http://127.0.0.1:4317","otlpProtocol":"http","useCollector":true}}`, status: TelemetryMisconfigured},
		{name: "collector disabled", body: `{"telemetry":{"enabled":true,"target":"local","otlpEndpoint":"http://127.0.0.1:4317","otlpProtocol":"grpc","useCollector":false}}`, status: TelemetryDisabled},
		{name: "enabled", body: `{"telemetry":{"enabled":true,"target":"local","otlpEndpoint":"http://localhost:4317","otlpProtocol":"grpc","useCollector":true}}`, status: TelemetryEnabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".json")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			status, _ := geminiStatus(path)
			if status != tt.status {
				t.Fatalf("geminiStatus = %q, want %q", status, tt.status)
			}
		})
	}
}

// Factory Droid reads its OTel endpoint from the login shell's environment, so discovery reports the
// profile the user's shell actually sources.
func TestDiscoverFactoryReportsTheLoginShellProfile(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("PATH", t.TempDir())

	if h := DiscoverFactory(); h.ConfigPath != filepath.Join(home, ".bash_profile") {
		t.Fatalf("ConfigPath = %q, want bash profile", h.ConfigPath)
	}
}

func TestDiscoverCopilotCLIDetectsConfigFile(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("COPILOT_HOME", "")
	configPath := filepath.Join(home, ".copilot", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir copilot config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{}`), 0600); err != nil {
		t.Fatalf("write copilot config: %v", err)
	}

	h := DiscoverCopilotCLI()
	if !h.Detected {
		t.Fatalf("DiscoverCopilotCLI did not detect config file: %#v", h)
	}
}

// With COPILOT_HOME set, the config file that marks Copilot CLI as present is the one under it; a
// stale ~/.copilot/config.json does not count.
func TestDiscoverCopilotCLIDetectsConfigFileUnderCopilotHome(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	copilotHome := t.TempDir()
	t.Setenv("COPILOT_HOME", copilotHome)
	if err := os.WriteFile(filepath.Join(copilotHome, "config.json"), []byte(`{}`), 0600); err != nil {
		t.Fatalf("write copilot config: %v", err)
	}
	if h := DiscoverCopilotCLI(); !h.Detected {
		t.Fatalf("DiscoverCopilotCLI did not detect $COPILOT_HOME/config.json: %#v", h)
	}

	t.Setenv("COPILOT_HOME", filepath.Join(copilotHome, "empty"))
	if err := os.MkdirAll(filepath.Join(home, ".copilot"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".copilot", "config.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if h := DiscoverCopilotCLI(); h.Detected {
		t.Fatalf("DiscoverCopilotCLI detected ~/.copilot/config.json with COPILOT_HOME pointing elsewhere: %#v", h)
	}
}

func TestDiscoverDevinDesktopDetectsAppSupport(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	appSupport := filepath.Join(home, "Library", "Application Support", "Devin")
	if err := os.MkdirAll(appSupport, 0755); err != nil {
		t.Fatalf("mkdir app support: %v", err)
	}

	h := DiscoverDevinDesktop()
	if !h.Detected {
		t.Fatalf("DiscoverDevinDesktop did not detect app support: %#v", h)
	}
	if h.Name != "devin-desktop" || h.DisplayName != "Devin Desktop" {
		t.Fatalf("Desktop harness identity = %s/%s, want devin-desktop/Devin Desktop", h.Name, h.DisplayName)
	}
	if h.ExecutablePath != appSupport && h.ExecutablePath != "/Applications/Devin.app" {
		t.Fatalf("ExecutablePath = %q, want app support or app path", h.ExecutablePath)
	}
	if h.ConfigPath != filepath.Join(home, ".codeium", "windsurf", "hooks.json") {
		t.Fatalf("ConfigPath = %q, want Windsurf user hooks path", h.ConfigPath)
	}
}

func TestDevinStatusVariants(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name   string
		body   string
		status TelemetryStatus
	}{
		{name: "invalid json", body: `{`, status: TelemetryMisconfigured},
		{name: "no beacon hooks", body: `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`, status: TelemetryDisabled},
		{name: "enabled", body: `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin pre-tool"}]}]}}`, status: TelemetryEnabled},
		{name: "standalone enabled", body: `{"PreToolUse":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin pre-tool"}]}]}`, status: TelemetryEnabled},
		{name: "devin cli enabled", body: `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin-cli pre-tool"}]}]}}`, status: TelemetryEnabled},
		{name: "desktop does not satisfy cli", body: `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin-desktop pre-tool"}]}]}}`, status: TelemetryDisabled},
		{name: "beacon string outside hooks", body: `{"note":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin pre-tool"}`, status: TelemetryDisabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".json")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			status, _ := devinStatus(path, "devin", "devin-cli")
			if status != tt.status {
				t.Fatalf("devinStatus = %q, want %q", status, tt.status)
			}
		})
	}
}

func TestFactoryStatusVariants(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name   string
		body   string
		status TelemetryStatus
	}{
		{name: "missing export", body: `export OTHER=1`, status: TelemetryDisabled},
		{name: "remote endpoint", body: `export OTEL_TELEMETRY_ENDPOINT="https://example.com:4318"`, status: TelemetryMisconfigured},
		{name: "localhost enabled", body: `export OTEL_TELEMETRY_ENDPOINT="http://localhost:4318"`, status: TelemetryEnabled},
		{name: "loopback enabled", body: `export OTEL_TELEMETRY_ENDPOINT=http://127.0.0.1:4318`, status: TelemetryEnabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".profile")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			status, _ := factoryStatus(path)
			if status != tt.status {
				t.Fatalf("factoryStatus = %q, want %q", status, tt.status)
			}
		})
	}
}

func TestCopilotStatusVariants(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name   string
		body   string
		status TelemetryStatus
	}{
		{name: "missing export", body: `export OTHER=1`, status: TelemetryDisabled},
		{name: "enabled default endpoint", body: `export COPILOT_OTEL_ENABLED=true`, status: TelemetryEnabled},
		{name: "endpoint without enable flag", body: `export OTEL_EXPORTER_OTLP_ENDPOINT="https://example.com:4318"`, status: TelemetryDisabled},
		{name: "copilot endpoint without enable flag", body: `export COPILOT_OTEL_ENDPOINT="http://localhost:4318"`, status: TelemetryDisabled},
		{name: "standard endpoint without enable flag", body: `export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318`, status: TelemetryDisabled},
		{name: "enabled remote endpoint", body: "export COPILOT_OTEL_ENABLED=true\nexport OTEL_EXPORTER_OTLP_ENDPOINT=\"https://example.com:4318\"", status: TelemetryMisconfigured},
		{name: "enabled copilot endpoint", body: "export COPILOT_OTEL_ENABLED=true\nexport COPILOT_OTEL_ENDPOINT=\"http://localhost:4318\"", status: TelemetryEnabled},
		{name: "enabled standard endpoint", body: "export COPILOT_OTEL_ENABLED=true\nexport OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318", status: TelemetryEnabled},
		{name: "file exporter bypass", body: "export COPILOT_OTEL_ENABLED=true\nexport COPILOT_OTEL_FILE_EXPORTER_PATH=/tmp/copilot.jsonl", status: TelemetryMisconfigured},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCopilotEnv(t)
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".profile")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			status, _ := copilotStatus(path)
			if status != tt.status {
				t.Fatalf("copilotStatus = %q, want %q", status, tt.status)
			}
		})
	}
}

func TestCopilotStatusReadsProcessEnvironment(t *testing.T) {
	clearCopilotEnv(t)
	t.Setenv("COPILOT_OTEL_ENABLED", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:54318")

	status, msg := copilotStatus(filepath.Join(t.TempDir(), "missing.profile"), "http://127.0.0.1:54318")
	if status != TelemetryEnabled {
		t.Fatalf("copilotStatus = %q (%s), want %q", status, msg, TelemetryEnabled)
	}
}

func TestCopilotStatusValidatesExpectedHTTPPort(t *testing.T) {
	clearCopilotEnv(t)
	path := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(path, []byte(`export COPILOT_OTEL_ENABLED=true`), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	status, _ := copilotStatus(path, "http://127.0.0.1:54318")
	if status != TelemetryMisconfigured {
		t.Fatalf("copilotStatus = %q, want %q for default 4318 endpoint with expected custom port", status, TelemetryMisconfigured)
	}
}

func TestCopilotStatusMergesProfileWithGenericProcessEndpoint(t *testing.T) {
	clearCopilotEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")

	path := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(path, []byte("export COPILOT_OTEL_ENABLED=true\n"), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	status, msg := copilotStatus(path)
	if status != TelemetryEnabled {
		t.Fatalf("copilotStatus = %q (%s), want %q; generic OTEL_EXPORTER_OTLP_ENDPOINT should not suppress profile COPILOT_OTEL_ENABLED", status, msg, TelemetryEnabled)
	}
}

func clearCopilotEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"COPILOT_OTEL_ENABLED",
		"COPILOT_OTEL_ENDPOINT",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"COPILOT_OTEL_FILE_EXPORTER_PATH",
	} {
		t.Setenv(key, "")
	}
}

// Cline is the one hook runtime that cannot be found by looking for an executable: it runs as a VS
// Code extension, a JetBrains plugin and a CLI over one agent core, and the two IDE hosts ship no
// `cline` binary. Detecting on the directory alone is deliberate here, and the opposite of what
// DiscoverCodex and DiscoverGemini do -- a binary probe would report "not detected" on the majority
// of real Cline installs.
func TestDiscoverClineDetectsConfigDirectoryWithoutAnExecutable(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("CLINE_DIR", "")
	t.Setenv("PATH", t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".cline"), 0755); err != nil {
		t.Fatalf("mkdir cline config dir: %v", err)
	}

	h := DiscoverCline()
	if !h.Detected {
		t.Fatalf("DiscoverCline did not detect Cline from its config directory: %#v", h)
	}
	if h.ExecutablePath != "" {
		t.Fatalf("ExecutablePath = %q, want empty with no binary on PATH", h.ExecutablePath)
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("TelemetryStatus = %q, want %q", h.TelemetryStatus, TelemetryMissing)
	}
	if h.ConfigPath != filepath.Join(home, ".cline", "plugins", "beacon.ts") {
		t.Fatalf("ConfigPath = %q, want the managed plugin path", h.ConfigPath)
	}
}

func TestDiscoverClineTelemetryStatusVariants(t *testing.T) {
	t.Setenv("CLINE_DIR", "")
	cases := []struct {
		name    string
		plugin  string
		want    TelemetryStatus
		message string
	}{
		{name: "managed plugin", plugin: "// beacon-managed-cline-plugin:v1\nexport default {}", want: TelemetryEnabled, message: "configured"},
		// Someone else's plugin at Beacon's path. Reporting that as enabled would claim telemetry
		// Beacon is not collecting.
		{name: "unmanaged plugin", plugin: "export default { name: \"someone-else\" }", want: TelemetryDisabled, message: "was not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			testenv.SetHome(t, home)
			t.Setenv("PATH", t.TempDir())
			pluginPath := filepath.Join(home, ".cline", "plugins", "beacon.ts")
			if err := os.MkdirAll(filepath.Dir(pluginPath), 0755); err != nil {
				t.Fatalf("mkdir cline plugin dir: %v", err)
			}
			if err := os.WriteFile(pluginPath, []byte(tc.plugin), 0644); err != nil {
				t.Fatalf("write cline plugin: %v", err)
			}

			h := DiscoverCline()
			if h.TelemetryStatus != tc.want {
				t.Fatalf("TelemetryStatus = %q, want %q", h.TelemetryStatus, tc.want)
			}
			if !strings.Contains(h.Message, tc.message) {
				t.Fatalf("Message = %q, want it to mention %q", h.Message, tc.message)
			}
		})
	}
}

// A runtime missing from DiscoverAll is invisible to `beacon endpoint discover` -- the command
// succeeds and simply never mentions it. And the name discovery reports must be the canonical one
// events are written under, or the dashboard groups a runtime's discovery row and its telemetry rows
// separately.
func TestDiscoverAllRegistersRuntimesUnderCanonicalNames(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("PATH", t.TempDir())
	// The session collectors write these names; discovery has to report the same ones.
	if FxName != fxsession.Harness || GrokName != groksession.Harness {
		t.Fatalf("discovery reports %q/%q while the collectors write %q/%q", FxName, GrokName, fxsession.Harness, groksession.Harness)
	}
	want := map[string]string{
		"cline":       "plugin",
		"pi_cli":      "plugin",
		"omp":         "plugin",
		"omo_senpi":   "plugin",
		"prime_agent": "plugin",
		FxName:        "session_log",
		GrokName:      "hooks+session_log",
		// "hooks", not "otel_env": Qwen Code is a Gemini CLI fork without Gemini's OpenTelemetry
		// export, so `endpoint install --harness qwen` has nothing to configure.
		"qwen_code": "hooks",
	}
	for _, h := range DiscoverAll() {
		capability, ok := want[h.Name]
		if !ok {
			continue
		}
		delete(want, h.Name)
		if h.Capability != capability {
			t.Errorf("%s capability = %q, want %q", h.Name, h.Capability, capability)
		}
		if got := asymptoteobserve.NormalizeHarnessName(h.Name); got != h.Name {
			t.Errorf("discovery reports %q, which normalizes to %q; discovery and telemetry would not join", h.Name, got)
		}
	}
	if len(want) != 0 {
		t.Fatalf("DiscoverAll does not include %v", want)
	}
}

// An unresolvable home directory must stop Cline discovery rather than produce a relative path.
//
// Cline is the sharpest case for this in the whole harness set: its project install is
// ".cline/plugins/beacon.ts", which is the user layout with the home prefix removed. A relative
// path therefore lands exactly where a project install lives, so discovery would read a
// repository's own plugin and report Cline detected with telemetry enabled for the machine -- on
// the strength of a file in whatever directory the command happened to run from.
//
// An empty HOME is not hypothetical for this binary: system-mode Beacon runs under launchd, and
// some CI runners leave it unset.
func TestDiscoverClineDoesNotFallBackToAProjectPluginWhenHomeIsUnresolvable(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	t.Setenv("PATH", t.TempDir())
	// An empty HOME is what makes os.UserHomeDir fail; USERPROFILE is cleared for the same reason on
	// Windows.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("CLINE_DIR", "")

	pluginPath := filepath.Join(work, ".cline", "plugins", "beacon.ts")
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0755); err != nil {
		t.Fatalf("mkdir project plugin dir: %v", err)
	}
	if err := os.WriteFile(pluginPath, []byte("// beacon-managed-cline-plugin:v1"), 0644); err != nil {
		t.Fatalf("write project plugin: %v", err)
	}

	h := DiscoverCline()
	if h.TelemetryStatus == TelemetryEnabled {
		t.Errorf("TelemetryStatus = %q; a project plugin was reported as the user install", h.TelemetryStatus)
	}
	if h.ConfigPath != "" {
		t.Errorf("ConfigPath = %q, want empty when the home directory cannot be resolved", h.ConfigPath)
	}
	if h.Detected {
		t.Errorf("Detected = true from a project directory with no resolvable home")
	}
	if !strings.Contains(h.Message, "could not be resolved") {
		t.Errorf("Message = %q, want it to name the unresolved directory", h.Message)
	}
}

// hermesDiscoverHome moves the home directory to a temporary one with no hermes executable on PATH,
// clears every variable that moves Hermes's home, and returns Hermes's native root there.
func hermesDiscoverHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "LocalAppData"))
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "LocalAppData", "hermes")
	}
	return filepath.Join(home, ".hermes")
}

const hermesBeaconHooksYAML = `hooks:
  on_session_end:
    - command: env BEACON_ENDPOINT_MODE=1 beacon-hooks --platform hermes session-end
`

func writeHermesConfigFixture(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Discovery reports the config.yaml Hermes reads: HERMES_HOME's, or the sticky profile's, rather
// than a stale ~/.hermes/config.yaml.
func TestDiscoverHermesFollowsHermesHomeAndActiveProfile(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		native := hermesDiscoverHome(t)
		want := writeHermesConfigFixture(t, native, hermesBeaconHooksYAML)
		h := DiscoverHermes()
		if h.ConfigPath != want || !h.Detected || h.TelemetryStatus != TelemetryEnabled {
			t.Fatalf("DiscoverHermes = path %q detected %v status %q, want %q detected and enabled", h.ConfigPath, h.Detected, h.TelemetryStatus, want)
		}
	})
	t.Run("HERMES_HOME", func(t *testing.T) {
		native := hermesDiscoverHome(t)
		writeHermesConfigFixture(t, native, "model: stale\n")
		dir := t.TempDir()
		t.Setenv("HERMES_HOME", dir)
		want := writeHermesConfigFixture(t, dir, hermesBeaconHooksYAML)
		h := DiscoverHermes()
		if h.ConfigPath != want || !h.Detected || h.TelemetryStatus != TelemetryEnabled {
			t.Fatalf("DiscoverHermes = path %q detected %v status %q, want %q detected and enabled", h.ConfigPath, h.Detected, h.TelemetryStatus, want)
		}
	})
	t.Run("active profile", func(t *testing.T) {
		native := hermesDiscoverHome(t)
		writeHermesConfigFixture(t, native, hermesBeaconHooksYAML)
		profile := filepath.Join(native, "profiles", "work")
		if err := os.MkdirAll(profile, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(native, "active_profile"), []byte("work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		h := DiscoverHermes()
		want := filepath.Join(profile, "config.yaml")
		if h.ConfigPath != want || !h.Detected || h.TelemetryStatus != TelemetryMissing {
			t.Fatalf("DiscoverHermes = path %q detected %v status %q, want %q detected and missing: the default profile's hooks do not run in this profile", h.ConfigPath, h.Detected, h.TelemetryStatus, want)
		}
	})
}

// Hermes refuses to start when active_profile names a profile it cannot use; discovery says so
// instead of reporting the default profile's config.
func TestDiscoverHermesReportsUnusableActiveProfile(t *testing.T) {
	native := hermesDiscoverHome(t)
	writeHermesConfigFixture(t, native, hermesBeaconHooksYAML)
	if err := os.WriteFile(filepath.Join(native, "active_profile"), []byte("gone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := DiscoverHermes()
	if h.TelemetryStatus != TelemetryMissing || !strings.Contains(h.Message, "active profile") || h.ConfigPath != "" {
		t.Fatalf("DiscoverHermes = status %q message %q path %q, want missing naming the active profile", h.TelemetryStatus, h.Message, h.ConfigPath)
	}
}

func TestGeminiSettingsHonorGeminiCLIHome(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("GEMINI_CLI_HOME", "")
	if got, want := GeminiSettingsPath(home), filepath.Join(home, ".gemini", "settings.json"); got != want {
		t.Fatalf("default path = %q, want %q", got, want)
	}

	geminiHome := t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", geminiHome)
	want := filepath.Join(geminiHome, ".gemini", "settings.json")
	written, err := ConfigureGemini(ConfigureOptions{Endpoint: "http://127.0.0.1:4317", UserMode: true})
	if err != nil {
		t.Fatalf("ConfigureGemini returned error: %v", err)
	}
	if written != want {
		t.Fatalf("ConfigureGemini path = %q, want %q", written, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini")); !os.IsNotExist(err) {
		t.Fatalf("settings were also written under the OS home: %v", err)
	}
	if got := DiscoverGemini().ConfigPath; got != want {
		t.Fatalf("DiscoverGemini config path = %q, want %q", got, want)
	}
}

func TestClaudeConfigFollowsClaudeConfigDir(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	config := filepath.Join(t.TempDir(), "claude-alt")
	t.Setenv("CLAUDE_CONFIG_DIR", config)

	written, err := ConfigureClaude(ConfigureOptions{Endpoint: "http://127.0.0.1:4317", UserMode: true})
	if err != nil {
		t.Fatalf("ConfigureClaude returned error: %v", err)
	}
	if want := filepath.Join(config, "settings.json"); written != want {
		t.Fatalf("ConfigureClaude path = %q, want %q", written, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("~/.claude/settings.json must be left alone, stat err = %v", err)
	}

	h := DiscoverClaude()
	if want := filepath.Join(config, "settings.json"); h.ConfigPath != want {
		t.Fatalf("DiscoverClaude config path = %q, want %q", h.ConfigPath, want)
	}
	if h.TelemetryStatus != TelemetryEnabled {
		t.Fatalf("DiscoverClaude status = %q (%s), want enabled", h.TelemetryStatus, h.Message)
	}
}
