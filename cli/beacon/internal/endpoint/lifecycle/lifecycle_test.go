package lifecycle

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	beaconauth "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/auth"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

func TestRestoreTargetTimestampedBackup(t *testing.T) {
	got := restoreTarget("/tmp/settings.json.beacon.20260511T230000Z.bak")
	want := "/tmp/settings.json"
	if got != want {
		t.Fatalf("restoreTarget() = %q, want %q", got, want)
	}
}

func TestRestoreTargetLegacyBackup(t *testing.T) {
	got := restoreTarget("/tmp/settings.json.beacon.bak")
	want := "/tmp/settings.json"
	if got != want {
		t.Fatalf("restoreTarget() = %q, want %q", got, want)
	}
}

func TestBuildConfigAppliesInstallOptions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logPath := filepath.Join(home, "runtime.jsonl")
	collectorPath := filepath.Join(home, "bin", "otelcol")

	cfg := buildConfig(InstallOptions{
		UserMode:      true,
		LogPath:       logPath,
		Harnesses:     []string{"codex"},
		GRPCPort:      54317,
		HTTPPort:      54318,
		CollectorPath: collectorPath,
		SplunkHEC: &endpointconfig.SplunkHEC{
			Endpoint: "https://splunk.example:8088/services/collector",
			Token:    "hec-token",
			Index:    "beacon",
		},
		FalconHEC: &endpointconfig.FalconHEC{
			Endpoint: "https://cloud.us.humio.com/api/v1/ingest/hec",
			Token:    "ingest-token",
			Index:    "beacon-repo",
		},
	})

	if !cfg.UserMode || cfg.LogPath != logPath {
		t.Fatalf("unexpected mode/log path: %#v", cfg)
	}
	if got := cfg.Harnesses; len(got) != 1 || got[0] != "codex" {
		t.Fatalf("Harnesses = %#v, want codex", got)
	}
	if cfg.Collector.GRPCPort != 54317 || cfg.Collector.HTTPPort != 54318 {
		t.Fatalf("unexpected ports: %#v", cfg.Collector)
	}
	if cfg.Collector.BinaryPath != collectorPath {
		t.Fatalf("BinaryPath = %q, want %q", cfg.Collector.BinaryPath, collectorPath)
	}
	if cfg.Destinations == nil || cfg.Destinations.SplunkHEC == nil || !cfg.Destinations.SplunkHEC.Enabled {
		t.Fatalf("Splunk destination not configured: %#v", cfg.Destinations)
	}
	if got := cfg.Destinations.SplunkHEC.Source; got != endpointconfig.DefaultSplunkSource {
		t.Fatalf("Splunk source = %q, want default", got)
	}
	if cfg.Destinations.FalconHEC == nil || !cfg.Destinations.FalconHEC.Enabled {
		t.Fatalf("Falcon destination not configured: %#v", cfg.Destinations)
	}
	if got := cfg.Destinations.FalconHEC.Sourcetype; got != endpointconfig.DefaultFalconSourcetype {
		t.Fatalf("Falcon sourcetype = %q, want default", got)
	}
}

func TestBuildConfigPreservesExplicitEmptyHarnesses(t *testing.T) {
	cfg := buildConfig(InstallOptions{UserMode: true, Harnesses: []string{}})
	if cfg.Harnesses == nil || len(cfg.Harnesses) != 0 {
		t.Fatalf("Harnesses = %#v, want explicit empty slice", cfg.Harnesses)
	}
	defaultCfg := buildConfig(InstallOptions{UserMode: true})
	if len(defaultCfg.Harnesses) == 0 {
		t.Fatalf("default harnesses should be preserved when Harnesses is nil")
	}
}

