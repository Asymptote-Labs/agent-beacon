package lifecycle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	endpointcollector "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/collector"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
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
		updater.RemoveUnits()
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
	// ServiceKind selects the service manager. Empty auto-detects: launchd on macOS,
	// systemd when it is PID 1, otherwise a supervised child process.
	ServiceKind service.Kind
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
	// The linger fields report the optional logout-persistence step, so a caller can tell a
	// fully persistent install from one that collects only until this user logs out. Flat rather
	// than nested, matching how Manifest already records the same outcome.
	LingerApplicable  bool   `json:"linger_applicable,omitempty"`
	LingerEnabled     bool   `json:"linger_enabled,omitempty"`
	LingerDetail      string `json:"linger_detail,omitempty"`
	LingerRemediation string `json:"linger_remediation,omitempty"`
}

type Status struct {
	Version       string                        `json:"version"`
	ConfigPath    string                        `json:"config_path"`
	LogPath       string                        `json:"log_path"`
	RuntimeLog    RuntimeLogSource              `json:"runtime_log"`
	Collector     endpointcollector.Status      `json:"collector"`
	Service       service.Status                `json:"service"`
	Harnesses     []harness.Harness             `json:"harnesses"`
	Diagnostics   []diagnostics.Check           `json:"diagnostics"`
	LastEvent     string                        `json:"last_event,omitempty"`
	Destinations  DestinationStatus             `json:"destinations"`
	ManagedIngest asymptote.ManagedIngestStatus `json:"managed_ingest"`
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
	// LingerEnabled and LingerDetail record whether a systemd --user unit was made to survive
	// logout. Recorded rather than silently attempted, so status and doctor can report the gap
	// instead of the user discovering it after their next logout. Absent on other backends.
	LingerEnabled bool   `json:"linger_enabled,omitempty"`
	LingerDetail  string `json:"linger_detail,omitempty"`
}

type fileSnapshot struct {
	Existed bool
	Data    []byte
	Mode    os.FileMode
}

// serviceController is the part of service.Manager that rollback uses.
//
// Narrow on purpose: rollback's decision about a running service is the branch worth testing, and
// without a seam here it can only be reached by driving a real install to fail against a real
// service manager. service.Manager satisfies this as-is.
type serviceController interface {
	Unload() error
	Load() error
}

type installRollback struct {
	Manager       serviceController
	ServiceLoaded bool
	// ServiceWasRunning records whether a collector was already up when this install began.
	//
	// It decides what rollback owes the machine. Unloading is right for a service this transaction
	// created -- leaving a half-installed endpoint registered would be worse. It is wrong for one
	// that was already running: a failed upgrade of a healthy endpoint would then stop and disable
	// it, and on systemd the disable survives reboot, so a machine that was collecting fine before
	// the attempt is left collecting nothing afterwards.
	ServiceWasRunning bool
	files             []string
	snapshots         map[string]fileSnapshot
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
	// Stop first, always, and while the service state on disk still describes the process this
	// install started.
	//
	// Ordering is the whole of it. The supervised backend's "unit file" *is* its pidfile, and that
	// file is one of the tracked files below -- so restoring it puts back the pid from before the
	// install, which by then names a process that has already been terminated. Unloading after the
	// restore would therefore look up a dead pid, stop nothing, and leave the collector this failed
	// install started running on the new config, holding the ports, with a second one spawned
	// beside it.
	if r.ServiceLoaded {
		_ = r.Manager.Unload()
	}

	restoreBackups(manifest.Backups)
	for i := len(r.files) - 1; i >= 0; i-- {
		path := r.files[i]
		restoreFile(path, r.snapshots[path])
	}

	// Bring back a service that was running before this install, now that the configuration it was
	// running is back on disk. A collector reads its config at startup, so this has to come after
	// the restore to be worth doing at all.
	//
	// Without it a failed reinstall of a healthy endpoint would leave the machine with the service
	// stopped and, on systemd, disabled -- surviving reboot. The upgrade would not merely fail, it
	// would end collection. A service this install created has no such claim: nothing was running
	// before, and leaving a half-installed endpoint registered is worse than leaving it absent.
	//
	// Best-effort. If it does not come back, the endpoint is no worse off than the failure that
	// brought us here, and the install error the caller receives is the more useful signal.
	if r.ServiceLoaded && r.ServiceWasRunning {
		_ = r.Manager.Load()
	}
}

