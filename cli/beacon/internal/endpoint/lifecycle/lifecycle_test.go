package lifecycle

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
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
		Harnesses:     []string{""},
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
		Harnesses:     []string{""},
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

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
