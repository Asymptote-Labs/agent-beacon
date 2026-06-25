package lifecycle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	beaconauth "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/auth"
	endpointcollector "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/collector"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/ingest"
	endpointingest "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/ingest/endpoint"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

var (
	writeCollectorConfig = endpointcollector.WriteConfig
	saveEndpointConfig   = endpointconfig.Save
	appendInstallEvent   = writer.AppendEvent
	detectUpdaterInstall = selfupdate.DetectInstall
	removeUpdaterJob     = func() {
		updater := service.UpdaterManager{}
		_ = updater.Unload()
		_ = os.Remove(updater.PlistPath())
	}
)

type InstallOptions struct {
	UserMode              bool
	LogPath               string
	Harnesses             []string
	GRPCPort              int
	HTTPPort              int
	CollectorPath         string
	StartService          bool
	IncludeRuntimeMetrics bool
	IncludeCodexSpans     bool
	SplunkHEC             *endpointconfig.SplunkHEC
	FalconHEC             *endpointconfig.FalconHEC
}

type UninstallOptions struct {
	UserMode    bool
	LogPath     string
	KeepLogs    bool
	KeepConfig  bool
	KeepUpdater bool
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
	Version       string                   `json:"version"`
	ConfigPath    string                   `json:"config_path"`
	LogPath       string                   `json:"log_path"`
	RuntimeLog    RuntimeLogSource         `json:"runtime_log"`
	Collector     endpointcollector.Status `json:"collector"`
	Service       service.Status           `json:"service"`
	Harnesses     []harness.Harness        `json:"harnesses"`
	Diagnostics   []diagnostics.Check      `json:"diagnostics"`
	LastEvent     string                   `json:"last_event,omitempty"`
	Destinations  DestinationStatus        `json:"destinations"`
	ManagedUpload ingest.State             `json:"managed_upload"`
}

type DestinationStatus struct {
	SplunkHEC ConfiguredStatus `json:"splunk_hec"`
	FalconHEC ConfiguredStatus `json:"falcon_hec"`
}

type ConfiguredStatus struct {
	Configured bool   `json:"configured"`
	Endpoint   string `json:"endpoint,omitempty"`
	Index      string `json:"index,omitempty"`
	Source     string `json:"source,omitempty"`
	Sourcetype string `json:"sourcetype,omitempty"`
}