func Install(opts InstallOptions) (InstallResult, error) {
	cfg := buildConfig(opts)
	if err := preflight(cfg, opts.StartService, opts.ServiceKind); err != nil {
		return InstallResult{}, err
	}
	manager := service.Manager{UserMode: cfg.UserMode, Kind: opts.ServiceKind}
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

	// Make the log directory writable by the people whose sessions this endpoint captures, before
	// anything writes to it.
	//
	// A no-op on POSIX, where the file mode carries the same guarantee. On Windows a system-mode
	// log lives under %ProgramData%, which denies ordinary users write access -- and hooks run as
	// the console user. Skipping this produces an install where every visible signal is healthy
	// (the service runs, status and doctor agree) and nothing the agent does is ever recorded,
	// because all of those describe the collector rather than the hooks exporting to it.
	//
	// Only for system mode: a user-mode log lives in that user's own profile, which they can
	// already write, and widening its ACL would hand their prompt text to every interactive
	// account on the machine.
	if !cfg.UserMode {
		if err := writer.EnsureSystemLogWritable(filepath.Dir(cfg.LogPath)); err != nil {
			return InstallResult{}, fmt.Errorf("prepare the log directory: %w", err)
		}
	}

	tx := newInstallRollback(manager)
	tx.Track(cfg.Collector.ConfigPath)
	manifest.Files = append(manifest.Files, cfg.Collector.ConfigPath)
	if err := writeCollectorConfig(cfg); err != nil {
		tx.Rollback(manifest)
		return InstallResult{}, err
	}

	plistPath, err := manager.UnitPath()
	if err != nil {
		tx.Rollback(manifest)
		return InstallResult{}, err
	}
	tx.Track(plistPath)
	manifest.Files = append(manifest.Files, plistPath)
	if _, err := manager.WriteUnit(collectorBinary, cfg.Collector.ConfigPath); err != nil {
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
	// Declared out here so the outcome survives the StartService block and reaches the result.
	// A --no-start install never reaches the attempt, and reports the zero value: not applicable.
	var lingerOutcome service.LingerOutcome
	if opts.StartService {
		// Restart when something is already running, rather than Load.
		//
		// Load is a no-op on a live service in every backend: `systemctl enable --now` does not
		// restart an active unit, launchd's bootstrap refuses an already-bootstrapped label, and the
		// supervised backend returns early on a live pid. That is correct for a first install and
		// wrong for every subsequent one, because by this point the install has already rewritten
		// the collector config and, during a package upgrade, the package manager has already
		// replaced the collector binary underneath the running process.
		//
		// Without this, `apt install ./beacon.deb` over an older version left the previous
		// collector serving OTLP -- holding a deleted inode, ignoring the new config -- while the
		// install reported success and the ports answered. The endpoint looked healthy and was a
		// version behind until the machine rebooted. The same path made `endpoint install
		// --splunk-hec-endpoint ...` write a destination that never took effect.
		// Read once and remember it. The same answer decides two things: whether to restart rather
		// than load now, and whether rollback is allowed to unload later. Asking twice would risk
		// the two decisions disagreeing about the state they are reasoning about.
		wasRunning := manager.Status().Running
		tx.ServiceWasRunning = wasRunning
		if wasRunning {
			if err := manager.Restart(); err != nil {
				tx.Rollback(manifest)
				return InstallResult{}, err
			}
		} else if err := manager.Load(); err != nil {
			tx.Rollback(manifest)
			return InstallResult{}, err
		}
		tx.ServiceLoaded = true
		// A systemd --user unit is torn down when the user logs out unless linger is set, so
		// without this a user-mode install silently stops collecting at logout. launchd has no
		// equivalent: its gui/<uid> domain persists for the login session on its own.
		//
		// Best-effort and non-fatal: enabling linger needs privileges a plain user may not
		// have, and refusing to install over it would be worse than collecting until logout.
		// The outcome is recorded so status and doctor can report the gap rather than leaving
		// the user to discover it after their next logout.
		lingerOutcome = manager.EnableLingerIfNeeded()
		if lingerOutcome.Applicable {
			manifest.LingerEnabled = lingerOutcome.Enabled
			manifest.LingerDetail = lingerOutcome.Detail
		}
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
		LingerApplicable:    lingerOutcome.Applicable,
		LingerEnabled:       lingerOutcome.Enabled,
		LingerDetail:        lingerOutcome.Detail,
		LingerRemediation:   lingerOutcome.Remediation,
	}, nil
}

// Uninstall removes the endpoint and reports what it could not remove.
//
// Every step used to discard its error and the function could only return nil, so
// `beacon endpoint uninstall --system` without privileges printed "Endpoint service, config, and
// managed files removed." and exited 0 while the systemd unit stayed enabled -- the collector came
// back at the next reboot, after the operator had been told it was gone.
//
// Removal still continues past a failure rather than stopping at the first one: a partial uninstall
// that removes what it can is more useful than one that abandons the job halfway, and the caller
// needs to hear about every leftover, not just the first. The failures are collected and returned
// together.
func Uninstall(opts UninstallOptions) error {
	cfg := loadOrDefault(opts.UserMode, opts.LogPath)
	// Refuse a system uninstall we cannot carry out, instead of half-doing it and reporting
	// success. Install has always had this gate; uninstall never did, which is the asymmetry that
	// let an unprivileged removal look like it worked.
	if !cfg.UserMode && !HasSystemPrivileges() {
		return fmt.Errorf("removing a system endpoint needs root: rerun with sudo, or pass --user to remove a user-mode install")
	}

	var problems []string
	fail := func(what string, err error) {
		if err != nil && !os.IsNotExist(err) {
			problems = append(problems, fmt.Sprintf("%s: %v", what, err))
		}
	}

	// Unload every backend that could plausibly hold this service, not just the one
	// detection picks now. An install performed under systemd and uninstalled from a
	// container would otherwise leave the unit enabled, and "uninstall" has to mean nothing
	// is left behind. Unload already tolerates a backend that is unusable here.
	manager := service.Manager{UserMode: cfg.UserMode}
	for _, kind := range service.ManagedKinds() {
		fail(fmt.Sprintf("unload %s service", kind), (service.Manager{UserMode: cfg.UserMode, Kind: kind}).Unload())
	}
	if !cfg.UserMode && !opts.KeepUpdater {
		removeUpdaterJob()
	}
	// The managed-ingest forwarder and its credentials go with the endpoint; server-side the
	// device stays registered until it is revoked from the dashboard.
	fail("disconnect managed ingest", asymptote.Disconnect(asymptote.DisconnectOptions{
		UserMode:        cfg.UserMode,
		LogPath:         cfg.LogPath,
		KeepCredentials: opts.KeepConfig,
	}))
	manifest, _ := ReadManifest(cfg.UserMode)
	if !opts.KeepConfig {
		restoreBackups(manifest.Backups)
	}
	for _, path := range manifest.Files {
		fail("remove "+path, os.Remove(path))
	}
	if len(manifest.Files) == 0 {
		if path, err := manager.UnitPath(); err == nil {
			fail("remove "+path, os.Remove(path))
		}
		fail("remove "+cfg.Collector.ConfigPath, os.Remove(cfg.Collector.ConfigPath))
		fail("remove endpoint config", os.Remove(endpointconfig.ConfigPath(cfg.UserMode)))
	}
	if !opts.KeepLogs {
		for _, path := range runtimeLogFiles(cfg.LogPath) {
			fail("remove "+path, os.Remove(path))
		}
	}
	fail("remove install manifest", os.Remove(manifestPath(cfg.UserMode)))

	if len(problems) > 0 {
		return fmt.Errorf("endpoint uninstall left %d item(s) behind:\n  %s",
			len(problems), strings.Join(problems, "\n  "))
	}
	return nil
}

// runtimeLogFiles lists the runtime log and everything rotation has produced from it.
//
// Removing only runtime.jsonl left up to five rotated archives holding retained prompt text and
// command lines on disk after an uninstall that was not asked to keep logs -- the opposite of what
// the operator requested, and invisible because the file they would check was gone.
func runtimeLogFiles(logPath string) []string {
	if logPath == "" {
		return nil
	}
	paths := []string{logPath, logPath + ".lock"}
	if rotated, err := filepath.Glob(logPath + ".*"); err == nil {
		for _, p := range rotated {
			if p != logPath+".lock" {
				paths = append(paths, p)
			}
		}
	}
	return paths
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
		mgr.RemoveUnits()
		return nil
	}
	if _, err := mgr.WriteUnit(selfupdate.SystemBeaconPath()); err != nil {
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
		ManagedIngest: asymptote.Status(effectiveCfg.UserMode, asymptote.StatusOptions{}),
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
	// A re-install (upgrades run one) must not disconnect the machine on paper: the
	// managed_ingest block is owned by connect/disconnect, so carry the existing one over.
	if existing, err := endpointconfig.Load(opts.UserMode); err == nil && existing.ManagedIngest != nil {
		cfg.ManagedIngest = existing.ManagedIngest
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

func preflight(cfg endpointconfig.Config, startService bool, kind service.Kind) error {
	if err := endpointconfig.ValidateDestinations(cfg.Destinations); err != nil {
		return err
	}
	// Replaces a blanket "macOS only" refusal. What actually matters is whether a service
	// manager can run the collector here, which is a property of the host rather than of the
	// operating system name: a Linux VM with systemd is fully supported, and a container
	// without an init system is supported through the supervised backend.
	mgr := service.Manager{UserMode: cfg.UserMode, Kind: kind}
	if !mgr.Available() {
		reason := mgr.UnsupportedReason()
		if kind == service.KindAuto {
			// Detection picked something unusable, which should not happen since detection
			// falls back to supervised; surface it rather than failing opaquely.
			return fmt.Errorf("no usable service manager on this host: %s", reason)
		}
		return fmt.Errorf("--service=%s is not usable here: %s", kind, reason)
	}
	if !cfg.UserMode && !HasSystemPrivileges() {
		return fmt.Errorf("system install needs elevated privileges; %s, or omit --system for the default user install",
			SystemPrivilegeHint())
	}
	if !startService {
		return nil
	}
	grpcAvailable := endpointcollector.PortAvailable(cfg.Collector.GRPCPort)
	httpAvailable := endpointcollector.PortAvailable(cfg.Collector.HTTPPort)
	if grpcAvailable && httpAvailable {
		return nil
	}
	if existingCollectorReady(cfg, kind) {
		return nil
	}
	if err := endpointcollector.WaitForPortsAvailable(cfg.Collector.GRPCPort, cfg.Collector.HTTPPort, 10*time.Second); err != nil {
		return fmt.Errorf("%w; if this persists, another process may be using Beacon's OTLP receiver ports", err)
	}
	return nil
}

// The kind is threaded through rather than re-detected: preflight already honours an explicit
// --service=, and auto-detecting here would consult a different backend than the one the install
// is about to use.
func existingCollectorReady(cfg endpointconfig.Config, kind service.Kind) bool {
	if !(service.Manager{UserMode: cfg.UserMode, Kind: kind}).Status().Loaded {
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
		case "cline":
			return paths, fmt.Errorf("Cline telemetry is installed with `beacon endpoint hooks install --harness cline`, not endpoint install")
		case "pi", "pi_cli":
			return paths, fmt.Errorf("Pi telemetry is installed with `beacon endpoint hooks install --harness pi`, not endpoint install")
		case "omp", "oh_my_pi", "oh-my-pi":
			return paths, fmt.Errorf("Oh My Pi telemetry is installed with `beacon endpoint hooks install --harness omp`, not endpoint install")
		// Qwen Code has no OpenTelemetry export to point at the local collector, so `endpoint
		// install --harness qwen` has nothing to configure. Saying so beats the generic
		// "unsupported harness", which reads as "Beacon does not support Qwen Code" when the
		// support exists behind a different command.
		case "qwen", "qwen_code":
			return paths, fmt.Errorf("Qwen Code telemetry is installed with `beacon endpoint hooks install --harness qwen`, not endpoint install")
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