func TestSelectRuntimeLogWarnsWhenSystemCollectorConflictsWithUserMode(t *testing.T) {
	userLog := filepath.Join(t.TempDir(), "user-runtime.jsonl")
	systemLog := filepath.Join(t.TempDir(), "system-runtime.jsonl")
	source := RuntimeLogSource{
		RequestedUserMode: true,
		EffectiveUserMode: true,
		RequestedLogPath:  userLog,
		EffectiveLogPath:  userLog,
	}
	systemCfg := endpointconfig.Config{LogPath: systemLog}

	got := selectRuntimeLog(
		source,
		service.Status{Label: service.UserLabel, Loaded: false, Running: false},
		service.Status{Label: service.SystemLabel, Loaded: true, Running: true},
		systemCfg,
	)

	if got.EffectiveUserMode {
		t.Fatal("expected effective system mode")
	}
	if got.EffectiveLogPath != systemLog {
		t.Fatalf("EffectiveLogPath = %q, want %q", got.EffectiveLogPath, systemLog)
	}
	if got.Warning == "" || !strings.Contains(got.Warning, userLog) || !strings.Contains(got.Warning, systemLog) {
		t.Fatalf("Warning = %q, want user/system log paths", got.Warning)
	}
}

func TestSelectRuntimeLogKeepsUserLogWhenUserCollectorIsRunning(t *testing.T) {
	userLog := filepath.Join(t.TempDir(), "user-runtime.jsonl")
	systemLog := filepath.Join(t.TempDir(), "system-runtime.jsonl")
	source := RuntimeLogSource{
		RequestedUserMode: true,
		EffectiveUserMode: true,
		RequestedLogPath:  userLog,
		EffectiveLogPath:  userLog,
	}
	systemCfg := endpointconfig.Config{LogPath: systemLog}

	got := selectRuntimeLog(
		source,
		service.Status{Label: service.UserLabel, Loaded: true, Running: true},
		service.Status{Label: service.SystemLabel, Loaded: true, Running: true},
		systemCfg,
	)

	if !got.EffectiveUserMode {
		t.Fatal("expected effective user mode")
	}
	if got.EffectiveLogPath != userLog {
		t.Fatalf("EffectiveLogPath = %q, want %q", got.EffectiveLogPath, userLog)
	}
	if got.Warning != "" {
		t.Fatalf("Warning = %q, want empty", got.Warning)
	}
}

func TestConfigureHarnessesRejectsUnsupportedHarness(t *testing.T) {
	cfg := endpointconfig.Config{Harnesses: []string{"unknown"}}
	if _, err := configureHarnesses(cfg); err == nil || !strings.Contains(err.Error(), "unsupported harness") {
		t.Fatalf("configureHarnesses error = %v, want unsupported harness", err)
	}
}

