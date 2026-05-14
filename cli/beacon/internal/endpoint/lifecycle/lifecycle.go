package lifecycle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	endpointcollector "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/collector"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

type InstallOptions struct {
	UserMode         bool
	LogPath          string
	Harnesses        []string
	GRPCPort         int
	HTTPPort         int
	CollectorPath    string
	StartService     bool
	ContentRetention endpointconfig.ContentRetention
}

type UninstallOptions struct {
	UserMode   bool
	LogPath    string
	KeepLogs   bool
	KeepConfig bool
}

type InstallResult struct {
	ConfigPath          string   `json:"config_path"`
	CollectorConfigPath string   `json:"collector_config_path"`
	PlistPath           string   `json:"plist_path"`
	LogPath             string   `json:"log_path"`
	ManifestPath        string   `json:"manifest_path"`
	HarnessConfigPaths  []string `json:"harness_config_paths,omitempty"`
}

type Status struct {
	Version     string                   `json:"version"`
	ConfigPath  string                   `json:"config_path"`
	LogPath     string                   `json:"log_path"`
	Collector   endpointcollector.Status `json:"collector"`
	Service     service.Status           `json:"service"`
	Harnesses   []harness.Harness        `json:"harnesses"`
	Diagnostics []diagnostics.Check      `json:"diagnostics"`
	LastEvent   string                   `json:"last_event,omitempty"`
}

type Manifest struct {
	CreatedAt      string   `json:"created_at"`
	UserMode       bool     `json:"user_mode"`
	Files          []string `json:"files"`
	Backups        []string `json:"backups,omitempty"`
	HarnessConfigs []string `json:"harness_configs,omitempty"`
	ServiceLabel   string   `json:"service_label"`
	LogPath        string   `json:"log_path"`
}

func Install(opts InstallOptions) (InstallResult, error) {
	cfg := buildConfig(opts)
	if err := preflight(cfg, opts.StartService); err != nil {
		return InstallResult{}, err
	}
	manager := service.Manager{UserMode: cfg.UserMode}
	collectorBinary := endpointcollector.DiscoverBinary(cfg.Collector.BinaryPath)
	if collectorBinary == "" {
		collectorBinary = cfg.Collector.BinaryPath
	}
	if collectorBinary == "" {
		collectorBinary = filepath.Join(endpointconfig.BaseDir(cfg.UserMode), "bin", "beacon-otelcol")
	}

	manifest := Manifest{
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		UserMode:     cfg.UserMode,
		ServiceLabel: manager.Label(),
		LogPath:      cfg.LogPath,
	}
	if err := endpointcollector.WriteConfig(cfg); err != nil {
		return InstallResult{}, err
	}
	manifest.Files = append(manifest.Files, cfg.Collector.ConfigPath)
	plistPath, err := manager.WritePlist(collectorBinary, cfg.Collector.ConfigPath)
	if err != nil {
		rollback(manifest)
		return InstallResult{}, err
	}
	manifest.Files = append(manifest.Files, plistPath)
	configPath, err := endpointconfig.Save(cfg)
	if err != nil {
		rollback(manifest)
		return InstallResult{}, err
	}
	manifest.Files = append(manifest.Files, configPath)
	harnessPaths, err := configureHarnesses(cfg)
	if err != nil {
		rollback(manifest)
		return InstallResult{}, err
	}
	manifest.HarnessConfigs = harnessPaths
	manifest.Backups = discoverBackups(harnessPaths)
	if opts.StartService {
		if err := manager.Load(); err != nil {
			rollback(manifest)
			return InstallResult{}, err
		}
	}
	manifestPath, err := writeManifest(cfg.UserMode, manifest)
	if err != nil {
		rollback(manifest)
		return InstallResult{}, err
	}
	event := schema.NewEvent(schema.NewEventOptions{
		Action:       "telemetry.enabled",
		Category:     "telemetry",
		Severity:     schema.SeverityInfo,
		AgentVersion: version.GetVersion(),
		Harness:      schema.HarnessInfo{Name: "endpoint"},
		Message:      "Beacon endpoint local telemetry configured",
	})
	event.Destination = &schema.DestinationInfo{Type: "wazuh", Mode: "localfile", Status: "configured"}
	if _, err := writer.AppendEvent(event, writer.Options{Path: cfg.LogPath, UserMode: cfg.UserMode}); err != nil {
		return InstallResult{}, err
	}
	return InstallResult{
		ConfigPath:          configPath,
		CollectorConfigPath: cfg.Collector.ConfigPath,
		PlistPath:           plistPath,
		LogPath:             cfg.LogPath,
		ManifestPath:        manifestPath,
		HarnessConfigPaths:  harnessPaths,
	}, nil
}

func Uninstall(opts UninstallOptions) error {
	cfg := loadOrDefault(opts.UserMode, opts.LogPath)
	manager := service.Manager{UserMode: cfg.UserMode}
	_ = manager.Unload()
	manifest, _ := ReadManifest(cfg.UserMode)
	if !opts.KeepConfig {
		restoreBackups(manifest.Backups)
	}
	for _, path := range manifest.Files {
		_ = os.Remove(path)
	}
	if len(manifest.Files) == 0 {
		if path, err := manager.PlistPath(); err == nil {
			_ = os.Remove(path)
		}
		_ = os.Remove(cfg.Collector.ConfigPath)
		_ = os.Remove(endpointconfig.ConfigPath(cfg.UserMode))
	}
	if !opts.KeepLogs {
		_ = os.Remove(cfg.LogPath)
	}
	_ = os.Remove(manifestPath(cfg.UserMode))
	return nil
}