type RuntimeLogSource struct {
	RequestedUserMode bool   `json:"requested_user_mode"`
	EffectiveUserMode bool   `json:"effective_user_mode"`
	RequestedLogPath  string `json:"requested_log_path"`
	EffectiveLogPath  string `json:"effective_log_path"`
	Reason            string `json:"reason,omitempty"`
	Warning           string `json:"warning,omitempty"`
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

type fileSnapshot struct {
	Existed bool
	Data    []byte
	Mode    os.FileMode
}

type installRollback struct {
	Manager       service.Manager
	ServiceLoaded bool
	files         []string
	snapshots     map[string]fileSnapshot
}

func newInstallRollback(manager service.Manager) *installRollback {
	return &installRollback{
		Manager:   manager,
		snapshots: map[string]fileSnapshot{},
	}
}

func (r *installRollback) Track(path string) {
	if r == nil || path == "" {
		return
	}
	if _, ok := r.snapshots[path]; ok {
		return
	}
	r.snapshots[path] = snapshotFile(path)
	r.files = append(r.files, path)
}

func (r *installRollback) Rollback(manifest Manifest) {
	if r == nil {
		rollback(manifest)
		return
	}
	if r.ServiceLoaded {
		_ = r.Manager.Unload()
	}
	restoreBackups(manifest.Backups)
	for i := len(r.files) - 1; i >= 0; i-- {
		path := r.files[i]
		restoreFile(path, r.snapshots[path])
	}
}

func Install(opts InstallOptions) (InstallResult, error) {
	cfg := buildConfig(opts)
	if err := preflight(cfg, opts.StartService); err != nil {
		return InstallResult{}, err
	}
	manager := service.Manager{UserMode: cfg.UserMode}
	collectorBinary, err := endpointcollector.ResolveBinary(cfg.Collector.BinaryPath)
	if err != nil {
		return InstallResult{}, err
	}

	manifest := Manifest{
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		UserMode:     cfg.UserMode,
		ServiceLabel: manager.Label(),
		LogPath:      cfg.LogPath,
	}

	tx := newInstallRollback(manager)
	tx.Track(cfg.Collector.ConfigPath)
	manifest.Files = append(manifest.Files, cfg.Collector.ConfigPath)
	if err := writeCollectorConfig(cfg); err != nil {
		tx.Rollback(manifest)
		return InstallResult{}, err
	}

	plistPath, err := manager.PlistPath()
	if err != nil {
		tx.Rollback(manifest)
		return InstallResult{}, err
	}
	tx.Track(plistPath)
	manifest.Files = append(manifest.Files, plistPath)
	if _, err := manager.WritePlist(collectorBinary, cfg.Collector.ConfigPath); err != nil {
		tx.Rollback(manifest)
		return InstallResult{}, err
	}

	configPath := endpointconfig.ConfigPath(cfg.UserMode)
	tx.Track(configPath)
	manifest.Files = append(manifest.Files, configPath)
	if _, err := saveEndpointConfig(cfg); err != nil {
		tx.Rollback(manifest)
		return InstallResult{}, err
	}

	harnessPaths, err := configureHarnesses(cfg)
	manifest.HarnessConfigs = harnessPaths
	manifest.Backups = discoverBackups(harnessPaths)
	if err != nil {
		tx.Rollback(manifest)
		return InstallResult{}, err
	}
	if opts.StartService {
		if err := manager.Load(); err != nil {
			tx.Rollback(manifest)
			return InstallResult{}, err
		}
		tx.ServiceLoaded = true
		if err := endpointcollector.WaitUntilReady(cfg, 10*time.Second); err != nil {
			tx.Rollback(manifest)
			return InstallResult{}, err
		}
	}
	tx.Track(manifestPath(cfg.UserMode))
	manifestPath, err := writeManifest(cfg.UserMode, manifest)
	if err != nil {
		tx.Rollback(manifest)
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
	event.Destination = installDestination(cfg)
	if _, err := appendInstallEvent(event, writer.Options{Path: cfg.LogPath, UserMode: cfg.UserMode}); err != nil {
		tx.Rollback(manifest)
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
	if !cfg.UserMode && !opts.KeepUpdater {
		removeUpdaterJob()
	}
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
	configPath := endpointconfig.ConfigPath(opts.UserMode)
	configSnapshot := snapshotFile(configPath)
	priorAutoUpdateMode := ""
	if !opts.UserMode {
		if mode, err := autoUpdateModeFromConfigFile(configPath); err == nil {
			priorAutoUpdateMode = mode
		}
	}
	_ = Uninstall(UninstallOptions{UserMode: opts.UserMode, LogPath: opts.LogPath, KeepLogs: true, KeepConfig: true, KeepUpdater: true})
	result, err := Install(opts)
	if err != nil {
		restoreFile(configPath, configSnapshot)
		return result, err
	}
	if !opts.UserMode {
		if priorAutoUpdateMode != "" {
			if err := setAutoUpdateModeInConfigFile(configPath, priorAutoUpdateMode); err != nil {
				return result, err
			}
		}
		if err := reconcileUpdaterFromConfig(opts.LogPath); err != nil {
			return result, err
		}
	}
	return result, nil
}

func reconcileUpdaterFromConfig(logPath string) error {
	if !detectUpdaterInstall().SupportsSeamlessUpdate() {
		return nil
	}
	localMode := ""
	if mode, err := autoUpdateModeFromConfigFile(endpointconfig.ConfigPath(false)); err == nil {
		localMode = mode
	}
	mode := selfupdate.ResolveMode(localMode)
	mgr := service.UpdaterManager{}
	if mode == selfupdate.ModeOff {
		_ = mgr.Unload()
		_ = os.Remove(mgr.PlistPath())
		return nil
	}
	if _, err := mgr.WritePlist(selfupdate.SystemBeaconPath()); err != nil {
		return err
	}
	return mgr.Load()
}

func autoUpdateModeFromConfigFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var partial struct {
		AutoUpdate *endpointconfig.AutoUpdate `json:"auto_update"`
	}
	if err := json.Unmarshal(data, &partial); err != nil {
		return "", err
	}
	if partial.AutoUpdate == nil {
		return "", nil
	}
	return partial.AutoUpdate.Mode, nil
}

func setAutoUpdateModeInConfigFile(path, mode string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	autoUpdate, err := json.Marshal(endpointconfig.AutoUpdate{Mode: mode})
	if err != nil {
		return err
	}
	raw["auto_update"] = autoUpdate
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	perm := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	return os.WriteFile(path, out, perm)
}

func GetStatus(userMode bool, logPath string) Status {
	cfg := loadOrDefault(userMode, logPath)
	runtimeLog := ResolveRuntimeLog(userMode, logPath)
	effectiveCfg := cfg
	effectiveCfg.UserMode = runtimeLog.EffectiveUserMode
	if runtimeLog.EffectiveUserMode != cfg.UserMode {
		effectiveCfg = loadOrDefault(runtimeLog.EffectiveUserMode, runtimeLog.EffectiveLogPath)
	}
	effectiveCfg.LogPath = runtimeLog.EffectiveLogPath
	last, _ := writer.LastLine(effectiveCfg.LogPath)
	checks := diagnostics.Run(effectiveCfg)
	if runtimeLog.Warning != "" {
		checks = append(checks, diagnostics.Check{
			Name:     "runtime_log_source",
			Status:   "warn",
			Severity: "medium",
			Message:  runtimeLog.Warning,
		})
	}
	creds, _ := beaconauth.LoadCredentials()
	managedUpload := ingest.Status(
		endpointingest.Settings(effectiveCfg, effectiveCfg.LogPath, effectiveCfg.UserMode),
		endpointingest.Store(effectiveCfg.UserMode),
		creds,
	)
	return Status{
		Version:       version.GetVersion(),
		ConfigPath:    endpointconfig.ConfigPath(effectiveCfg.UserMode),
		LogPath:       effectiveCfg.LogPath,
		RuntimeLog:    runtimeLog,
		Collector:     endpointcollector.CheckStatus(effectiveCfg),
		Service:       service.Manager{UserMode: effectiveCfg.UserMode}.Status(),
		Harnesses:     harness.DiscoverAll(),
		Diagnostics:   checks,
		LastEvent:     last,
		Destinations:  destinationStatus(effectiveCfg),
		ManagedUpload: managedUpload,
	}
}

func ResolveRuntimeLog(userMode bool, logPath string) RuntimeLogSource {
	cfg := loadOrDefault(userMode, logPath)
	source := RuntimeLogSource{
		RequestedUserMode: userMode,
		EffectiveUserMode: userMode,
		RequestedLogPath:  cfg.LogPath,
		EffectiveLogPath:  cfg.LogPath,
		Reason:            "requested endpoint configuration",
	}
	if logPath != "" {
		source.Reason = "explicit runtime log path"
		return source
	}
	if !userMode {
		return source
	}
	systemCfg, err := endpointconfig.Load(false)
	if err != nil || !sameCollectorPorts(cfg, systemCfg) || systemCfg.LogPath == "" || systemCfg.LogPath == cfg.LogPath {
		return source
	}
	return selectRuntimeLog(source, service.Manager{UserMode: true}.Status(), service.Manager{UserMode: false}.Status(), systemCfg)
}

func selectRuntimeLog(source RuntimeLogSource, requestedService, systemService service.Status, systemCfg endpointconfig.Config) RuntimeLogSource {
	if systemService.Running && !requestedService.Running {
		source.Reason = "requested endpoint configuration; system collector is also running on the configured OTLP ports"
		source.EffectiveUserMode = false
		source.EffectiveLogPath = systemCfg.LogPath
		source.Warning = fmt.Sprintf("system collector is writing OTLP events to %s instead of the user runtime log %s; stop the system collector or install user mode to keep all events in one file", systemCfg.LogPath, source.RequestedLogPath)
	}
	return source
}

func sameCollectorPorts(left, right endpointconfig.Config) bool {
	return left.Collector.GRPCPort == right.Collector.GRPCPort && left.Collector.HTTPPort == right.Collector.HTTPPort
}

func buildConfig(opts InstallOptions) endpointconfig.Config {
	logPath := opts.LogPath
	if logPath == "" {
		logPath = writer.DefaultPath(opts.UserMode)
	}
	cfg := endpointconfig.Default(opts.UserMode, logPath)
	if mode, err := autoUpdateModeFromConfigFile(endpointconfig.ConfigPath(opts.UserMode)); err == nil && mode != "" {
		cfg.AutoUpdate = &endpointconfig.AutoUpdate{Mode: mode}
	}
	if opts.Harnesses != nil {
		cfg.Harnesses = opts.Harnesses
	}
	if opts.GRPCPort != 0 {
		cfg.Collector.GRPCPort = opts.GRPCPort
	}
	if opts.HTTPPort != 0 {
		cfg.Collector.HTTPPort = opts.HTTPPort
	}
	cfg.Collector.BinaryPath = opts.CollectorPath
	cfg.Collector.IncludeRuntimeMetrics = opts.IncludeRuntimeMetrics
	cfg.Collector.IncludeCodexSpans = opts.IncludeCodexSpans
	if opts.SplunkHEC != nil {
		if cfg.Destinations == nil {
			cfg.Destinations = &endpointconfig.Destinations{}
		}
		cfg.Destinations.SplunkHEC = opts.SplunkHEC
	}
	if opts.FalconHEC != nil {
		if cfg.Destinations == nil {
			cfg.Destinations = &endpointconfig.Destinations{}
		}
		cfg.Destinations.FalconHEC = opts.FalconHEC
	}
	if cfg.Destinations != nil {
		endpointconfig.NormalizeDestinations(&cfg)
	}
	return cfg
}

func installDestination(cfg endpointconfig.Config) *schema.DestinationInfo {
	destination := &schema.DestinationInfo{Type: "local_jsonl", Mode: "file", Status: "configured"}
	if cfg.Destinations != nil && cfg.Destinations.SplunkHEC != nil && cfg.Destinations.SplunkHEC.Enabled {
		destination.Type += ",splunk_hec"
		destination.Mode += ",hec"
	}
	if cfg.Destinations != nil && cfg.Destinations.FalconHEC != nil && cfg.Destinations.FalconHEC.Enabled {
		destination.Type += ",falcon_hec"
		destination.Mode += ",hec"
	}
	return destination
}

func destinationStatus(cfg endpointconfig.Config) DestinationStatus {
	status := DestinationStatus{}
	if cfg.Destinations == nil {
		return status
	}
	if cfg.Destinations.SplunkHEC != nil && cfg.Destinations.SplunkHEC.Enabled {
		splunk := cfg.Destinations.SplunkHEC
		status.SplunkHEC = ConfiguredStatus{
			Configured: true,
			Endpoint:   splunk.Endpoint,
			Index:      splunk.Index,
			Source:     splunk.Source,
			Sourcetype: splunk.Sourcetype,
		}
	}
	if cfg.Destinations.FalconHEC != nil && cfg.Destinations.FalconHEC.Enabled {
		falcon := cfg.Destinations.FalconHEC
		status.FalconHEC = ConfiguredStatus{
			Configured: true,
			Endpoint:   falcon.Endpoint,
			Index:      falcon.Index,
			Source:     falcon.Source,
			Sourcetype: falcon.Sourcetype,
		}
	}
	return status
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

func snapshotFile(path string) fileSnapshot {
	snapshot := fileSnapshot{}
	if data, err := os.ReadFile(path); err == nil {
		snapshot.Existed = true
		snapshot.Data = data
		if info, statErr := os.Stat(path); statErr == nil {
			snapshot.Mode = info.Mode().Perm()
		}
	}
	return snapshot
}

func restoreFile(path string, snapshot fileSnapshot) {
	if !snapshot.Existed {
		_ = os.Remove(path)
		return
	}
	mode := snapshot.Mode
	if mode == 0 {
		mode = 0600
	}
	_ = os.WriteFile(path, snapshot.Data, mode)
}

func preflight(cfg endpointconfig.Config, startService bool) error {
	if err := endpointconfig.ValidateDestinations(cfg.Destinations); err != nil {
		return err
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("production endpoint install is currently supported only on macOS")
	}
	if !cfg.UserMode && os.Geteuid() != 0 {
		return fmt.Errorf("system install requires root; rerun with sudo or omit --system for the default user install")
	}
	if !startService {
		return nil
	}
	if !endpointcollector.PortAvailable(cfg.Collector.GRPCPort) {
		if !existingCollectorReady(cfg) {
			return fmt.Errorf("OTLP gRPC port %d is already in use", cfg.Collector.GRPCPort)
		}
	}
	if !endpointcollector.PortAvailable(cfg.Collector.HTTPPort) {
		if !existingCollectorReady(cfg) {
			return fmt.Errorf("OTLP HTTP port %d is already in use", cfg.Collector.HTTPPort)
		}
	}
	return nil
}

func existingCollectorReady(cfg endpointconfig.Config) bool {
	if !(service.Manager{UserMode: cfg.UserMode}).Status().Loaded {
		return false
	}
	status := endpointcollector.CheckStatus(cfg)
	return status.GRPCReady && status.HTTPReady && status.HealthReady
}

func configureHarnesses(cfg endpointconfig.Config) ([]string, error) {
	grpcEndpoint := fmt.Sprintf("http://127.0.0.1:%d", cfg.Collector.GRPCPort)
	httpEndpoint := fmt.Sprintf("http://127.0.0.1:%d", cfg.Collector.HTTPPort)
	var paths []string
	for _, name := range cfg.Harnesses {
		switch name {
		case "claude", "claude_code":
			path, err := harness.ConfigureClaude(harness.ConfigureOptions{Endpoint: grpcEndpoint, UserMode: cfg.UserMode})
			if err != nil {
				return paths, err
			}
			paths = append(paths, path)
		case "codex", "codex_cli":
			path, err := harness.ConfigureCodex(harness.ConfigureOptions{Endpoint: grpcEndpoint, UserMode: cfg.UserMode})
			if err != nil {
				return paths, err
			}
			paths = append(paths, path)
		case "gemini", "gemini_cli":
			path, err := harness.ConfigureGemini(harness.ConfigureOptions{Endpoint: grpcEndpoint, UserMode: cfg.UserMode})
			if err != nil {
				return paths, err
			}
			paths = append(paths, path)
		case "vscode", "vs_code", "vscode_copilot":
			path, err := harness.ConfigureVSCode(harness.VSCodeConfigOptions{Endpoint: httpEndpoint})
			if err != nil {
				return paths, err
			}
			paths = append(paths, path)
		case "opencode":
			return paths, fmt.Errorf("opencode telemetry is installed with `beacon endpoint hooks install --harness opencode`, not endpoint install")
		case "copilot", "copilot_cli", "github_copilot":
			return paths, fmt.Errorf("Copilot CLI telemetry is MDM-managed; set COPILOT_OTEL_ENABLED=true and OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:%d in the Copilot CLI launch environment instead of using --harness %s", cfg.Collector.HTTPPort, name)
		case "factory", "droid":
			return paths, fmt.Errorf("Factory Droid telemetry is MDM-managed; set OTEL_TELEMETRY_ENDPOINT=http://127.0.0.1:%d in the Droid launch environment instead of using --harness %s", cfg.Collector.HTTPPort, name)
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