func TestGetStatusDoesNotUploadManagedTelemetry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logPath := filepath.Join(home, "runtime.jsonl")
	if err := os.WriteFile(logPath, []byte("{\"event\":\"ok\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "status should not upload", http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := endpointconfig.Save(endpointconfig.Config{
		UserMode:  true,
		LogPath:   logPath,
		Harnesses: []string{},
		ManagedUpload: &endpointconfig.ManagedUpload{
			Enabled:   true,
			Managed:   true,
			IngestURL: server.URL,
			SourceID:  "source-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := beaconauth.SaveCredentials(&beaconauth.Credentials{Token: "token", UserID: "user"}); err != nil {
		t.Fatal(err)
	}

	status := GetStatus(true, logPath)
	if !status.ManagedUpload.Enabled || !status.ManagedUpload.Managed || !status.ManagedUpload.LoggedIn {
		t.Fatalf("unexpected endpoint ingest status: %#v", status.ManagedUpload)
	}
	if called {
		t.Fatal("GetStatus performed an ingest upload network call")
	}
}

func TestConfigureHarnessesRejectsFactoryAsMDMManaged(t *testing.T) {
	cfg := endpointconfig.Config{
		Harnesses: []string{"factory"},
		Collector: endpointconfig.Collector{
			GRPCPort: 54317,
			HTTPPort: 54318,
		},
	}

	_, err := configureHarnesses(cfg)
	if err == nil || !strings.Contains(err.Error(), "Factory Droid telemetry is MDM-managed") || !strings.Contains(err.Error(), "54318") {
		t.Fatalf("configureHarnesses error = %v, want MDM-managed Factory error with HTTP port", err)
	}
}

func TestConfigureHarnessesRejectsCopilotCLIAsMDMManaged(t *testing.T) {
	cfg := endpointconfig.Config{
		Harnesses: []string{"copilot"},
		Collector: endpointconfig.Collector{
			GRPCPort: 54317,
			HTTPPort: 54318,
		},
	}

	_, err := configureHarnesses(cfg)
	if err == nil || !strings.Contains(err.Error(), "Copilot CLI telemetry is MDM-managed") || !strings.Contains(err.Error(), "54318") {
		t.Fatalf("configureHarnesses error = %v, want MDM-managed Copilot CLI error with HTTP port", err)
	}
}

func TestConfigureHarnessesRejectsOpenCodeAsHookManaged(t *testing.T) {
	cfg := endpointconfig.Config{Harnesses: []string{"opencode"}}
	_, err := configureHarnesses(cfg)
	if err == nil || !strings.Contains(err.Error(), "endpoint hooks install --harness opencode") {
		t.Fatalf("configureHarnesses error = %v, want opencode hook-managed error", err)
	}
}

func TestConfigureHarnessesAcceptsGemini(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := endpointconfig.Config{
		UserMode:  true,
		Harnesses: []string{"gemini"},
		Collector: endpointconfig.Collector{
			GRPCPort: 54317,
		},
	}

	paths, err := configureHarnesses(cfg)
	if err != nil {
		t.Fatalf("configureHarnesses returned error: %v", err)
	}
	wantPath := filepath.Join(home, ".gemini", "settings.json")
	if len(paths) != 1 || paths[0] != wantPath {
		t.Fatalf("paths = %#v, want %q", paths, wantPath)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read Gemini settings: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"otlpEndpoint": "http://127.0.0.1:54317"`) || !strings.Contains(text, `"logPrompts": true`) {
		t.Fatalf("Gemini settings were not configured for local OTLP with prompt logging:\n%s", text)
	}
}

func TestWriteReadManifestRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	manifest := Manifest{
		CreatedAt:      "2026-05-12T00:00:00Z",
		UserMode:       true,
		Files:          []string{filepath.Join(home, "config.json")},
		Backups:        []string{filepath.Join(home, "settings.json.beacon.bak")},
		HarnessConfigs: []string{filepath.Join(home, ".claude", "settings.json")},
		ServiceLabel:   "com.beacon.endpoint.collector.user",
		LogPath:        filepath.Join(home, "runtime.jsonl"),
	}

	path, err := writeManifest(true, manifest)
	if err != nil {
		t.Fatalf("writeManifest returned error: %v", err)
	}
	if got, want := path, filepath.Join(home, ".beacon", "endpoint", "install-manifest.json"); got != want {
		t.Fatalf("manifest path = %q, want %q", got, want)
	}
	loaded, err := ReadManifest(true)
	if err != nil {
		t.Fatalf("ReadManifest returned error: %v", err)
	}
	if loaded.ServiceLabel != manifest.ServiceLabel || loaded.Files[0] != manifest.Files[0] || loaded.Backups[0] != manifest.Backups[0] {
		t.Fatalf("manifest did not round-trip: %#v", loaded)
	}
}

func TestAutoUpdateModeFromConfigFileIgnoresDestinationValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
  "user_mode": false,
  "log_path": "/var/log/beacon-agent/runtime.jsonl",
  "collector": {"grpc_port":4317,"http_port":4318},
  "harnesses": ["claude"],
  "destinations": {"splunk_hec": {"endpoint": "https://splunk.example"}},
  "auto_update": {"mode": "auto"}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mode, err := autoUpdateModeFromConfigFile(path)
	if err != nil {
		t.Fatalf("autoUpdateModeFromConfigFile: %v", err)
	}
	if mode != "auto" {
		t.Fatalf("mode = %q, want auto", mode)
	}
}

func TestDiscoverAndRestoreBackups(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "settings.json")
	backup := target + ".beacon.20260512T120000Z.bak"
	if err := os.WriteFile(backup, []byte("restored"), 0600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	backups := discoverBackups([]string{target})
	if len(backups) != 1 || backups[0] != backup {
		t.Fatalf("discoverBackups = %#v, want %q", backups, backup)
	}
	restoreBackups(backups)
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read restored target: %v", err)
	}
	if string(data) != "restored" {
		t.Fatalf("restored target = %q", string(data))
	}
}

func TestRollbackRestoresBackupsAndRemovesFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "settings.json")
	backup := target + ".beacon.bak"
	created := filepath.Join(dir, "created.txt")
	if err := os.WriteFile(backup, []byte("original"), 0600); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	if err := os.WriteFile(created, []byte("created"), 0600); err != nil {
		t.Fatalf("write created file: %v", err)
	}

	rollback(Manifest{Files: []string{created}, Backups: []string{backup}})

	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("created file still exists or unexpected error: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read restored target: %v", err)
	}
	if string(data) != "original" {
		t.Fatalf("restored target = %q", string(data))
	}
}

func TestPreflightRejectsPortInUse(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("preflight is macOS-only")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	cfg := endpointconfig.Default(true, filepath.Join(t.TempDir(), "runtime.jsonl"))
	cfg.Collector.GRPCPort = port
	cfg.Collector.HTTPPort = freePort(t)

	err = preflight(cfg, true)
	if err == nil || !strings.Contains(err.Error(), "OTLP gRPC port") {
		t.Fatalf("preflight error = %v, want gRPC port in use", err)
	}
}

func TestPreflightAllowsPortInUseWhenServiceWillNotStart(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("preflight is macOS-only")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	cfg := endpointconfig.Default(true, filepath.Join(t.TempDir(), "runtime.jsonl"))
	cfg.Collector.GRPCPort = port
	cfg.Collector.HTTPPort = freePort(t)

	if err := preflight(cfg, false); err != nil {
		t.Fatalf("preflight returned error for no-start install: %v", err)
	}
}

func TestUninstallFallbackRemovesKnownFiles(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd paths are macOS-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	logPath := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
	cfg := endpointconfig.Default(true, logPath)
	for _, path := range []string{endpointconfig.ConfigPath(true), cfg.Collector.ConfigPath, logPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	if err := Uninstall(UninstallOptions{UserMode: true, LogPath: logPath}); err != nil {
		t.Fatalf("Uninstall returned error: %v", err)
	}
	for _, path := range []string{endpointconfig.ConfigPath(true), cfg.Collector.ConfigPath, logPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists or unexpected error: %v", path, err)
		}
	}
}

func TestConfigureHarnessesAcceptsVSCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := endpointconfig.Config{
		UserMode:  true,
		Harnesses: []string{"vscode"},
		Collector: endpointconfig.Collector{
			GRPCPort: 54317,
			HTTPPort: 54318,
		},
	}

	paths, err := configureHarnesses(cfg)
	if err != nil {
		t.Fatalf("configureHarnesses returned error: %v", err)
	}
	settingsPath, err := harness.VSCodeUserSettingsPath()
	if err != nil {
		t.Fatalf("VSCodeUserSettingsPath returned error: %v", err)
	}
	if len(paths) != 1 || paths[0] != settingsPath {
		t.Fatalf("paths = %#v, want VS Code settings path %q", paths, settingsPath)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read VS Code settings: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"github.copilot.chat.otel.otlpEndpoint": "http://127.0.0.1:54318"`) ||
		!strings.Contains(text, `"github.copilot.chat.otel.captureContent": false`) {
		t.Fatalf("VS Code settings did not configure low-noise local OTLP: %s", text)
	}
}

func TestInstallUserModeWithoutStartingService(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("install preflight is macOS-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	collectorPath := filepath.Join(home, "bin", "beacon-otelcol")
	if err := os.MkdirAll(filepath.Dir(collectorPath), 0755); err != nil {
		t.Fatalf("mkdir fake collector dir: %v", err)
	}
	if err := os.WriteFile(collectorPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write fake collector: %v", err)
	}
	logPath := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")

	result, err := Install(InstallOptions{
		UserMode:      true,
		LogPath:       logPath,
		Harnesses:     []string{},
		GRPCPort:      freePort(t),
		HTTPPort:      freePort(t),
		CollectorPath: collectorPath,
		StartService:  false,
	})
	if err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	for _, path := range []string{result.ConfigPath, result.CollectorConfigPath, result.PlistPath, result.ManifestPath, result.LogPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected install artifact %s: %v", path, err)
		}
	}
	manifest, err := ReadManifest(true)
	if err != nil {
		t.Fatalf("ReadManifest returned error: %v", err)
	}
	if len(manifest.Files) != 3 {
		t.Fatalf("manifest files = %#v, want collector config, plist, config", manifest.Files)
	}
	if len(result.HarnessConfigPaths) != 0 {
		t.Fatalf("unexpected harness config paths: %#v", result.HarnessConfigPaths)
	}
}

func TestInstallFailsBeforeWritingArtifactsWhenCollectorMissing(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("install preflight is macOS-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	logPath := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
	missingCollector := filepath.Join(home, "bin", "beacon-otelcol")

	_, err := Install(InstallOptions{
		UserMode:      true,
		LogPath:       logPath,
		Harnesses:     []string{},
		GRPCPort:      freePort(t),
		HTTPPort:      freePort(t),
		CollectorPath: missingCollector,
		StartService:  false,
	})
	if err == nil || !strings.Contains(err.Error(), "collector binary") || !strings.Contains(err.Error(), "not usable") {
		t.Fatalf("Install error = %v, want missing collector error", err)
	}
	for _, path := range []string{endpointconfig.ConfigPath(true), logPath} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("%s exists or unexpected error after failed install: %v", path, statErr)
		}
	}
}