func Repair(opts InstallOptions) (InstallResult, error) {
	_ = Uninstall(UninstallOptions{UserMode: opts.UserMode, LogPath: opts.LogPath, KeepLogs: true, KeepConfig: true})
	return Install(opts)
}

func GetStatus(userMode bool, logPath string) Status {
	cfg := loadOrDefault(userMode, logPath)
	last, _ := writer.LastLine(cfg.LogPath)
	return Status{
		Version:     version.GetVersion(),
		ConfigPath:  endpointconfig.ConfigPath(cfg.UserMode),
		LogPath:     cfg.LogPath,
		Collector:   endpointcollector.CheckStatus(cfg),
		Service:     service.Manager{UserMode: cfg.UserMode}.Status(),
		Harnesses:   harness.DiscoverAll(),
		Diagnostics: diagnostics.Run(cfg),
		LastEvent:   last,
	}
}

func buildConfig(opts InstallOptions) endpointconfig.Config {
	logPath := opts.LogPath
	if logPath == "" {
		logPath = writer.DefaultPath(opts.UserMode)
	}
	cfg := endpointconfig.Default(opts.UserMode, logPath)
	if len(opts.Harnesses) > 0 {
		cfg.Harnesses = opts.Harnesses
	}
	if opts.GRPCPort != 0 {
		cfg.Collector.GRPCPort = opts.GRPCPort
	}
	if opts.HTTPPort != 0 {
		cfg.Collector.HTTPPort = opts.HTTPPort
	}
	cfg.Collector.BinaryPath = opts.CollectorPath
	if opts.ContentRetention != "" {
		cfg.ContentRetention = opts.ContentRetention
	}
	return cfg
}

func loadOrDefault(userMode bool, logPath string) endpointconfig.Config {
	if cfg, err := endpointconfig.Load(userMode); err == nil {
		if logPath != "" {
			cfg.LogPath = logPath
		}
		return cfg
	}
	if logPath == "" {
		logPath = writer.DefaultPath(userMode)
	}
	return endpointconfig.Default(userMode, logPath)
}

func preflight(cfg endpointconfig.Config, startService bool) error {
	if err := endpointconfig.ValidateContentRetention(cfg.ContentRetention); err != nil {
		return err
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("production endpoint install is currently supported only on macOS")
	}
	if !cfg.UserMode && os.Geteuid() != 0 {
		return fmt.Errorf("system install requires root; rerun with sudo or use --user")
	}
	if !startService {
		return nil
	}
	if !endpointcollector.PortAvailable(cfg.Collector.GRPCPort) {
		return fmt.Errorf("OTLP gRPC port %d is already in use", cfg.Collector.GRPCPort)
	}
	if !endpointcollector.PortAvailable(cfg.Collector.HTTPPort) {
		return fmt.Errorf("OTLP HTTP port %d is already in use", cfg.Collector.HTTPPort)
	}
	return nil
}

func configureHarnesses(cfg endpointconfig.Config) ([]string, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", cfg.Collector.GRPCPort)
	var paths []string
	for _, name := range cfg.Harnesses {
		switch name {
		case "claude", "claude_code":
			path, err := harness.ConfigureClaude(harness.ConfigureOptions{Endpoint: endpoint, UserMode: cfg.UserMode})
			if err != nil {
				return paths, err
			}
			paths = append(paths, path)
		case "codex", "codex_cli":
			path, err := harness.ConfigureCodex(harness.ConfigureOptions{Endpoint: endpoint, UserMode: cfg.UserMode})
			if err != nil {
				return paths, err
			}
			paths = append(paths, path)
		case "":
		default:
			return paths, fmt.Errorf("unsupported harness %q", name)
		}
	}
	return paths, nil
}

func discoverBackups(paths []string) []string {
	var backups []string
	for _, path := range paths {
		matches, _ := filepath.Glob(path + ".beacon.*.bak")
		backups = append(backups, matches...)
		if _, err := os.Stat(path + ".beacon.bak"); err == nil {
			backups = append(backups, path+".beacon.bak")
		}
	}
	return backups
}

func restoreBackups(backups []string) {
	for _, backup := range backups {
		target := restoreTarget(backup)
		if target == "" {
			continue
		}
		data, err := os.ReadFile(backup)
		if err == nil {
			_ = os.WriteFile(target, data, 0600)
		}
	}
}

func writeManifest(userMode bool, manifest Manifest) (string, error) {
	path := manifestPath(userMode)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0600)
}

func ReadManifest(userMode bool) (Manifest, error) {
	data, err := os.ReadFile(manifestPath(userMode))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func manifestPath(userMode bool) string {
	return filepath.Join(endpointconfig.BaseDir(userMode), "install-manifest.json")
}

func rollback(manifest Manifest) {
	restoreBackups(manifest.Backups)
	for i := len(manifest.Files) - 1; i >= 0; i-- {
		_ = os.Remove(manifest.Files[i])
	}
}

func restoreTarget(backup string) string {
	for _, suffix := range []string{".beacon.bak", ".beacon."} {
		for i := len(backup) - len(suffix); i >= 0; i-- {
			if i+len(suffix) <= len(backup) && backup[i:i+len(suffix)] == suffix {
				return backup[:i]
			}
		}
	}
	return ""
}