func TestInstallRollsBackArtifactsWhenFinalEventFails(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("install preflight is macOS-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	collectorPath := filepath.Join(home, "bin", "beacon-otelcol")
	if err := os.MkdirAll(filepath.Dir(collectorPath), 0755); err != nil {
		t.Fatalf("mkdir fake collector dir: %v", err)
	}
	if err := os.WriteFile(collectorPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write fake collector: %v", err)
	}
	oldAppend := appendInstallEvent
	appendInstallEvent = func(event schema.Event, opts writer.Options) (string, error) {
		return "", errors.New("append failed")
	}
	t.Cleanup(func() {
		appendInstallEvent = oldAppend
	})
	logPath := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")

	_, err := Install(InstallOptions{
		UserMode:      true,
		LogPath:       logPath,
		Harnesses:     []string{},
		GRPCPort:      freePort(t),
		HTTPPort:      freePort(t),
		CollectorPath: collectorPath,
		StartService:  false,
	})
	if err == nil || !strings.Contains(err.Error(), "append failed") {
		t.Fatalf("Install error = %v, want append failure", err)
	}
	cfg := endpointconfig.Default(true, logPath)
	for _, path := range []string{endpointconfig.ConfigPath(true), cfg.Collector.ConfigPath, manifestPath(true)} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("%s exists or unexpected error after rollback: %v", path, statErr)
		}
	}
}

func TestRepairPreservesExistingConfigWhenReinstallFails(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("install preflight is macOS-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	logPath := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
	configPath := endpointconfig.ConfigPath(true)
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	const originalConfig = `{"user_mode":true,"log_path":"original","collector":{"config_path":"original","grpc_port":4317,"http_port":4318}}`
	if err := os.WriteFile(configPath, []byte(originalConfig), 0600); err != nil {
		t.Fatalf("write original config: %v", err)
	}
	collectorPath := filepath.Join(home, "bin", "beacon-otelcol")
	if err := os.MkdirAll(filepath.Dir(collectorPath), 0755); err != nil {
		t.Fatalf("mkdir fake collector dir: %v", err)
	}
	if err := os.WriteFile(collectorPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write fake collector: %v", err)
	}
	oldAppend := appendInstallEvent
	appendInstallEvent = func(event schema.Event, opts writer.Options) (string, error) {
		return "", errors.New("append failed")
	}
	t.Cleanup(func() {
		appendInstallEvent = oldAppend
	})

	_, err := Repair(InstallOptions{
		UserMode:      true,
		LogPath:       logPath,
		Harnesses:     []string{},
		GRPCPort:      freePort(t),
		HTTPPort:      freePort(t),
		CollectorPath: collectorPath,
		StartService:  false,
	})
	if err == nil {
		t.Fatal("Repair returned nil, want append failure")
	}
	data, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("read restored config: %v", readErr)
	}
	if string(data) != originalConfig {
		t.Fatalf("config was not restored: %s", string(data))
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestConfigureHarnessesAcceptsClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := endpointconfig.Config{
		UserMode:  true,
		Harnesses: []string{"claude"},
		Collector: endpointconfig.Collector{GRPCPort: 54317},
	}

	paths, err := configureHarnesses(cfg)
	if err != nil {
		t.Fatalf("configureHarnesses returned error: %v", err)
	}
	wantPath := filepath.Join(home, ".claude", "settings.json")
	if len(paths) != 1 || paths[0] != wantPath {
		t.Fatalf("paths = %#v, want %q", paths, wantPath)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read Claude settings: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"CLAUDE_CODE_ENABLE_TELEMETRY": "1"`) ||
		!strings.Contains(text, `"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:54317"`) {
		t.Fatalf("Claude settings were not configured for local OTLP:\n%s", text)
	}
}

func TestSameCollectorPorts(t *testing.T) {
	base := endpointconfig.Config{Collector: endpointconfig.Collector{GRPCPort: 4317, HTTPPort: 4318}}
	same := endpointconfig.Config{Collector: endpointconfig.Collector{GRPCPort: 4317, HTTPPort: 4318}}
	if !sameCollectorPorts(base, same) {
		t.Fatal("sameCollectorPorts returned false for equal ports")
	}
	differentGRPC := endpointconfig.Config{Collector: endpointconfig.Collector{GRPCPort: 14317, HTTPPort: 4318}}
	if sameCollectorPorts(base, differentGRPC) {
		t.Fatal("sameCollectorPorts returned true for differing gRPC port")
	}
	differentHTTP := endpointconfig.Config{Collector: endpointconfig.Collector{GRPCPort: 4317, HTTPPort: 14318}}
	if sameCollectorPorts(base, differentHTTP) {
		t.Fatal("sameCollectorPorts returned true for differing HTTP port")
	}
}

func TestLoadOrDefaultFallsBackToDefaultThenLoadsSavedConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logPath := filepath.Join(home, "runtime.jsonl")

	// No config saved yet: returns a default with the requested log path.
	got := loadOrDefault(true, logPath)
	if got.LogPath != logPath {
		t.Fatalf("default LogPath = %q, want %q", got.LogPath, logPath)
	}
	if !got.UserMode {
		t.Fatal("default config should preserve user mode")
	}

	// Persist a config and confirm it is loaded back.
	saved := endpointconfig.Default(true, filepath.Join(home, "saved.jsonl"))
	if _, err := endpointconfig.Save(saved); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	loaded := loadOrDefault(true, "")
	if loaded.LogPath != saved.LogPath {
		t.Fatalf("loaded LogPath = %q, want %q", loaded.LogPath, saved.LogPath)
	}
	// An explicit log path overrides the saved value.
	overridden := loadOrDefault(true, logPath)
	if overridden.LogPath != logPath {
		t.Fatalf("override LogPath = %q, want %q", overridden.LogPath, logPath)
	}
}

func TestSnapshotAndRestoreFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("original"), 0640); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	snap := snapshotFile(path)
	if !snap.Existed || string(snap.Data) != "original" {
		t.Fatalf("snapshot did not capture existing file: %#v", snap)
	}

	if err := os.WriteFile(path, []byte("mutated"), 0640); err != nil {
		t.Fatalf("mutate file: %v", err)
	}
	restoreFile(path, snap)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(data) != "original" {
		t.Fatalf("restoreFile did not restore content: %q", string(data))
	}

	// A snapshot of a missing file restores by removing the file.
	missing := snapshotFile(filepath.Join(dir, "absent.json"))
	if missing.Existed {
		t.Fatal("snapshot of missing file should not report Existed")
	}
	restoreFile(path, missing)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("restoreFile of missing snapshot should remove file, stat err = %v", err)
	}
}

func TestInstallRollbackTracksAndRestoresFiles(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.json")
	if err := os.WriteFile(existing, []byte("before"), 0640); err != nil {
		t.Fatalf("write existing fixture: %v", err)
	}
	created := filepath.Join(dir, "created.json")

	rollback := newInstallRollback(service.Manager{UserMode: true})
	rollback.Track(existing)
	rollback.Track(created)
	rollback.Track("")       // ignored
	rollback.Track(existing) // de-duplicated

	if err := os.WriteFile(existing, []byte("after"), 0640); err != nil {
		t.Fatalf("mutate existing: %v", err)
	}
	if err := os.WriteFile(created, []byte("new"), 0640); err != nil {
		t.Fatalf("write created: %v", err)
	}

	rollback.Rollback(Manifest{})

	data, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("read restored existing: %v", err)
	}
	if string(data) != "before" {
		t.Fatalf("existing file not restored to original: %q", string(data))
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("newly created file should be removed on rollback, stat err = %v", err)
	}
}

func TestDestinationStatusAndInstallDestinationReportEnabledHEC(t *testing.T) {
	cfg := endpointconfig.Config{
		Destinations: &endpointconfig.Destinations{
			SplunkHEC: &endpointconfig.SplunkHEC{
				Enabled:    true,
				Endpoint:   "https://splunk.example/services/collector",
				Index:      "main",
				Source:     "beacon",
				Sourcetype: "beacon:endpoint",
			},
			FalconHEC: &endpointconfig.FalconHEC{
				Enabled:  true,
				Endpoint: "https://falcon.example/services/collector",
			},
		},
	}

	status := destinationStatus(cfg)
	if !status.SplunkHEC.Configured || status.SplunkHEC.Endpoint != "https://splunk.example/services/collector" {
		t.Fatalf("Splunk destination status not reported: %#v", status.SplunkHEC)
	}
	if !status.FalconHEC.Configured {
		t.Fatalf("Falcon destination status not reported: %#v", status.FalconHEC)
	}

	info := installDestination(cfg)
	if !strings.Contains(info.Type, "splunk_hec") || !strings.Contains(info.Type, "falcon_hec") {
		t.Fatalf("installDestination type missing HEC destinations: %q", info.Type)
	}
	if !strings.Contains(info.Mode, "hec") {
		t.Fatalf("installDestination mode missing hec: %q", info.Mode)
	}

	// With no destinations configured, status is empty and only local JSONL is reported.
	empty := destinationStatus(endpointconfig.Config{})
	if empty.SplunkHEC.Configured || empty.FalconHEC.Configured {
		t.Fatalf("empty config should report no destinations: %#v", empty)
	}
	localOnly := installDestination(endpointconfig.Config{})
	if localOnly.Type != "local_jsonl" {
		t.Fatalf("installDestination with no HEC = %q, want local_jsonl", localOnly.Type)
	}
}

func TestGetStatusAndResolveRuntimeLogReportRequestedConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logPath := filepath.Join(home, "runtime.jsonl")

	source := ResolveRuntimeLog(true, logPath)
	if source.EffectiveLogPath != logPath || source.Reason != "explicit runtime log path" {
		t.Fatalf("ResolveRuntimeLog explicit path = %#v", source)
	}

	status := GetStatus(true, logPath)
	if status.LogPath != logPath {
		t.Fatalf("GetStatus LogPath = %q, want %q", status.LogPath, logPath)
	}
	if status.ConfigPath != endpointconfig.ConfigPath(true) {
		t.Fatalf("GetStatus ConfigPath = %q, want %q", status.ConfigPath, endpointconfig.ConfigPath(true))
	}
	if status.Version == "" {
		t.Fatal("GetStatus should populate Version")
	}
}
